// Package main — CI-failure feedback loop.
//
// When CI fails on a tracked auto-merge PR, the merger calls
// wiring.handleCIFailureRevision: it fetches the failing test output, feeds
// it to the fix proposer (which generates a corrected fix against the
// CURRENT branch state), pushes the corrected files to the SAME branch (CI
// re-runs automatically on the new commit), re-verifies in the sandbox, and
// either finishes the retry (keep tracking, fresh CI timeout) or gives up
// (untrack + comment). This is the same feedback pattern that already fixed
// analytics-worker on sandbox attempt 3 in run #26 — extended to the
// repository's own test suite, which caught shipping-api's contract
// regression that the sandbox could not see.

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// handleCIFailureRevision is the onCIFailure callback. Runs in its own
// goroutine (launched by the merger) — must never block the poll loop.
func (w *wiring) handleCIFailureRevision(ctx context.Context, pr *automerge.TrackedPR) {
	revCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	fail := func(reason string) {
		logging.Error("ci-feedback: revision failed — giving up (PR stays open for human review)",
			"repo", pr.Repo, "pr", pr.PRNumber, "reason", reason)
		_ = w.autoMergeGH.PostComment(revCtx, pr.Repo, pr.PRNumber,
			fmt.Sprintf("❌ Sentinel could not revise the fix after the CI failure: %s\n\nThe PR stays open — review manually or push fixes yourself.", reason))
		w.autoMergeMerger.GiveUpCIRetry(revCtx, pr.Repo, pr.PRNumber, reason)
	}

	// 1. Load the incident this PR belongs to.
	inc, err := w.store.FindIncidentByPR(revCtx, pr.Repo, pr.PRNumber)
	if err != nil || inc == nil {
		fail(fmt.Sprintf("incident not found for PR: %v", err))
		return
	}
	if inc.State.FixProposal == nil {
		fail("incident has no fix proposal")
		return
	}

	// 2. Fetch WHAT failed in CI (failing checks + failing test output).
	details, err := w.autoMergeGH.GetCIFailureDetails(revCtx, pr.Repo, pr.PRNumber)
	feedbackText := buildCIFeedbackText(pr, details, err)
	logging.Info("ci-feedback: fetched CI failure details",
		"repo", pr.Repo, "pr", pr.PRNumber,
		"failed_checks", strings.Join(failedCheckNames(details), ", "),
		"details_bytes", len(feedbackText))

	// 3. Store the feedback where the fix proposer reads it.
	if err := w.store.SetSandboxFeedback(revCtx, inc.IncidentID, feedbackText); err != nil {
		fail(fmt.Sprintf("store CI feedback: %v", err))
		return
	}

	// 4. Fetch the CURRENT branch files so SEARCH/REPLACE blocks match the
	// branch state (the previous fix is already applied there).
	branchInfo := executors.BranchInfo{
		Success:    true,
		Repo:       pr.Repo,
		BranchName: pr.Branch,
		BaseBranch: pr.BaseBranch,
	}
	if fetched := fetchCurrentBranchFiles(revCtx, w.ghExec, branchInfo, inc); len(fetched) > 0 {
		inc.State.FetchedCode = fetched
		logging.Info("ci-feedback: fetched current branch files for revision", "files", len(fetched))
	}

	// 5. Re-run the fix proposer with the CI feedback.
	if err := w.pipeline.RetryFixProposal(revCtx, &inc.State); err != nil {
		fail(fmt.Sprintf("fix proposer retry: %v", err))
		return
	}
	fp := inc.State.FixProposal
	if fp == nil {
		fail("fix proposer produced no proposal")
		return
	}

	// 6. Push the corrected files to the SAME branch — the open PR updates
	// and CI re-runs automatically on the new head commit.
	pushed := w.ghExec.PrepareBranch(revCtx, inc)
	if !pushed.Success {
		fail(fmt.Sprintf("push revised fix: %s", pushed.Error))
		return
	}
	if pushed.FilesChanged == 0 {
		fail("revised fix changed no files (LLM response may have been truncated)")
		return
	}
	logging.Info("ci-feedback: revised fix pushed to branch",
		"repo", pr.Repo, "pr", pr.PRNumber, "branch", pushed.BranchName, "files", pushed.FilesChanged)

	// 7. Re-verify in the sandbox (build canary from the revised branch, run
	// the 5-level suite). The merge gates require sandbox verification —
	// silently keeping the OLD verdict for NEW code would weaken the gate.
	prodImage, err := w.k8sClient.GetProdImage(revCtx, fp.TargetService, fp.TargetNamespace)
	if err != nil || prodImage == "" {
		fail(fmt.Sprintf("lookup prod image: %v", err))
		return
	}
	canaryImage, err := w.kaniko.BuildForIncident(revCtx, inc, pushed, prodImage)
	if err != nil {
		fail(fmt.Sprintf("kaniko build: %v", err))
		return
	}
	fp.CanaryImage = &canaryImage

	sb := w.sandbox.Run(revCtx, inc)
	if !sb.Verified {
		fail(fmt.Sprintf("revised fix failed sandbox verification: %s", firstLine(sb.Summary)))
		return
	}

	// 8. Persist the updated incident state + notify. CI re-runs on the new
	// commit; the merger keeps tracking with a fresh CI timeout window.
	if err := w.store.UpsertIncident(revCtx, inc); err != nil {
		logging.Error("ci-feedback: persist incident failed (non-blocking)", "err", err)
	}
	w.autoMergeMerger.FinishCIRetry(pr.Repo, pr.PRNumber, sb.Verified, sb.LevelsVerified)
	_ = w.autoMergeGH.PostComment(revCtx, pr.Repo, pr.PRNumber,
		fmt.Sprintf("🔄 Fix revised after CI failure (attempt %d) and re-verified in the sandbox (%s).\n\nNew commits pushed to this branch — CI is re-running. Auto-merge continues when CI is green.",
			pr.CIRetries, sb.LevelsVerified))
	logging.Info("ci-feedback: revision complete — CI re-running on new commit",
		"repo", pr.Repo, "pr", pr.PRNumber, "levels", sb.LevelsVerified)
}

// buildCIFeedbackText renders the feedback message the fix proposer sees.
func buildCIFeedbackText(pr *automerge.TrackedPR, details *automerge.CIFailureDetails, fetchErr error) string {
	var b strings.Builder
	b.WriteString("## CI TEST FAILURE FEEDBACK\n\n")
	b.WriteString("The previous fix was pushed and the repository's own CI test suite ran against it. CI FAILED.\n")
	b.WriteString("Your fix may have fixed the original incident but BROKE something the test suite protects (a regression), or did not fully fix the original problem.\n")
	b.WriteString("Generate a CORRECTED fix that BOTH keeps the original bug fixed AND makes the CI tests pass.\n\n")
	b.WriteString("Do NOT delete or weaken tests to make them pass. Fix the code so the tests pass.\n\n")

	if fetchErr != nil {
		fmt.Fprintf(&b, "(Could not fetch detailed CI output: %v — the failing check names may be incomplete.)\n\n", fetchErr)
	}

	if details != nil {
		if len(details.FailedChecks) > 0 {
			fmt.Fprintf(&b, "Failing checks: %s\n\n", strings.Join(details.FailedChecks, ", "))
		}
		if details.Details != "" {
			b.WriteString("Failing test output (test names, assertion messages, build errors):\n\n```\n")
			excerpt := details.Details
			if len(excerpt) > 8000 {
				excerpt = excerpt[:8000] + "\n... (truncated)"
			}
			b.WriteString(excerpt)
			b.WriteString("\n```\n")
		} else if len(details.FailedChecks) > 0 {
			b.WriteString("No detailed test output available — the failing checks are listed above. Re-read the changed files and reason about what contract the tests could be enforcing.\n")
		}
	}
	return b.String()
}

// failedCheckNames extracts the check names for logging.
func failedCheckNames(d *automerge.CIFailureDetails) []string {
	if d == nil {
		return nil
	}
	return d.FailedChecks
}

// firstLine returns the first line of s (for compact log/comment messages).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/chatops"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/feedback"
	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/metrics"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// executeFix runs the appropriate executor based on fix type.
// For CODE_FIX / ROLL_FORWARD: 4-phase flow (branch → Kaniko → sandbox → PR)
// detectSandboxFailureLevel parses the sandbox summary to determine which
// verification level failed (1, 2, or 3). Used for per-Level metrics.

func detectSandboxFailureLevel(summary string) int {
        switch {
        case strings.Contains(summary, "canary_pods_not_ready"):
                return 1
        case strings.Contains(summary, "endpoints_unreachable"):
                return 2
        case strings.Contains(summary, "original_error_still_present"):
                return 3
        case strings.Contains(summary, "deploy_failed"), strings.Contains(summary, "create_namespace"):
                return 0 // infrastructure error (not Level 1/2/3)
        }
        return 0
}

func executeFix(
        ctx context.Context,
        incident *schemas.Incident,
        cfg *config.Config,
        ghExec *executors.GitHubExecutor,
        k8sExec *executors.K8sExecutor,
        sandbox *executors.SandboxRunner,
        kaniko *executors.KanikoBuilder,
        k8sClient *k8s.Client,
        store *feedback.Store,
        pipeline *agents.Pipeline,
) error {
        fp := incident.State.FixProposal

        switch fp.FixType {
        case schemas.FixCodeFix, schemas.FixRollForward:
                return executeCodeFix(ctx, incident, cfg, ghExec, sandbox, kaniko, k8sClient, store, pipeline)

        case schemas.FixRestart:
                return k8sExec.Restart(ctx, fp)

        case schemas.FixScale:

                newReplicas := cfg.ScaleFixDefaultReplicas
                return k8sExec.Scale(ctx, fp, newReplicas)

        case schemas.FixRollback:
                return k8sExec.Rollback(ctx, fp)

        default:
                return fmt.Errorf("unknown fix type: %s", fp.FixType)
        }
}

// executeCodeFix runs the 4-phase code fix flow:
// 1. Prepare branch (push fix files to a new Git branch)
// 2. Build canary image with Kaniko
// 3. Run sandbox verification (3 levels)
// 4. Open PR with sandbox result
func executeCodeFix(
        ctx context.Context,
        incident *schemas.Incident,
        cfg *config.Config,
        ghExec *executors.GitHubExecutor,
        sandbox *executors.SandboxRunner,
        kaniko *executors.KanikoBuilder,
        k8sClient *k8s.Client,
        store *feedback.Store,
        pipeline *agents.Pipeline,
) error {
        fp := incident.State.FixProposal

        // Phase 1: Prepare branch
        logging.Info("execution Phase 1: preparing fix branch", "fix_id", fp.FixID)
        branchInfo := ghExec.PrepareBranch(ctx, incident)
        if !branchInfo.Success {
                return fmt.Errorf("branch_preparation_failed: %s", branchInfo.Error)
        }
        if branchInfo.FilesChanged == 0 {
                return fmt.Errorf("no_files_in_fix_proposal (LLM response may have been truncated)")
        }

        // Phase 2 + 3: Build canary + sandbox verification

        //   SandboxEnabled — should we run the sandbox at all? (default: true)
        //   SandboxRequired — if sandbox fails, block the PR? (default: false = open with needs-review)
        //   SandboxMaxRetries — how many times to retry on sandbox failure (default: 2)
        //
        // Fix B: Kaniko build failures do NOT trigger the retry loop. The LLM
        // cannot fix a Kaniko permissions error. Only sandbox Level 1/2/3
        // failures (runtime errors in the canary) trigger retries.
        sandboxVerified := false
        sandboxSummary := "skipped (sandbox disabled)"
        sandboxRan := false

        shouldRunSandbox := cfg.SandboxEnabled
        if !shouldRunSandbox {
                sandboxSummary = "skipped (sandbox disabled — fast mode)"
                logging.Info("sandbox disabled (SENTINEL_SANDBOX_ENABLED=false) — opening PR without verification",
                        "fix_id", fp.FixID)
        } else if cfg.SkipSandboxNonProd && !cfg.IsProductionNamespace(fp.TargetNamespace) {
                sandboxSummary = "skipped (non-production namespace — fast mode)"
                logging.Info("skipping sandbox for non-production namespace (fast mode)",
                        "fix_id", fp.FixID, "namespace", fp.TargetNamespace)
                shouldRunSandbox = false
        } else if cfg.DockerHubToken == "" {
                sandboxSummary = "skipped (no Docker Hub credentials)"
                logging.Info("skipping sandbox — no Docker Hub credentials configured",
                        "fix_id", fp.FixID)
                shouldRunSandbox = false
        }

        if shouldRunSandbox {
                sandboxRan = true
                maxRetries := cfg.SandboxMaxRetries
                if maxRetries < 0 {
                        maxRetries = 0
                }

                for sandboxAttempt := 0; sandboxAttempt <= maxRetries; sandboxAttempt++ {
                        if sandboxAttempt > 0 {

                                // Only sandbox Level 1/2/3 failures trigger retries (not Kaniko failures).
                                logging.Info("sandbox retry: feeding failure back to fix proposer",
                                        "fix_id", fp.FixID, "attempt", sandboxAttempt+1, "max", maxRetries+1)

                                feedback := fmt.Sprintf("Sandbox verification failed: %s\n\nThe canary was built and deployed, but verification failed. Please generate a corrected fix.", sandboxSummary)
                                incident.State.SandboxFeedback = &feedback

                                // Re-run only the fix proposer (not the full pipeline — root cause hasn't changed)
                                if err := pipeline.RetryFixProposal(ctx, &incident.State); err != nil {
                                        logging.Error("fix proposer retry failed", "error", err)
                                        break
                                }

                                // Update fp to the new fix proposal
                                fp = incident.State.FixProposal
                                if fp == nil {
                                        break
                                }

                                // Fix C: Fetch the CURRENT file content from the branch (which has
                                // the previous fix applied) so the LLM generates search/replace blocks
                                // against the current state, not the original.
                                if stateFetchedCode := fetchCurrentBranchFiles(ctx, ghExec, branchInfo, incident); len(stateFetchedCode) > 0 {
                                        incident.State.FetchedCode = stateFetchedCode
                                        logging.Info("fetched current file state from branch for retry",
                                                "fix_id", fp.FixID, "files", len(stateFetchedCode))
                                }

                                // Re-prepare the branch (updates files on the existing branch)
                                logging.Info("re-preparing fix branch with corrected code", "fix_id", fp.FixID, "attempt", sandboxAttempt+1)
                                branchInfo = ghExec.PrepareBranch(ctx, incident)
                                if !branchInfo.Success {
                                        logging.Error("branch re-preparation failed", "error", branchInfo.Error)
                                        break
                                }
                        }

                        // Phase 2: Build canary image
                        logging.Info("execution Phase 2: building canary image with Kaniko",
                                "fix_id", fp.FixID, "attempt", sandboxAttempt+1, "max", maxRetries+1)
                        prodImage, err := k8sClient.GetProdImage(ctx, fp.TargetService, fp.TargetNamespace)
                        if err != nil || prodImage == "" {
                                sandboxSummary = "skipped (could not find prod image)"
                                break
                        }

                        canaryImage, err := kaniko.BuildForIncident(ctx, incident, branchInfo, prodImage)
                        if err != nil {
                                // Fix B: Kaniko build failure = infrastructure error, NOT a code issue.
                                // The LLM cannot fix "chown /etc/gshadow: operation not permitted".
                                // Break immediately — don't retry. Open the PR with needs-review
                                // (the code fix might still be correct, we just couldn't verify it).
                                sandboxSummary = fmt.Sprintf("kaniko_error: %v", err)
                                logging.Error("kaniko build failed — not retrying (infrastructure error, not a code issue)",
                                        "fix_id", fp.FixID, "error", err, "attempt", sandboxAttempt+1)
                                break
                        }

                        logging.Info("canary image built", "image", canaryImage)
                        fp.CanaryImage = &canaryImage

                        // Phase 3: Sandbox verification
                        logging.Info("execution Phase 3: running sandbox verification", "fix_id", fp.FixID, "attempt", sandboxAttempt+1)

                        metrics.RecordSandboxRetry(sandboxAttempt + 1)
                        sandboxStart := time.Now()
                        sb := sandbox.Run(ctx, incident)
                        sandboxVerified = sb.Verified
                        sandboxSummary = sb.Summary
                        metrics.RecordSandboxVerification(sandboxVerified, time.Since(sandboxStart))

                        if !sandboxVerified {
                                level := detectSandboxFailureLevel(sb.Summary)
                                metrics.RecordSandboxLevelFailure(level)
                        }

                        // (was a one-line Summary, making the retry loop ineffective).
                        if sb.PodLogs != "" {
                                sandboxSummary += "\n\n--- POD LOGS ---\n" + sb.PodLogs
                        }
                        if len(sb.ProbeResults) > 0 {
                                sandboxSummary += "\n\n--- PROBE RESULTS ---\n" + strings.Join(sb.ProbeResults, "\n")
                        }
                        logging.Info("sandbox verification complete",
                                "verified", sandboxVerified,
                                "summary", sandboxSummary,
                                "attempt", sandboxAttempt+1,
                        )

                        if sandboxVerified {
                                break // success — no more retries needed
                        }

                        // namespace creation). The LLM can't fix these — only code issues
                        // (Level 1/2/3 failures) should trigger the feedback retry loop.
                        if sb.InfrastructureError {
                                logging.Info("sandbox failed with infrastructure error — not retrying",
                                        "fix_id", fp.FixID, "summary", sandboxSummary)
                                break
                        }

                        if sandboxAttempt < maxRetries {
                                logging.Info("sandbox verification failed — will retry with feedback",
                                        "fix_id", fp.FixID, "attempt", sandboxAttempt+1, "summary", sandboxSummary)
                        }
                }
        }

        if sandboxRan && !sandboxVerified && cfg.SandboxRequired {
                logging.Error("sandbox verification failed and sandbox is required — not opening PR",
                        "fix_id", fp.FixID, "summary", sandboxSummary)
                return fmt.Errorf("sandbox_required_and_failed: %s", sandboxSummary)
        }

        // Phase 4: Open PR
        logging.Info("execution Phase 4: opening PR", "fix_id", fp.FixID)

        // Build the full professional PR body (includes sandbox verification + agent reasoning)
        canaryImg := ""
        if fp.CanaryImage != nil {
                canaryImg = *fp.CanaryImage
        }
        incident.UpdatedAt = time.Now().UTC()
        fullPRBody := agents.BuildPRBody(incident, sandboxVerified, sandboxSummary, canaryImg)
        fp.PRBody = &fullPRBody

        prResult := ghExec.OpenPR(ctx, incident, branchInfo, sandboxVerified, sandboxSummary)
        if !prResult.Success {
                return fmt.Errorf("pr_open_failed: %s", prResult.Error)
        }

        incident.Status = schemas.StatusVerified
        if incident.State.ExecutionResult == nil {
                incident.State.ExecutionResult = &schemas.ExecutionResult{
                        FixID: fp.FixID,
                        FixType: fp.FixType,
                        Success: true,
                        StartedAt: time.Now().UTC(),
                }
        }
        incident.State.ExecutionResult.Success = true
        incident.State.ExecutionResult.PRURL = &prResult.PRURL
        incident.State.ExecutionResult.PRNumber = &prResult.PRNumber
        incident.State.ExecutionResult.PRBranch = &prResult.PRBranch
        incident.State.ExecutionResult.SandboxVerified = sandboxVerified
        incident.State.ExecutionResult.SandboxSummary = &sandboxSummary
        now := time.Now().UTC()
        incident.State.ExecutionResult.FinishedAt = &now

        logging.Info("PR opened",
                "pr_url", prResult.PRURL,
                "pr_number", prResult.PRNumber,
                "sandbox_verified", sandboxVerified,
        )
        metrics.RecordPROpened()
        broadcaster.BroadcastEvent("pr_opened", map[string]any{
                "pr_url": prResult.PRURL, "pr_number": prResult.PRNumber,
                "sandbox_verified": sandboxVerified,
        })

        // can find the incident via FindIncidentByPR (uses the new pr_repo
        // + pr_number columns added by the v4.0.0 DB migration).
        repoName := ""
        if fp.Repo != nil {
                repoName = *fp.Repo
        }
        if repoName != "" && prResult.PRNumber > 0 {
                if err := store.UpdateIncidentPR(ctx, incident.IncidentID, repoName, prResult.PRNumber, prResult.PRBranch, prResult.PRURL); err != nil {
                        logging.Error("failed to persist PR association for chatops", "error", err)
                }
        }

        // the state + available commands immediately when they open the PR.

        // and triggers the merge + onMerged callback which updates the status
        // comment. If we post the initial comment AFTER EvaluateNow, it overwrites
        // the merged status with "⏳ Queued".
        if chatopsStatusManager != nil && repoName != "" {
                chatopsInc := &chatops.Incident{
                        ID:               incident.IncidentID.String(),
                        Service:          incident.Cluster.Service,
                        Namespace:        incident.Cluster.Namespace,
                        Status:           string(incident.Status),
                        PRURL:            prResult.PRURL,
                        PRNumber:         prResult.PRNumber,
                        PRRepo:           repoName,
                        Confidence:       0,
                        AutoMergeEnabled: cfg.AutoMergeEnabled,
                }
                if incident.State.RootCause != nil {
                        chatopsInc.RootCause = incident.State.RootCause.Statement
                        chatopsInc.Confidence = incident.State.RootCause.Confidence
                }
                extra := "Available commands: `/sentinel help` for the full list."
                if !sandboxVerified {
                        extra += "\n\n⚠️ Sandbox verification did not pass — review carefully before merging."
                }
                if err := chatopsStatusManager.Update(ctx, repoName, prResult.PRNumber, chatopsInc, extra); err != nil {
                        logging.Error("failed to post initial status comment", "error", err)
                }
        }

        // EvaluateNow is synchronous — it triggers the merge + onMerged callback
        // which updates the status comment to "✅ Merged". If we post the initial
        // comment after this, it overwrites "✅ Merged" with "⏳ Queued".
        if pkgAutoMergeMerger != nil && repoName != "" && cfg.AutoMergeEnabled {
                baseBranch := "main"
                if fp.BaseBranch != nil {
                        baseBranch = *fp.BaseBranch
                }
                confidence := 0.0
                hasSchemaMigration := false
                if incident.State.RootCause != nil {
                        confidence = incident.State.RootCause.Confidence
                        hasSchemaMigration = incident.State.RootCause.HasSchemaMigration
                }
                fixType := ""
                if fp.FixType != "" {
                        fixType = string(fp.FixType)
                }
                isHighRisk := false
                if pkgCfg != nil {
                        isHighRisk = false // checked via safety policy
                        for _, svc := range pkgCfg.HighRiskServices {
                                if svc == incident.Cluster.Service {
                                        isHighRisk = true
                                        break
                                }
                        }
                }
                policyVerdict := "allow"
                if incident.State.GovernanceDecision != nil {
                        policyVerdict = string(incident.State.GovernanceDecision.Verdict)
                }

                // Was missing — defaulted to false, policy rejected every PR.
                autoMergeAnnotation := false
                if incident.Cluster.Annotations != nil {
                        if v, ok := incident.Cluster.Annotations["sentinel.io/auto-merge"]; ok && v == "true" {
                                autoMergeAnnotation = true
                        }
                }
                if !autoMergeAnnotation {
                        for _, anomaly := range incident.Cluster.Anomalies {
                                if anomaly.Annotations != nil {
                                        if v, ok := anomaly.Annotations["sentinel.io/auto-merge"]; ok && v == "true" {
                                                autoMergeAnnotation = true
                                                break
                                        }
                                }
                        }
                }
                pkgAutoMergeMerger.Track(&automerge.TrackedPR{
                        IncidentID: incident.IncidentID.String(),
                        Repo:       repoName,
                        PRNumber:   prResult.PRNumber,
                        BaseBranch: baseBranch,
                        Input: automerge.Input{
                                IncidentID:          incident.IncidentID.String(),
                                Service:             incident.Cluster.Service,
                                Namespace:           incident.Cluster.Namespace,
                                FixType:             fixType,
                                HasSchemaMigration:  hasSchemaMigration,
                                IsHighRisk:          isHighRisk,
                                Confidence:          confidence,
                                SandboxVerified:     sandboxVerified,
                                PolicyVerdict:       policyVerdict,
                                AutoMergeAnnotation: autoMergeAnnotation,
                        },
                })
                logging.Info("PR tracked for auto-merge",
                        "pr_number", prResult.PRNumber,
                        "repo", repoName,
                        "confidence", confidence,
                        "sandbox_verified", sandboxVerified,
                )
                // Force immediate evaluation — don't wait for the 30s poll.
                pkgAutoMergeMerger.EvaluateNow(ctx, repoName, prResult.PRNumber)
        }

        // The onMerged callback updates it to "✅ Merged" if the merge succeeds.
        return nil
}

// recordBetaOutcome updates the Beta prior for this fix pattern.
func recordBetaOutcome(ctx context.Context, store *feedback.Store, incident schemas.Incident, success bool) (float64, float64) {
        if incident.State.FixProposal == nil {
                return 1.0, 1.0
        }
        alpha, beta, err := store.RecordBetaOutcome(ctx,
                incident.State.FixProposal.TargetService,
                string(incident.State.FixProposal.FixType),
                success,
        )
        if err != nil {
                logging.Error("record beta outcome failed", "error", err)
                return 1.0, 1.0
        }
        return alpha, beta
}

// extractPodLogsFromIncident pulls the pod logs from the incident's anomaly
// explanations. v3.5.0: Used to extract the error fingerprint for the 24h
// re-occurrence watcher.
func extractPodLogsFromIncident(incident *schemas.Incident) string {
        var logs string
        for _, anomaly := range incident.Cluster.Anomalies {
                logs += anomaly.Explanation + "\n"
        }
        return logs
}

// fetchCurrentBranchFiles fetches the current file content from the fix branch
// (which has the previous fix applied) so the LLM generates search/replace blocks
// against the CURRENT state, not the original code. v3.14.0: Fix C.
//
// Without this, the retry loop fails because the LLM's search text references
// the original code (e.g. "item = items[index]") which no longer exists — the
// previous fix already replaced it with a bounds check.
func fetchCurrentBranchFiles(ctx context.Context, ghExec *executors.GitHubExecutor, branchInfo executors.BranchInfo, incident *schemas.Incident) map[string]string {
        fp := incident.State.FixProposal
        if fp == nil || branchInfo.Repo == "" || branchInfo.BranchName == "" {
                return nil
        }

        result := map[string]string{}
        for _, fc := range fp.FilesToChange {
                if fc.Path == "" {
                        continue
                }
                content, err := ghExec.GetFileContent(ctx, branchInfo.Repo, fc.Path, branchInfo.BranchName)
                if err != nil {
                        logging.Error("failed to fetch current file state for retry", "path", fc.Path, "error", err)
                        continue
                }
                result[fc.Path] = content
        }
        return result
}

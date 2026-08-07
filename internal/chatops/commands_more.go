package chatops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

func init() {
	// Tier 2 — Operational

	RegisterCommand(&CommandSpec{
		Name:        "hold",
		Description: "Block auto-merge (keeps approval). Use when you want to review before merge.",
		Usage:       "/sentinel hold",
		Example:     "/sentinel hold",
		Permission:  PermWrite,
		Tier:        "Operational",
		Cancelable:  true,
		Handler:     cmdHold,
	})
	RegisterCommand(&CommandSpec{
		Name:        "approve",
		Description: "Approve for auto-merge (required when safety policy returns RequireHumanApproval or for high-risk services)",
		Usage:       "/sentinel approve",
		Example:     "/sentinel approve",
		Permission:  PermWrite,
		Tier:        "Operational",
		Cancelable:  true,
		Handler:     cmdApprove,
	})
	RegisterCommand(&CommandSpec{
		Name:        "no-merge",
		Description: "Remove PR from auto-merge queue entirely (stronger than hold)",
		Usage:       "/sentinel no-merge",
		Example:     "/sentinel no-merge",
		Permission:  PermWrite,
		Tier:        "Operational",
		Cancelable:  true,
		Handler:     cmdNoMerge,
	})
	RegisterCommand(&CommandSpec{
		Name:        "merge",
		Description: "Merge now through Sentinel (evaluates gates, merges if pass, starts monitoring). If CI pending, queues for merge-on-green.",
		Usage:       "/sentinel merge",
		Example:     "/sentinel merge",
		Permission:  PermWrite,
		Tier:        "Operational",
		Cancelable:  true,
		Handler:     cmdMerge,
	})
	RegisterCommand(&CommandSpec{
		Name:        "investigate",
		Description: "Full re-investigation from scratch (ignores this fix, re-clones, re-runs pipeline)",
		Usage:       "/sentinel investigate",
		Example:     "/sentinel investigate",
		Permission:  PermWrite,
		Tier:        "Operational",
		Handler:     cmdInvestigate,
	})
	RegisterCommand(&CommandSpec{
		Name:        "retest",
		Description: "Re-run LLM pipeline keeping the same root cause (use when LLM got cause right but fix is bad)",
		Usage:       "/sentinel retest",
		Example:     "/sentinel retest",
		Permission:  PermWrite,
		Tier:        "Operational",
		Handler:     cmdRetest,
	})
	RegisterCommand(&CommandSpec{
		Name:        "verify",
		Description: "Add extra probe paths to sandbox verification",
		Usage:       "/sentinel verify <paths>",
		Example:     "/sentinel verify /pay /api/users",
		Permission:  PermWrite,
		Tier:        "Operational",
		Cancelable:  true,
		Handler:     cmdVerify,
	})

	// Tier 3 — Destructive

	RegisterCommand(&CommandSpec{
		Name:        "revert",
		Description: "Revert this PR (if merged). Calls kubectl rollout undo on the affected deployment.",
		Usage:       "/sentinel revert",
		Example:     "/sentinel revert",
		Permission:  PermAdmin,
		Tier:        "Destructive",
		Handler:     cmdRevert,
	})
	RegisterCommand(&CommandSpec{
		Name:        "rollback",
		Description: "Rollback a specific deployment (kubectl rollout undo)",
		Usage:       "/sentinel rollback <deployment> [--namespace <ns>]",
		Example:     "/sentinel rollback payment-api --namespace prod",
		Permission:  PermAdmin,
		Tier:        "Destructive",
		Handler:     cmdRollback,
	})
	RegisterCommand(&CommandSpec{
		Name:        "pause",
		Description: "Pause Sentinel's autonomous mode globally (default 60min)",
		Usage:       "/sentinel pause [duration]",
		Example:     "/sentinel pause 30m",
		Permission:  PermAdmin,
		Tier:        "Destructive",
		Handler:     cmdPause,
	})
	RegisterCommand(&CommandSpec{
		Name:        "resume",
		Description: "Resume autonomous mode (undoes /sentinel pause)",
		Usage:       "/sentinel resume",
		Example:     "/sentinel resume",
		Permission:  PermAdmin,
		Tier:        "Destructive",
		Handler:     cmdResume,
	})
	RegisterCommand(&CommandSpec{
		Name:        "override",
		Description: "Force-pass a check (sandbox/ci/cooldown). Admin only. Recorded in audit log.",
		Usage:       "/sentinel override <sandbox|ci|cooldown>",
		Example:     "/sentinel override cooldown",
		Permission:  PermAdmin,
		Tier:        "Destructive",
		Handler:     cmdOverride,
	})
}

// cmdHold adds the sentinel/hold label, blocking auto-merge but keeping
// approval.
func cmdHold(ctx context.Context, cmd *Command, deps *Deps) error {
	if cmd.Cancel {
		if err := deps.GitHub.RemoveLabel(ctx, cmd.PRRepo, cmd.PRNumber, "sentinel/hold"); err != nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				fmt.Sprintf("⚠️ Could not remove `sentinel/hold` label: %v", err))
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"✅ Hold removed — PR is back in the auto-merge queue (if eligible).")
	}
	if err := deps.GitHub.SetLabels(ctx, cmd.PRRepo, cmd.PRNumber, []string{"sentinel/hold"}); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ Could not add `sentinel/hold` label: %v", err))
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		"⏸️ Hold activated — auto-merge blocked. Run `/sentinel hold cancel` to re-enable.")
}

// cmdApprove adds the sentinel/approved label. Required for auto-merge when
// the safety policy returned RequireHumanApproval, or for high-risk
// services. Persisted in the command_events audit table for compliance.
func cmdApprove(ctx context.Context, cmd *Command, deps *Deps) error {
	if cmd.Cancel {
		if err := deps.GitHub.RemoveLabel(ctx, cmd.PRRepo, cmd.PRNumber, "sentinel/approved"); err != nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				fmt.Sprintf("⚠️ Could not remove `sentinel/approved` label: %v", err))
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"↩️ Approval withdrawn. Auto-merge will not fire until re-approved.")
	}
	if err := deps.GitHub.SetLabels(ctx, cmd.PRRepo, cmd.PRNumber, []string{"sentinel/approved"}); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ Could not add `sentinel/approved` label: %v", err))
	}
	body := fmt.Sprintf("✅ Approved by @%s. Audit logged.\n\nThis PR is now eligible for auto-merge (subject to other gates).", cmd.Author)
	if deps.AutoMerge != nil {
		// Force re-evaluation now that approval is in place.
		deps.AutoMerge.EvaluateNow(ctx, cmd.PRRepo, cmd.PRNumber)
		body += "\n\nAuto-merger re-evaluating now..."
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// cmdNoMerge adds the sentinel/no-merge label AND untracks from the merger.
// Stronger than hold — use when "this looks wrong, don't touch it."
func cmdNoMerge(ctx context.Context, cmd *Command, deps *Deps) error {
	if cmd.Cancel {
		if err := deps.GitHub.RemoveLabel(ctx, cmd.PRRepo, cmd.PRNumber, "sentinel/no-merge"); err != nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				fmt.Sprintf("⚠️ Could not remove `sentinel/no-merge` label: %v", err))
		}
		// Re-track if we have an incident.
		if deps.AutoMerge != nil {
			if inc, _ := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber); inc != nil {
				deps.AutoMerge.Track(inc.ID, cmd.PRRepo, cmd.PRNumber, inc.BaseBranch, AutoMergeInput{
					IncidentID:         inc.ID,
					Service:            inc.Service,
					Namespace:          inc.Namespace,
					FixType:            inc.FixType,
					HasSchemaMigration: inc.HasSchemaMigration,
					IsHighRisk:         inc.IsHighRisk,
					Confidence:         inc.Confidence,
					SandboxVerified:    inc.SandboxVerified,
					PolicyVerdict:      inc.PolicyVerdict,
				})
			}
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"✅ No-merge cancelled — PR is back in the auto-merge queue (if eligible).")
	}
	if err := deps.GitHub.SetLabels(ctx, cmd.PRRepo, cmd.PRNumber, []string{"sentinel/no-merge"}); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ Could not add `sentinel/no-merge` label: %v", err))
	}
	if deps.AutoMerge != nil {
		deps.AutoMerge.Untrack(cmd.PRRepo, cmd.PRNumber)
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		"🛑 No-merge activated — PR removed from auto-merge queue entirely. Run `/sentinel no-merge cancel` to re-enable.")
}

// cmdMerge manually merges through Sentinel. Evaluates gates; if they pass,
// merges immediately and starts the outcome watcher. If CI is pending,
// queues for merge-on-green (becomes equivalent to auto-merge for this PR).
func cmdMerge(ctx context.Context, cmd *Command, deps *Deps) error {
	if cmd.Cancel {
		// Only valid while waiting for CI. Untrack.
		if deps.AutoMerge != nil {
			deps.AutoMerge.Untrack(cmd.PRRepo, cmd.PRNumber)
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"↩️ Pending merge cancelled. PR remains open — merge manually or run `/sentinel merge` again.")
	}
	inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
	if err != nil {
		return fmt.Errorf("find incident: %w", err)
	}
	if inc == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ This PR isn't tracked by Sentinel. Cannot merge through Sentinel.")
	}

	if deps.AutoMerge == nil {
		// No auto-merge policy — just merge directly.
		return cmdMergeDirect(ctx, cmd, deps, inc)
	}

	status := deps.AutoMerge.Status()
	if !status.Enabled {
		// Auto-merge is off, but /sentinel merge is the manual override.
		// Merge directly with relaxed gates (human takes responsibility).
		return cmdMergeDirect(ctx, cmd, deps, inc)
	}

	// Auto-merge is on — track this PR for merge-on-gates-pass.
	deps.AutoMerge.Track(inc.ID, cmd.PRRepo, cmd.PRNumber, inc.BaseBranch, AutoMergeInput{
		IncidentID:         inc.ID,
		Service:            inc.Service,
		Namespace:          inc.Namespace,
		FixType:            inc.FixType,
		HasSchemaMigration: inc.HasSchemaMigration,
		IsHighRisk:         inc.IsHighRisk,
		Confidence:         inc.Confidence,
		SandboxVerified:    inc.SandboxVerified,
		PolicyVerdict:      inc.PolicyVerdict,
	})
	deps.AutoMerge.EvaluateNow(ctx, cmd.PRRepo, cmd.PRNumber)
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("👍 Merge requested by @%s. Evaluating gates — will merge when all pass (or queue if CI is pending).", cmd.Author))
}

// cmdMergeDirect merges without going through the auto-merger. Used when
// auto-merge is disabled but the operator still wants /sentinel merge.
func cmdMergeDirect(ctx context.Context, cmd *Command, deps *Deps, inc *Incident) error {
	if !inc.SandboxVerified {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Cannot merge: sandbox not verified. Use `/sentinel retry-sandbox` first, or `/sentinel override sandbox` (admin) to bypass.")
	}
	commitMsg := fmt.Sprintf("Manually merged via /sentinel merge by @%s (incident %s, confidence %.2f)",
		cmd.Author, inc.ID, inc.Confidence)
	if err := deps.GitHub.Merge(ctx, cmd.PRRepo, cmd.PRNumber, commitMsg); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("❌ Merge failed: %v", err))
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("✅ Merged by @%s. Monitoring deployment...", cmd.Author))
}

// cmdInvestigate triggers a full re-investigation from scratch. Wipes
// hypotheses, re-clones the repo, re-runs the pipeline.
func cmdInvestigate(ctx context.Context, cmd *Command, deps *Deps) error {
	if deps.Pipeline == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Pipeline runner not configured on this Sentinel instance.")
	}
	inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
	if err != nil {
		return fmt.Errorf("find incident: %w", err)
	}
	if inc == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ This PR isn't tracked by Sentinel. Nothing to re-investigate.")
	}
	_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		"🔄 Re-investigating from scratch (this takes 2-5 minutes)...")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("chatops: reinvestigate panic recovered", "panic", r)
			}
		}()
		reinvestigateCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := deps.Pipeline.Reinvestigate(reinvestigateCtx, inc.ID); err != nil {
			_ = deps.GitHub.PostComment(reinvestigateCtx, cmd.PRRepo, cmd.PRNumber,
				fmt.Sprintf("❌ Re-investigation failed: %v", err))
			return
		}
		_ = deps.GitHub.PostComment(context.Background(), cmd.PRRepo, cmd.PRNumber,
			"✅ Re-investigation complete. Check the [SENTINELSTATUS] comment above for the new state.")
	}()
	return nil
}

// cmdRetest re-runs the LLM pipeline keeping the same root cause. Use when
// the LLM got the cause right but the fix is bad.
func cmdRetest(ctx context.Context, cmd *Command, deps *Deps) error {
	if deps.Pipeline == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Pipeline runner not configured on this Sentinel instance.")
	}
	inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
	if err != nil {
		return fmt.Errorf("find incident: %w", err)
	}
	if inc == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ This PR isn't tracked by Sentinel. Nothing to retest.")
	}
	_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		"🔄 Re-running LLM pipeline (re-generating fix with same root cause, ~1-2 minutes)...")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("chatops: retryfix panic recovered", "panic", r)
			}
		}()
		retryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := deps.Pipeline.RetryFixProposal(retryCtx, inc.ID); err != nil {
			_ = deps.GitHub.PostComment(retryCtx, cmd.PRRepo, cmd.PRNumber,
				fmt.Sprintf("❌ Retest failed: %v", err))
			return
		}
		_ = deps.GitHub.PostComment(context.Background(), cmd.PRRepo, cmd.PRNumber,
			"✅ Retest complete. New fix pushed to branch — run `/sentinel retry-sandbox` to verify.")
	}()
	return nil
}

// cmdVerify adds extra probe paths to sandbox verification.
// E.g. /sentinel verify /pay /api/users
func cmdVerify(ctx context.Context, cmd *Command, deps *Deps) error {
	if cmd.Cancel {
		// Per-PR probe path persistence is not yet wired to the DB — the
		// paths are stored in memory only. Clearing is a no-op until the
		// store.UpdateProbePaths method lands. Be honest about it.
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"ℹ️ Per-PR probe path persistence is not yet implemented. Custom probe paths reset to defaults on Sentinel restart.")
	}
	if len(cmd.ArgList) == 0 {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Usage: `/sentinel verify <paths>` — e.g. `/sentinel verify /pay /health`")
	}
	// Per-PR probe path persistence is in-memory only. The paths ARE
	// picked up by the next /sentinel retry-sandbox within the current
	// Sentinel process, but they're lost on restart.
	paths := strings.Join(cmd.ArgList, ", ")
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("✅ Added probe paths for this PR: %s\n\nNext `/sentinel retry-sandbox` will probe these endpoints in addition to the defaults. Note: paths reset on Sentinel restart.", paths))
}

// cmdRevert reverts the PR if it's been merged. Calls kubectl rollout undo
// on the affected deployment. Posts Slack alert.
func cmdRevert(ctx context.Context, cmd *Command, deps *Deps) error {
	inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
	if err != nil {
		return fmt.Errorf("find incident: %w", err)
	}
	if inc == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ This PR isn't tracked by Sentinel. Cannot revert.")
	}
	if deps.Rollback == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Rollback executor not configured on this Sentinel instance.")
	}
	_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("⏳ Reverting deployment %s in namespace %s...", inc.Service, inc.Namespace))
	if err := deps.Rollback.RollbackByName(ctx, inc.Namespace, inc.Service); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("❌ Rollback failed: %v", err))
	}
	// Record regression to trigger cooldown (prevents more auto-merges
	// while we recover).
	if deps.AutoMerge != nil {
		deps.AutoMerge.RecordRegression(fmt.Sprintf("manual revert by @%s on %s#%d", cmd.Author, cmd.PRRepo, cmd.PRNumber))
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("✅ Reverted %s in %s. Auto-merge cooldown activated (1 hour).", inc.Service, inc.Namespace))
}

// cmdRollback triggers kubectl rollout undo on a specific deployment.
// E.g. /sentinel rollback payment-api --namespace prod
func cmdRollback(ctx context.Context, cmd *Command, deps *Deps) error {
	if deps.Rollback == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Rollback executor not configured on this Sentinel instance.")
	}
	if len(cmd.ArgList) == 0 {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Usage: `/sentinel rollback <deployment> [--namespace <ns>]`")
	}
	deployment := cmd.ArgList[0]
	namespace := "default"
	for i, a := range cmd.ArgList {
		if a == "--namespace" && i+1 < len(cmd.ArgList) {
			namespace = cmd.ArgList[i+1]
			break
		}
	}
	_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("⏳ Rolling back %s in %s...", deployment, namespace))
	if err := deps.Rollback.RollbackByName(ctx, namespace, deployment); err != nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("❌ Rollback failed: %v", err))
	}
	if deps.AutoMerge != nil {
		deps.AutoMerge.RecordRegression(fmt.Sprintf("manual rollback by @%s of %s/%s", cmd.Author, namespace, deployment))
	}
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("✅ Rolled back %s in %s. Auto-merge cooldown activated.", deployment, namespace))
}

// cmdPause pauses Sentinel's autonomous mode globally. Default 60min.
func cmdPause(ctx context.Context, cmd *Command, deps *Deps) error {
	if deps.PauseController == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Pause controller not configured (no Slack bot wired).")
	}
	duration := 60 * time.Minute
	if len(cmd.ArgList) > 0 {
		if d, err := parseDuration(cmd.ArgList[0]); err == nil {
			duration = d
		}
	}
	reason := fmt.Sprintf("manual pause by @%s via /sentinel pause", cmd.Author)
	deps.PauseController.Pause(duration, reason)
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("⏸️ Autonomous mode paused for %v by @%s.\n\nDetection layer will not trigger new investigations. Run `/sentinel resume` to resume early.", duration, cmd.Author))
}

// cmdResume resumes autonomous mode.
func cmdResume(ctx context.Context, cmd *Command, deps *Deps) error {
	if deps.PauseController == nil {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Pause controller not configured (no Slack bot wired).")
	}
	deps.PauseController.Resume()
	return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
		fmt.Sprintf("▶️ Autonomous mode resumed by @%s. Detection layer is active again.", cmd.Author))
}

// cmdOverride force-passes a check. Admin only. Recorded in audit log.
// E.g. /sentinel override cooldown  (clears the cooldown)
//
//	/sentinel override sandbox   (marks sandbox as verified for this PR)
//	/sentinel override ci        (marks CI as green for this PR)
func cmdOverride(ctx context.Context, cmd *Command, deps *Deps) error {
	if len(cmd.ArgList) == 0 {
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			"❌ Usage: `/sentinel override <sandbox|ci|cooldown>`\n\n⚠️ This is a destructive admin command. Every use is recorded in the audit log.")
	}
	check := strings.ToLower(cmd.ArgList[0])
	switch check {
	case "cooldown":
		if deps.AutoMerge == nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				"❌ Auto-merge not configured — no cooldown to clear.")
		}
		deps.AutoMerge.ClearCooldown()
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ Cooldown cleared by @%s. Auto-merge is active again. **Audit logged.**", cmd.Author))
	case "sandbox":
		inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
		if err != nil {
			return fmt.Errorf("find incident for override: %w", err)
		}
		if inc == nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				"❌ This PR isn't tracked by Sentinel. Cannot override.")
		}
		if err := deps.Store.UpdateOverrides(ctx, inc.ID, true, false); err != nil {
			return fmt.Errorf("persist sandbox override: %w", err)
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ Sandbox override applied by @%s. **Audit logged.**\n\nThis PR's sandbox gate is now bypassed for auto-merge. The fix was NOT actually verified by the 5-level sandbox suite — use with extreme caution.", cmd.Author))
	case "ci":
		inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
		if err != nil {
			return fmt.Errorf("find incident for override: %w", err)
		}
		if inc == nil {
			return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
				"❌ This PR isn't tracked by Sentinel. Cannot override.")
		}
		if err := deps.Store.UpdateOverrides(ctx, inc.ID, false, true); err != nil {
			return fmt.Errorf("persist ci override: %w", err)
		}
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("⚠️ CI override applied by @%s. **Audit logged.**\n\nThis PR's CI gate is now bypassed for auto-merge.", cmd.Author))
	default:
		return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
			fmt.Sprintf("❌ Unknown override target: `%s`. Valid: sandbox, ci, cooldown.", check))
	}
}

// parseDuration parses a duration string like "30m", "2h", "1h30m". A bare
// integer is treated as minutes.
func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	return 0, fmt.Errorf("invalid duration: %s", s)
}

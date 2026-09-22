// Package main — investigation orchestrator.
//
// investigation.go contains the entry point invoked when any trigger
// (CRD watcher, autonomous detector, SLO watcher, Slack, manual API call,
// chaos runner) decides an incident needs the full agent pipeline. The
// orchestrator runs the pipeline, applies the safety policy, executes the
// approved fix (with Kaniko + sandbox + PR), records the outcome, and
// starts the post-fix watcher.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/chatops"
	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/feedback"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/metrics"
	"github.com/karimzakzouk/sentinel/internal/safety"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// handleInvestigation runs the full agent pipeline + execution + feedback
// loop for one anomaly cluster. It is invoked in its own goroutine by
// every trigger (CRD watcher, detector, SLO, chaos, Slack, manual API).
// crName + crNamespace identify the SentinelIncident CR (empty/"auto-
// detected"/"manual"/"slack"/"alertmanager" for non-CR triggers — see
// isRealCR).
//
// The orchestrator never panics: a deferred recover logs the panic and
// returns, so a buggy agent cannot kill the Sentinel process.
func (w *wiring) handleInvestigation(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
	w.invWG.Add(1)
	defer w.invWG.Done()

	select {
	case w.invSem <- struct{}{}:
		defer func() { <-w.invSem }()
	case <-ctx.Done():
		return
	}

	invStart := time.Now()
	finalStatus := "failed"
	metrics.RecordInvestigationStart()
	defer func() {
		metrics.RecordInvestigationComplete(finalStatus, time.Since(invStart))
	}()

	defer func() {
		if r := recover(); r != nil {
			logging.Error("investigation panic recovered",
				"panic", r,
				"cr_name", crName,
				"cr_namespace", crNamespace,
				"service", cluster.Service,
			)
		}
	}()

	logging.Info("investigation started",
		"cr_name", crName,
		"cr_namespace", crNamespace,
		"service", cluster.Service,
		"namespace", cluster.Namespace,
	)

	incident := schemas.NewIncident(cluster)
	incident.Status = schemas.StatusInvestigating
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.Error("save incident failed", "error", err, "service", cluster.Service)
		return
	}
	w.broadcaster.BroadcastEvent("incident", map[string]any{
		"type":        "started",
		"incident_id": incident.IncidentID.String(),
		"service":     cluster.Service,
		"namespace":   cluster.Namespace,
	})
	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "detection",
		"status":      "complete",
		"message":     fmt.Sprintf("Error detected in %s/%s: %s", cluster.Service, cluster.Namespace, cluster.Summary),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	invCtx := logging.WithIncidentID(ctx, incident.IncidentID.String())
	_ = w.store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_started", nil,
		map[string]any{"cluster_summary": cluster.Summary})

	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "investigation",
		"status":      "running",
		"message":     "Investigation started — fetching pod logs + cloning repo",
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	// Run the pipeline WITHOUT post-fix artifacts — those run in parallel
	// with the Kaniko build in executeCodeFix for a 30-90s speedup.
	w.pipeline.SetSkipPostFix(true)

	invTimeout := w.Config().InvestigationTimeout
	investigationCtx, cancel := context.WithTimeout(ctx, time.Duration(invTimeout)*time.Second)
	defer cancel()

	if err := w.pipeline.Run(investigationCtx, &incident.State); err != nil {
		logging.ErrorCtx(invCtx, "pipeline error", "error", err)
		incident.Status = schemas.StatusFailed
		s := err.Error()
		incident.State.Error = &s
		if err := w.store.UpsertIncident(ctx, &incident); err != nil {
			logging.ErrorCtx(invCtx, "save incident (pipeline-error) failed", "error", err)
		}
		if isRealCR(crName) {
			if err := w.k8sClient.PatchCRStatus(ctx, crName, crNamespace, "failed"); err != nil {
				logging.ErrorCtx(invCtx, "patch CR status (failed) failed", "error", err)
			}
		}
		return
	}
	incident.Status = incident.State.Status
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "save incident (post-pipeline) failed", "error", err)
	}

	invMsg := "Investigation complete"
	if incident.State.RootCause != nil {
		invMsg = fmt.Sprintf("Root cause: %s (confidence: %.0f%%)", incident.State.RootCause.Statement, incident.State.RootCause.Confidence*100)
	}
	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "investigation",
		"status":      "complete",
		"message":     invMsg,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
	_ = w.store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_completed", nil,
		map[string]any{"status": string(incident.Status)})

	if incident.State.FixProposal == nil {
		logging.InfoCtx(invCtx, "no fix proposed", "status", incident.Status)
		rootCause := ""
		if incident.State.RootCause != nil {
			rootCause = incident.State.RootCause.Statement
		}
		if isRealCR(crName) {
			if err := w.k8sClient.PatchCRStatusFull(ctx, crName, crNamespace, string(incident.Status),
				incident.IncidentID.String(), rootCause, 0, ""); err != nil {
				logging.ErrorCtx(invCtx, "patch CR status (no-fix) failed", "error", err)
			}
		}
		return
	}

	// === SAFETY PHASE ===
	fp := incident.State.FixProposal
	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "safety",
		"status":      "running",
		"message":     fmt.Sprintf("Evaluating safety policy for %s fix...", fp.FixType),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	decision := w.policy.Evaluate(safety.Input{
		FixType:            fp.FixType,
		TargetService:      fp.TargetService,
		TargetNamespace:    fp.TargetNamespace,
		Confidence:         fp.ProposedConfidence,
		HasSchemaMigration: incident.State.RootCause.HasSchemaMigration,
	})

	switch decision.Verdict {
	case schemas.VerdictAllow:
		incident.Status = schemas.StatusApproved
	case schemas.VerdictRequireHumanApproval:
		incident.Status = schemas.StatusGovernanceReview
	default:
		incident.Status = schemas.StatusEscalated
	}
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "save incident (post-safety) failed", "error", err)
	}
	_ = w.store.AppendEvent(ctx, incident.IncidentID, "safety", "governance_decision", nil,
		map[string]any{"verdict": string(decision.Verdict)})

	if decision.Verdict != schemas.VerdictAllow {
		if decision.Verdict == schemas.VerdictRequireHumanApproval {
			logging.WarnCtx(invCtx, "fix requires human approval",
				"verdict", decision.Verdict,
				"reason", decision.HumanReviewReason,
				"hint", "use 'sentinelctl approve <incident-id>' to override")
		} else {
			logging.WarnCtx(invCtx, "denied by safety", "verdict", decision.Verdict)
		}
		if isRealCR(crName) {
			if err := w.k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status)); err != nil {
				logging.ErrorCtx(invCtx, "patch CR status (denied) failed", "error", err)
			}
		}
		return
	}

	// === EXECUTION PHASE ===
	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "execution",
		"status":      "running",
		"message":     "Approved — preparing fix branch + opening PR",
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	if !w.Config().CanWriteTo(fp.TargetNamespace) {
		logging.WarnCtx(invCtx, "execution denied — namespace not in allowlist", "namespace", fp.TargetNamespace)
		incident.Status = schemas.StatusEscalated
		if err := w.store.UpsertIncident(ctx, &incident); err != nil {
			logging.ErrorCtx(invCtx, "save incident (rbac-deny) failed", "error", err)
		}
		if isRealCR(crName) {
			if err := w.k8sClient.PatchCRStatus(ctx, crName, crNamespace, "escalated"); err != nil {
				logging.ErrorCtx(invCtx, "patch CR status (escalated) failed", "error", err)
			}
		}
		return
	}

	incident.Status = schemas.StatusExecuting
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "save incident (executing) failed", "error", err)
	}

	if err := w.executeFix(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "execution error", "error", err)
		incident.Status = schemas.StatusFailed
		s := err.Error()
		incident.State.Error = &s
	}
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "save incident (post-execution) failed", "error", err)
	}

	// === FEEDBACK PHASE ===
	success := incident.Status == schemas.StatusVerified
	alpha, beta := recordBetaOutcome(ctx, w.store, incident, success)
	incident.BetaAlpha = alpha
	incident.BetaBeta = beta

	if incident.State.RootCause != nil && incident.State.FixProposal != nil {
		vectorText := incident.Cluster.Summary + " " + incident.State.RootCause.Statement + " " + incident.State.FixProposal.Description
		embedding, embErr := w.vectorStore.GenerateEmbedding(ctx, vectorText)
		if embErr != nil {
			logging.ErrorCtx(invCtx, "embedding computation failed", "error", embErr)
		} else {
			if err := w.store.StoreIncidentVector(ctx, &incident, success, embedding); err != nil {
				logging.ErrorCtx(invCtx, "store incident vector failed", "error", err)
			}
			if err := w.vectorStore.Add(ctx, vectorText, &feedback.VectorMetadata{
				IncidentID: incident.IncidentID.String(),
				Service:    incident.Cluster.Service,
				RootCause:  incident.State.RootCause.Statement,
				FixType:    string(incident.State.FixProposal.FixType),
				FixPattern: incident.State.FixProposal.Description,
				Outcome:    map[bool]string{true: "success", false: "failure"}[success],
			}); err != nil {
				logging.ErrorCtx(invCtx, "vector store add failed", "error", err)
			}
		}
	}

	incident.Status = schemas.StatusResolved
	if err := w.store.UpsertIncident(ctx, &incident); err != nil {
		logging.ErrorCtx(invCtx, "save incident (resolved) failed", "error", err)
	}

	prURL := ""
	if incident.State.ExecutionResult != nil && incident.State.ExecutionResult.PRURL != nil {
		prURL = *incident.State.ExecutionResult.PRURL
	}
	w.broadcaster.BroadcastEvent("pipeline", map[string]any{
		"incident_id": incident.IncidentID.String(),
		"phase":       "resolved",
		"status":      "complete",
		"message":     fmt.Sprintf("Incident resolved — PR opened: %s", prURL),
		"pr_url":      prURL,
		"success":     success,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	w.tracker.MarkResolved(incident.Cluster.Service, incident.Cluster.Namespace, success)
	w.alertSender.SendAlert(incident.Cluster.Service, incident.Cluster.Namespace, incident.Cluster.Summary, prURL)

	if success && prURL != "" {
		fingerprint := feedback.ExtractErrorFingerprint(extractPodLogsFromIncident(&incident))
		fixType := "code_fix"
		if incident.State.FixProposal != nil {
			fixType = string(incident.State.FixProposal.FixType)
		}
		w.outcomeWatcher.WatchWithDetails(
			ctx,
			incident.Cluster.Service,
			incident.Cluster.Namespace,
			incident.IncidentID.String(),
			true,
			fixType,
			fingerprint,
		)
	}

	logging.InfoCtx(invCtx, "investigation resolved",
		"status", incident.Status,
		"success", success,
		"alpha", alpha,
		"beta", beta,
		"pr_url", prURL,
	)
	w.broadcaster.BroadcastEvent("incident", map[string]any{
		"type":        "resolved",
		"incident_id": incident.IncidentID.String(),
		"service":     cluster.Service,
		"status":      string(incident.Status),
		"pr_url":      prURL,
		"success":     success,
	})
	finalStatus = string(incident.Status)

	rootCause := ""
	confidence := 0.0
	if incident.State.RootCause != nil {
		rootCause = incident.State.RootCause.Statement
		confidence = incident.State.RootCause.Confidence
	}
	if isRealCR(crName) {
		if err := w.k8sClient.PatchCRStatusFull(ctx, crName, crNamespace, string(incident.Status),
			incident.IncidentID.String(), rootCause, confidence, prURL); err != nil {
			logging.ErrorCtx(invCtx, "patch CR status (resolved) failed", "error", err)
		}
	}
}

// executeFix dispatches the approved fix to the appropriate executor. For
// CODE_FIX / ROLL_FORWARD this is the 4-phase flow (branch → Kaniko →
// sandbox → PR) implemented in executeCodeFix; for the K8s action fix
// types it delegates directly to the K8s executor.
func (w *wiring) executeFix(ctx context.Context, incident *schemas.Incident) error {
	fp := incident.State.FixProposal

	switch fp.FixType {
	case schemas.FixCodeFix, schemas.FixRollForward:
		return w.executeCodeFix(ctx, incident)
	case schemas.FixRestart:
		return w.k8sExec.Restart(ctx, fp)
	case schemas.FixScale:
		return w.k8sExec.Scale(ctx, fp, w.Config().ScaleFixDefaultReplicas)
	case schemas.FixRollback:
		return w.k8sExec.Rollback(ctx, fp)
	default:
		return fmt.Errorf("unknown fix type: %s", fp.FixType)
	}
}

// executeCodeFix runs the 4-phase code-fix flow:
//  1. Prepare branch (push fix files to a new Git branch).
//  2. Build canary image with Kaniko (in parallel with post-fix LLM
//     artifacts on the first attempt).
//  3. Run sandbox verification (5 levels). On Level 1-3 failure, feed
//     level-specific feedback to the fix proposer and retry (up to
//     SandboxMaxRetries). On infrastructure failure, do not retry.
//  4. Open PR with sandbox result + track for auto-merge.
func (w *wiring) executeCodeFix(ctx context.Context, incident *schemas.Incident) error {
	fp := incident.State.FixProposal

	logging.Info("execution Phase 1: preparing fix branch", "fix_id", fp.FixID)
	branchInfo := w.ghExec.PrepareBranch(ctx, incident)
	if !branchInfo.Success {
		return fmt.Errorf("branch_preparation_failed: %s", branchInfo.Error)
	}
	if branchInfo.FilesChanged == 0 {
		return fmt.Errorf("no_files_in_fix_proposal (LLM response may have been truncated)")
	}

	// Decide whether to run the sandbox at all. Skipped when:
	//   - sandbox is globally disabled (fast mode)
	//   - the target namespace is non-production and SkipSandboxNonProd is set
	//   - no Docker Hub credentials are configured (can't pull the base image)
	cfg := w.Config()
	shouldRunSandbox := cfg.SandboxEnabled
	skipNonProd := cfg.SkipSandboxNonProd
	isProd := cfg.IsProductionNamespace(fp.TargetNamespace)
	hasDockerCreds := cfg.DockerHubToken != ""

	sandboxVerified := false
	sandboxSummary := "skipped (sandbox disabled)"
	sandboxLevels := ""
	sandboxRan := false

	if !shouldRunSandbox {
		sandboxSummary = "skipped (sandbox disabled — fast mode)"
		logging.Info("sandbox disabled (SENTINEL_SANDBOX_ENABLED=false) — opening PR without verification",
			"fix_id", fp.FixID)
	} else if skipNonProd && !isProd {
		sandboxSummary = "skipped (non-production namespace — fast mode)"
		logging.Info("skipping sandbox for non-production namespace (fast mode)",
			"fix_id", fp.FixID, "namespace", fp.TargetNamespace)
		shouldRunSandbox = false
	} else if !hasDockerCreds {
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
				// Only sandbox Level 1-5 failures trigger retries (not Kaniko
				// failures — the LLM cannot fix a permissions error).
				logging.Info("sandbox retry: feeding level-specific failure back to fix proposer",
					"fix_id", fp.FixID, "attempt", sandboxAttempt+1, "max", maxRetries+1,
					"level", detectSandboxFailureLevel(sandboxSummary))

				fb := levelSpecificFeedback(sandboxSummary, sandboxSummary)
				incident.State.SandboxFeedback = &fb

				// Fetch the CURRENT file content from the branch so the fix
				// proposer generates SEARCH/REPLACE blocks that match the
				// current branch state, not the original file content.
				if stateFetchedCode := fetchCurrentBranchFiles(ctx, w.ghExec, branchInfo, incident); len(stateFetchedCode) > 0 {
					incident.State.FetchedCode = stateFetchedCode
					logging.Info("fetched current file state from branch for retry",
						"fix_id", fp.FixID, "files", len(stateFetchedCode))
				}

				if err := w.pipeline.RetryFixProposal(ctx, &incident.State); err != nil {
					logging.Error("fix proposer retry failed", "error", err)
					break
				}
				fp = incident.State.FixProposal
				if fp == nil {
					break
				}
				logging.Info("re-preparing fix branch with corrected code", "fix_id", fp.FixID, "attempt", sandboxAttempt+1)
				branchInfo = w.ghExec.PrepareBranch(ctx, incident)
				if !branchInfo.Success {
					logging.Error("branch re-preparation failed", "error", branchInfo.Error)
					break
				}
			}

			// Phase 2: Build canary image (+ post-fix artifacts in parallel on first attempt).
			logging.Info("execution Phase 2: building canary image with Kaniko",
				"fix_id", fp.FixID, "attempt", sandboxAttempt+1, "max", maxRetries+1)
			prodImage, err := w.k8sClient.GetProdImage(ctx, fp.TargetService, fp.TargetNamespace)
			if err != nil || prodImage == "" {
				sandboxSummary = "skipped (could not find prod image)"
				break
			}

			var kanikoErr error
			var canaryImage string
			if sandboxAttempt == 0 {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					canaryImage, kanikoErr = w.kaniko.BuildForIncident(ctx, incident, branchInfo, prodImage)
				}()
				go func() {
					defer wg.Done()
					if err := w.pipeline.RunPostFixArtifacts(ctx, &incident.State); err != nil {
						logging.Error("post-fix artifacts failed (non-blocking)", "err", err)
					}
				}()
				wg.Wait()
			} else {
				canaryImage, kanikoErr = w.kaniko.BuildForIncident(ctx, incident, branchInfo, prodImage)
			}

			if kanikoErr != nil {
				sandboxSummary = fmt.Sprintf("kaniko_error: %v", kanikoErr)
				logging.Error("kaniko build failed — not retrying (infrastructure error)",
					"fix_id", fp.FixID, "error", kanikoErr, "attempt", sandboxAttempt+1)
				break
			}

			logging.Info("canary image built", "image", canaryImage)
			fp.CanaryImage = &canaryImage

			// Phase 3: Sandbox verification.
			logging.Info("execution Phase 3: running sandbox verification", "fix_id", fp.FixID, "attempt", sandboxAttempt+1)
			metrics.RecordSandboxRetry(sandboxAttempt + 1)
			sandboxStart := time.Now()
			sb := w.sandbox.Run(ctx, incident)
			sandboxVerified = sb.Verified
			sandboxSummary = sb.Summary
			sandboxLevels = sb.LevelsVerified
			metrics.RecordSandboxVerification(sandboxVerified, time.Since(sandboxStart))
			if !sandboxVerified {
				level := detectSandboxFailureLevel(sb.Summary)
				metrics.RecordSandboxLevelFailure(level)
			}
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
				break
			}

			// Infrastructure errors (RBAC, image pull, namespace creation)
			// cannot be fixed by the LLM — stop the retry loop.
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

	cfg = w.Config()
	if sandboxRan && !sandboxVerified && cfg.SandboxRequired {
		logging.Error("sandbox verification failed and sandbox is required — not opening PR",
			"fix_id", fp.FixID, "summary", sandboxSummary)
		return fmt.Errorf("sandbox_required_and_failed: %s", sandboxSummary)
	}

	// Phase 4: Open PR.
	logging.Info("execution Phase 4: opening PR", "fix_id", fp.FixID)
	canaryImg := ""
	if fp.CanaryImage != nil {
		canaryImg = *fp.CanaryImage
	}
	incident.UpdatedAt = time.Now().UTC()
	fullPRBody := agents.BuildPRBody(incident, sandboxVerified, sandboxSummary, canaryImg)
	fp.PRBody = &fullPRBody

	prResult := w.ghExec.OpenPR(ctx, incident, branchInfo, sandboxVerified, sandboxLevels, sandboxSummary)
	if !prResult.Success {
		return fmt.Errorf("pr_open_failed: %s", prResult.Error)
	}

	incident.Status = schemas.StatusVerified
	if incident.State.ExecutionResult == nil {
		incident.State.ExecutionResult = &schemas.ExecutionResult{
			FixID:     fp.FixID,
			FixType:   fp.FixType,
			Success:   true,
			StartedAt: time.Now().UTC(),
		}
	}
	incident.State.ExecutionResult.Success = true
	incident.State.ExecutionResult.SandboxLevels = sandboxLevels
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
	w.broadcaster.BroadcastEvent("pr_opened", map[string]any{
		"pr_url":           prResult.PRURL,
		"pr_number":        prResult.PRNumber,
		"sandbox_verified": sandboxVerified,
	})

	repoName := ""
	if fp.Repo != nil {
		repoName = *fp.Repo
	}
	if repoName != "" && prResult.PRNumber > 0 {
		if err := w.store.UpdateIncidentPR(ctx, incident.IncidentID, repoName, prResult.PRNumber, prResult.PRBranch, prResult.PRURL); err != nil {
			logging.Error("failed to persist PR association for chatops", "error", err)
		}
	}

	// Post the initial [SENTINELSTATUS] comment so reviewers see the state
	// and available commands immediately when they open the PR. Must happen
	// BEFORE EvaluateNow so the merger's "✅ Merged" callback doesn't get
	// overwritten with "⏳ Queued".
	if w.chatopsStatus != nil && repoName != "" {
		chatopsInc := &chatops.Incident{
			ID:               incident.IncidentID.String(),
			Service:          incident.Cluster.Service,
			Namespace:        incident.Cluster.Namespace,
			Status:           string(incident.Status),
			PRURL:            prResult.PRURL,
			PRNumber:         prResult.PRNumber,
			PRRepo:           repoName,
			Confidence:       0,
			AutoMergeEnabled: w.Config().AutoMergeEnabled,
		}
		if incident.State.RootCause != nil {
			chatopsInc.RootCause = incident.State.RootCause.Statement
			chatopsInc.Confidence = incident.State.RootCause.Confidence
		}
		extra := "Available commands: `/sentinel help` for the full list."
		if !sandboxVerified {
			extra += "\n\n⚠️ Sandbox verification did not pass — review carefully before merging."
		}
		if err := w.chatopsStatus.Update(ctx, repoName, prResult.PRNumber, chatopsInc, extra); err != nil {
			logging.Error("failed to post initial status comment", "error", err)
		}
	}

	// Track for auto-merge + force immediate evaluation.
	autoMergeEnabled := w.Config().AutoMergeEnabled
	if w.autoMergeMerger != nil && repoName != "" && autoMergeEnabled {
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
		cfgSnapshot := w.Config()
		isHighRisk := false
		for _, svc := range cfgSnapshot.HighRiskServices {
			if svc == incident.Cluster.Service {
				isHighRisk = true
				break
			}
		}
		policyVerdict := "allow"
		if incident.State.GovernanceDecision != nil {
			policyVerdict = string(incident.State.GovernanceDecision.Verdict)
		}
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
		w.autoMergeMerger.Track(&automerge.TrackedPR{
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
				SandboxLevels:       sandboxLevels,
				PolicyVerdict:       policyVerdict,
				AutoMergeAnnotation: autoMergeAnnotation,
				SandboxOverride:     incident.State.ExecutionResult != nil && incident.State.ExecutionResult.SandboxOverride,
				CIOverride:          incident.State.ExecutionResult != nil && incident.State.ExecutionResult.CIOverride,
			},
		})
		logging.Info("PR tracked for auto-merge",
			"pr_number", prResult.PRNumber,
			"repo", repoName,
			"confidence", confidence,
			"sandbox_verified", sandboxVerified,
		)
		w.autoMergeMerger.EvaluateNow(ctx, repoName, prResult.PRNumber)
	}

	return nil
}

// detectSandboxFailureLevel parses the sandbox summary to determine which
// verification level failed. Used for per-level metrics + level-specific
// LLM feedback.
//
//	1 = canary pod not Ready / not sustained (startup crash not fixed)
//	2 = health endpoints failing (app starts but serves 4xx/5xx)
//	3 = original error still present in canary logs (PRIMARY ORACLE)
//	4 = regression test failed
//	5 = reproducer script crashed (strongest signal)
//	0 = infrastructure error (RBAC, image pull, namespace creation)
func detectSandboxFailureLevel(summary string) int {
	switch {
	case strings.Contains(summary, "canary_pods_not_ready"):
		return 1
	case strings.Contains(summary, "endpoints_unreachable"), strings.Contains(summary, "health_endpoints_failing"):
		return 2
	case strings.Contains(summary, "original_error_still_present"):
		return 3
	case strings.Contains(summary, "regression_test_failed"):
		return 4
	case strings.Contains(summary, "reproducer_failed"):
		return 5
	case strings.Contains(summary, "deploy_failed"), strings.Contains(summary, "create_namespace"), strings.Contains(summary, "kaniko_error"):
		return 0
	}
	return 0
}

// levelSpecificFeedback returns a targeted feedback message for the LLM
// based on which sandbox level failed. Critical for L4/L5 — without it
// the LLM just sees "sandbox failed" without knowing its fix passed
// Levels 1-3 but failed the regression test.
func levelSpecificFeedback(summary, fullSummary string) string {
	level := detectSandboxFailureLevel(summary)
	switch level {
	case 1:
		return fmt.Sprintf("Sandbox Level 1 FAILED: the canary pod did not become Ready (or did not stay Ready for the sustained window).\n\nThis means the startup crash was NOT fixed — the app still crashes on boot.\n\nFull failure detail:\n%s\n\nPlease generate a corrected fix that addresses the startup crash.", fullSummary)
	case 2:
		return fmt.Sprintf("Sandbox Level 2 FAILED: the canary pod is Ready, but health endpoints (/, /healthz, /readyz) returned 4xx/5xx.\n\nThis means the app starts but is broken at the HTTP layer.\n\nFull failure detail:\n%s\n\nPlease generate a corrected fix that makes the health endpoints return 2xx.", fullSummary)
	case 3:
		return fmt.Sprintf("Sandbox Level 3 FAILED: the original error is still present in the canary pod's logs.\n\nThis means the fix did NOT eliminate the original error.\n\nFull failure detail:\n%s\n\nPlease generate a corrected fix that actually eliminates the original error.", fullSummary)
	case 4:
		return fmt.Sprintf("Sandbox Level 4 FAILED: the regression test did not pass.\n\nIMPORTANT: Your fix passed Levels 1-3 (pod is Ready, health endpoints OK, original error gone from logs). But the LLM-generated regression test — which reproduces the original bug — still FAILS against your fixed code. This means your fix did not actually fix the underlying bug; it only masked the symptom.\n\nFull failure detail:\n%s\n\nPlease generate a corrected fix that makes the regression test PASS.", fullSummary)
	case 5:
		return fmt.Sprintf("Sandbox Level 5 FAILED: the reproducer script crashed (exit code != 0).\n\nIMPORTANT: Your fix passed Levels 1-4. But the LLM-generated reproducer script — which exercises the buggy code path — still crashes against your fixed code. This is the strongest signal that the fix is incomplete.\n\nFull failure detail:\n%s\n\nPlease generate a corrected fix that makes the reproducer script exit 0.", fullSummary)
	default:
		return fmt.Sprintf("Sandbox verification failed (infrastructure or unknown error — the LLM may not be able to fix this):\n%s\n\nPlease generate a corrected fix if you can; otherwise the PR will be opened with needs-review.", fullSummary)
	}
}

// recordBetaOutcome updates the Beta prior for the fix pattern and returns
// the new (alpha, beta). Returns (1, 1) when no fix proposal is present.
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

// extractPodLogsFromIncident pulls the pod logs from the incident's
// anomaly explanations. Used to extract the error fingerprint for the
// 24h re-occurrence watcher.
func extractPodLogsFromIncident(incident *schemas.Incident) string {
	var logs string
	for _, anomaly := range incident.Cluster.Anomalies {
		logs += anomaly.Explanation + "\n"
	}
	return logs
}

// fetchCurrentBranchFiles fetches the current file content from the fix
// branch (which has the previous fix applied) so the LLM generates
// SEARCH/REPLACE blocks against the CURRENT state, not the original code.
// Without this, the retry loop fails because the LLM's search text
// references the original code which no longer exists.
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

// isRealCR returns true if crName refers to an actual SentinelIncident CR
// (created via kubectl apply or the CRD watcher). Auto-detected incidents
// pass crName="auto-detected"; manual API triggers pass "manual"; Slack
// triggers pass "slack"; Alertmanager triggers pass "alertmanager"; SLO
// + chaos triggers pass "slo" / "chaos". None of these correspond to a
// real CR object, so patching their status generates a 404 + spurious
// error log.
func isRealCR(crName string) bool {
	switch crName {
	case "", "auto-detected", "manual", "slack", "alertmanager", "slo", "chaos":
		return false
	}
	return true
}

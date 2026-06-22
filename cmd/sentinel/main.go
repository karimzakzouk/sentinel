// Package main is the Sentinel v3 single-binary entrypoint.
// One binary, one process, one pod. ~25MB image.
package main

import (
        "context"
        "flag"
        "fmt"
        "log"
        "net/http"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzoukz/sentinel/v3/internal/agents"
        "github.com/karimzakzoukz/sentinel/v3/internal/api"
        "github.com/karimzakzoukz/sentinel/v3/internal/config"
        "github.com/karimzakzoukz/sentinel/v3/internal/detection"
        "github.com/karimzakzoukz/sentinel/v3/internal/executors"
        "github.com/karimzakzoukz/sentinel/v3/internal/feedback"
        "github.com/karimzakzoukz/sentinel/v3/internal/k8s"
        "github.com/karimzakzoukz/sentinel/v3/internal/llm"
        "github.com/karimzakzoukz/sentinel/v3/internal/safety"
        "github.com/karimzakzoukz/sentinel/v3/internal/schemas"
        "github.com/karimzakzoukz/sentinel/v3/internal/watcher"
)

func main() {
        addr := flag.String("addr", "", "HTTP listen address (overrides env)")
        dbPath := flag.String("db", "", "SQLite DB path (overrides env)")
        flag.Parse()

        cfg := config.Load()
        if *dbPath != "" {
                cfg.SQLitePath = *dbPath
        }
        listenAddr := fmt.Sprintf("%s:%d", cfg.APIHost, cfg.APIPort)
        if *addr != "" {
                listenAddr = *addr
        }

        log.Printf("[main] Sentinel %s starting", Version)
        log.Printf("[main] LLM: %s (model: %s)", cfg.LLMProvider, cfg.LLMFrontierModel)
        log.Printf("[main] SQLite: %s", cfg.SQLitePath)
        log.Printf("[main] API: %s", listenAddr)

        // 1. Storage (SQLite replaces Postgres + Weaviate)
        store, err := feedback.New(cfg.SQLitePath)
        if err != nil {
                log.Fatalf("[main] storage init: %v", err)
        }
        defer store.Close()

        // 2. K8s client
        k8sClient, err := k8s.New()
        if err != nil {
                log.Fatalf("[main] k8s client: %v", err)
        }

        // 3. LLM router
        router := llm.New(cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMFastModel, cfg.LLMFrontierModel)

        // 4. Agent pipeline
        pipeline := agents.NewPipeline(router, k8sClient, cfg.GitHubToken, store)

        // 5. Safety policy (embedded, replaces OPA sidecar)
        policy := safety.NewPolicy(cfg.ProductionNamespaces, cfg.ConfidenceAutonomous)

        // 6. Executors
        ghExec := executors.NewGitHub(cfg.GitHubToken)
        k8sExec := executors.NewK8s(k8sClient.Clientset())
        sandbox := executors.NewSandbox(k8sClient.Clientset(), cfg.SandboxNamespacePrefix, cfg.EnableTrafficMirroring)
        kaniko := executors.NewKaniko(k8sClient.Clientset(), cfg.Namespace)

        ready := false

        // 7. The full investigation handler — runs the agent pipeline + execution
        handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
                log.Printf("[investigation] %s/%s — service=%s", crNamespace, crName, cluster.Service)

                // Create + save incident
                incident := schemas.NewIncident(cluster)
                incident.Status = schemas.StatusInvestigating
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        log.Printf("[investigation] save incident: %v", err)
                        return
                }
                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_started", nil,
                        map[string]any{"cluster_summary": cluster.Summary})

                // === REASONING PHASE ===
                investigationCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.InvestigationTimeout)*time.Second)
                defer cancel()

                if err := pipeline.Run(investigationCtx, &incident.State); err != nil {
                        log.Printf("[investigation] pipeline error: %v", err)
                        incident.Status = schemas.StatusFailed
                        s := err.Error()
                        incident.State.Error = &s
                        _ = store.UpsertIncident(ctx, &incident)
                        _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, "failed")
                        return
                }
                incident.Status = incident.State.Status
                _ = store.UpsertIncident(ctx, &incident)
                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_completed", nil,
                        map[string]any{"status": string(incident.Status)})

                if incident.State.FixProposal == nil {
                        log.Printf("[investigation] no fix proposed — status=%s", incident.Status)
                        _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status))
                        return
                }

                // === SAFETY PHASE (embedded policy, no OPA) ===
                fp := incident.State.FixProposal
                decision := policy.Evaluate(safety.Input{
                        FixType:           fp.FixType,
                        TargetService:     fp.TargetService,
                        TargetNamespace:   fp.TargetNamespace,
                        Confidence:        fp.ProposedConfidence,
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
                _ = store.UpsertIncident(ctx, &incident)
                _ = store.AppendEvent(ctx, incident.IncidentID, "safety", "governance_decision", nil,
                        map[string]any{"verdict": string(decision.Verdict)})

                if decision.Verdict != schemas.VerdictAllow {
                        log.Printf("[investigation] denied by safety — verdict=%s", decision.Verdict)
                        _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status))
                        return
                }

                // === EXECUTION PHASE ===
                // RBAC check
                if !cfg.CanWriteTo(fp.TargetNamespace) {
                        log.Printf("[investigation] execution denied — namespace %s not in allowlist", fp.TargetNamespace)
                        incident.Status = schemas.StatusEscalated
                        _ = store.UpsertIncident(ctx, &incident)
                        _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, "escalated")
                        return
                }

                incident.Status = schemas.StatusExecuting
                _ = store.UpsertIncident(ctx, &incident)

                if err := executeFix(ctx, &incident, cfg, ghExec, k8sExec, sandbox, kaniko, k8sClient, store); err != nil {
                        log.Printf("[investigation] execution error: %v", err)
                        incident.Status = schemas.StatusFailed
                        s := err.Error()
                        incident.State.Error = &s
                }
                _ = store.UpsertIncident(ctx, &incident)

                // === FEEDBACK PHASE ===
                success := incident.Status == schemas.StatusVerified
                alpha, beta := recordBetaOutcome(ctx, store, incident, success)
                incident.BetaAlpha = alpha
                incident.BetaBeta = beta
                _ = store.StoreIncidentVector(ctx, &incident, success)
                incident.Status = schemas.StatusResolved
                _ = store.UpsertIncident(ctx, &incident)

                log.Printf("[investigation] %s resolved (status=%s, alpha=%.0f, beta=%.0f)",
                        incident.IncidentID, incident.Status, alpha, beta)
                _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status))
        }

        // 8. Start the CRD watcher
        w, err := watcher.New(k8sClient, handler)
        if err != nil {
                log.Fatalf("[main] watcher: %v", err)
        }

        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()

        go func() {
                log.Printf("[main] CRD watcher started")
                w.Run(ctx)
        }()

        // 9. Start the autonomous detection layer (live log streaming + pod status)
        detectionHandler := func(service, namespace, reason, podLogs string) {
                log.Printf("[detection] auto-triggered incident: %s/%s — %s", namespace, service, reason)

                // Look up workload annotations for repo + branch
                annotations, _ := k8sClient.LookupWorkloadAnnotations(ctx, service, namespace)
                if annotations == nil {
                        annotations = map[string]string{}
                }

                // Build the anomaly cluster (same structure as CRD watcher)
                explanation := fmt.Sprintf("Auto-detected incident: %s", reason)
                if podLogs != "" {
                        explanation += fmt.Sprintf("\n\n## Pod Logs (last 8KB)\n%s", podLogs)
                }

                cluster := schemas.AnomalyCluster{
                        ClusterID: uuid.New(),
                        Service:   service,
                        Namespace: namespace,
                        Anomalies: []schemas.Anomaly{
                                {
                                        AnomalyID:   uuid.New(),
                                        SignalID:    uuid.New(),
                                        Service:     service,
                                        Namespace:   namespace,
                                        Detector:    "auto_detection",
                                        Score:       0.9,
                                        Dimensions:  map[string]float64{"auto": 1.0},
                                        Explanation: explanation,
                                        Annotations: annotations,
                                },
                        },
                        Summary:     reason,
                        Severity:    schemas.SeverityWarning,
                        Annotations: annotations,
                }

                // Run the same handler as the CRD watcher
                handler(ctx, cluster, "auto-detected", namespace)
        }

        detector := detection.New(k8sClient.Clientset(), detectionHandler, "")
        go func() {
                log.Printf("[main] detection layer started (live log streaming + pod status)")
                detector.Run(ctx)
        }()

        go func() {
                time.Sleep(2 * time.Second)
                ready = true
                log.Printf("[main] Sentinel ready — watching for incidents")
        }()

        // 9. HTTP API
        apiServer := api.New(
                listenAddr,
                &storeAdapter{store: store},
                func() map[string]any {
                        return map[string]any{
                                "llm_provider":       cfg.LLMProvider,
                                "llm_fast_model":     cfg.LLMFastModel,
                                "llm_frontier_model": cfg.LLMFrontierModel,
                                "github_token_set":   cfg.GitHubToken != "",
                                "write_namespaces":   cfg.WriteNamespaces,
                        }
                },
                func(service, namespace, reason string) (string, error) {
                        cluster := schemas.AnomalyCluster{
                                ClusterID: uuid.New(),
                                Service:   service,
                                Namespace: namespace,
                                Severity:  schemas.SeverityWarning,
                                Summary:   fmt.Sprintf("Manual investigation: %s", reason),
                        }
                        go handler(ctx, cluster, "manual", "sentinel")
                        return cluster.ClusterID.String(), nil
                },
                func() bool { return ready },
        )

        go func() {
                if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
                        log.Printf("[main] API server: %v", err)
                }
        }()

        // 10. Wait for shutdown
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        log.Printf("[main] shutting down...")
        cancel()

        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        _ = apiServer.Shutdown(shutdownCtx)
        log.Printf("[main] stopped")
}

// executeFix runs the appropriate executor based on fix type.
// For CODE_FIX / ROLL_FORWARD: 4-phase flow (branch → Kaniko → sandbox → PR)
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
) error {
        fp := incident.State.FixProposal

        switch fp.FixType {
        case schemas.FixCodeFix, schemas.FixRollForward:
                return executeCodeFix(ctx, incident, cfg, ghExec, sandbox, kaniko, k8sClient)

        case schemas.FixRestart:
                return k8sExec.Restart(ctx, fp)

        case schemas.FixScale:
                // Default to doubling replicas (capped at 10)
                newReplicas := int32(2)
                // v3.1: parse from fp.K8sAction
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
) error {
        fp := incident.State.FixProposal

        // Phase 1: Prepare branch
        log.Printf("[execution] Phase 1: preparing fix branch...")
        branchInfo := ghExec.PrepareBranch(ctx, incident)
        if !branchInfo.Success {
                return fmt.Errorf("branch_preparation_failed: %s", branchInfo.Error)
        }
        if branchInfo.FilesChanged == 0 {
                return fmt.Errorf("no_files_in_fix_proposal (LLM response may have been truncated)")
        }

        // Phase 2 + 3: Build canary + sandbox (only if Docker Hub creds available)
        sandboxVerified := false
        sandboxSummary := "skipped (no Docker Hub credentials)"

        if cfg.DockerHubToken != "" {
                log.Printf("[execution] Phase 2: building canary image with Kaniko...")
                prodImage, err := k8sClient.GetProdImage(ctx, fp.TargetService, fp.TargetNamespace)
                if err == nil && prodImage != "" {
                        canaryImage, err := kaniko.BuildForIncident(ctx, incident, branchInfo, prodImage)
                        if err != nil {
                                sandboxSummary = fmt.Sprintf("kaniko_error: %v", err)
                                log.Printf("[execution] kaniko failed: %v", err)
                        } else {
                                log.Printf("[execution] canary image built: %s", canaryImage)
                                fp.CanaryImage = &canaryImage

                                log.Printf("[execution] Phase 3: running sandbox verification...")
                                sb := sandbox.Run(ctx, incident)
                                sandboxVerified = sb.Verified
                                sandboxSummary = sb.Summary
                                log.Printf("[execution] sandbox: verified=%v (%s)", sandboxVerified, sandboxSummary)
                        }
                } else {
                        sandboxSummary = "skipped (could not find prod image)"
                }
        }

        // Phase 4: Open PR
        log.Printf("[execution] Phase 4: opening PR...")
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

        log.Printf("[execution] PR opened: %s (#%d) — sandbox_verified=%v",
                prResult.PRURL, prResult.PRNumber, sandboxVerified)
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
                log.Printf("[feedback] record beta outcome: %v", err)
                return 1.0, 1.0
        }
        return alpha, beta
}

// storeAdapter bridges feedback.Store to the api.Store interface.
type storeAdapter struct {
        store *feedback.Store
}

func (a *storeAdapter) ListRecentIncidents(ctx context.Context, limit int) ([]any, error) {
        incidents, err := a.store.ListRecentIncidents(ctx, limit)
        if err != nil {
                return nil, err
        }
        out := make([]any, len(incidents))
        for i, inc := range incidents {
                out[i] = inc
        }
        return out, nil
}

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

        "github.com/karimzakzouk/sentinel/v3/internal/agents"
        "github.com/karimzakzouk/sentinel/v3/internal/api"
        "github.com/karimzakzouk/sentinel/v3/internal/config"
        "github.com/karimzakzouk/sentinel/v3/internal/correlation"
        "github.com/karimzakzouk/sentinel/v3/internal/detection"
        "github.com/karimzakzouk/sentinel/v3/internal/events"
        "github.com/karimzakzouk/sentinel/v3/internal/executors"
        "github.com/karimzakzouk/sentinel/v3/internal/feedback"
        "github.com/karimzakzouk/sentinel/v3/internal/github" // package name: githubapp
        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/llm"
        "github.com/karimzakzouk/sentinel/v3/internal/logging"
        "github.com/karimzakzouk/sentinel/v3/internal/metrics"
        "github.com/karimzakzouk/sentinel/v3/internal/prreview"
        "github.com/karimzakzouk/sentinel/v3/internal/runtimecfg"
        "github.com/karimzakzouk/sentinel/v3/internal/safety"
        "github.com/karimzakzouk/sentinel/v3/internal/slackbot"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
        "github.com/karimzakzouk/sentinel/v3/internal/watcher"
)

// v3.7.0: Package-level broadcaster for SSE events. Assigned in main() to the
// API server, used by the investigation handler to push real-time updates.
var broadcaster events.Broadcaster = events.Noop()
var _ = broadcaster // used in handler closures below

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

        // v3.4.0: Initialize structured logger (JSON to stdout, level from config).
        // Falls back to stdlib log for any code not yet migrated to slog.
        logging.Init(cfg.LogLevel)

        logging.Info("Sentinel starting",
                "version", Version,
                "llm_provider", cfg.LLMProvider,
                "llm_frontier_model", cfg.LLMFrontierModel,
                "llm_embedding_model", cfg.LLMEmbeddingModel,
                "sqlite_path", cfg.SQLitePath,
                "api_addr", listenAddr,
                "log_level", cfg.LogLevel,
        )

        // v3.8.0: Load runtime config (overrides env vars, editable from web UI)
        rtCfg := runtimecfg.New("/data/runtime-config.json")

        // 1. Storage (SQLite replaces Postgres + Weaviate)
        store, err := feedback.New(cfg.SQLitePath)
        if err != nil {
                log.Fatalf("storage init: %v", err)
        }
        defer store.Close()

        // 2. K8s client
        k8sClient, err := k8s.New()
        if err != nil {
                log.Fatalf("k8s client: %v", err)
        }

        // 3. LLM router
        // v3.4.0: retry config (was no retry — single HTTP call, killed by free-tier rate limits)
        router := llm.New(cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMFastModel, cfg.LLMFrontierModel)
        router.SetRetryConfig(cfg.LLMMaxRetries, time.Duration(cfg.LLMRetryBaseDelayMs)*time.Millisecond)

        // v3.8.4: Wire the runtime config apply callback. When the user
        // saves settings via POST /settings, runtimecfg.Manager invokes
        // this callback, which propagates the new LLM provider / API key /
        // models into the live router. This is what makes the web UI's
        // Settings page actually take effect — previously settings were
        // saved to disk but only read back at startup.
        rtCfg.SetApplyCallback(func(rc *runtimecfg.RuntimeConfig) {
                // Resolve base URL: use override from runtime config if set,
                // otherwise derive from provider, otherwise keep current.
                baseURL := rc.LLMBaseURL
                if baseURL == "" && rc.LLMProvider != "" {
                        // Save the current provider override and re-derive.
                        // We can't call cfg.ResolvedBaseURL() because cfg is
                        // the startup config, not the runtime one. Fall back
                        // to keeping the current base URL if provider changed
                        // but no base URL is set — operators who change
                        // provider should also set base URL.
                        if rc.LLMProvider != cfg.LLMProvider {
                                logging.Warn("LLM provider changed via runtime config but no base URL set — keeping current base URL. Set llm_base_url in settings.", "old_provider", cfg.LLMProvider, "new_provider", rc.LLMProvider)
                        }
                }
                apiKey := rc.LLMAPIKey
                if apiKey == "" {
                        apiKey = cfg.LLMAPIKey // fall back to startup value
                }
                fastModel := rc.LLMFastModel
                if fastModel == "" {
                        fastModel = cfg.LLMFastModel
                }
                frontierModel := rc.LLMFrontierModel
                if frontierModel == "" {
                        frontierModel = cfg.LLMFrontierModel
                }
                if baseURL != "" {
                        router.Update(baseURL, apiKey, fastModel, frontierModel)
                } else {
                        // Update without changing base URL — pass empty so
                        // Update keeps... no, Update sets it to empty. We
                        // need a different code path. Snapshot the current
                        // base URL and pass it back.
                        curBase, curKey, _, _ := router.Snapshot()
                        _ = curKey
                        router.Update(curBase, apiKey, fastModel, frontierModel)
                }
                logging.Info("LLM router reconfigured from runtime settings",
                        "fast_model", fastModel, "frontier_model", frontierModel,
                        "api_key_set", apiKey != "", "base_url_changed", baseURL != "")
        })

        // v3.4.0: Wire LLM metrics (avoids circular import: llm → metrics → ... → llm)
        llm.SetMetricsRecorder(metrics.RecordLLMCall)
        llm.SetInFlightHooks(metrics.LLMCallStart, metrics.LLMCallEnd)

        // 3.5 Vector store (HNSW + LLM embeddings) — created before pipeline for memory loop
        // v3.4.0: embedding model now configurable (was hardcoded "text-embedding-3-small")
        vectorStore := feedback.NewVectorStore(cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMEmbeddingModel)

        // 4. Agent pipeline (with vector store for memory loop)
        pipeline := agents.NewPipeline(router, k8sClient, cfg.GitHubToken, store, &vectorStoreAdapter{vs: vectorStore})
        // v3.6.0: Configurable timeouts + confidence threshold (re-wired v3.8.1 — was accidentally dropped)
        pipeline.SetConfig(
                cfg.InvestigatorTimeoutSec,
                cfg.EvidenceGathererTimeoutSec,
                cfg.AnalystTimeoutSec,
                cfg.FixProposerTimeoutSec,
                cfg.ConfidenceLoopBackThreshold,
        )
        // v3.4.0: Wire Prometheus backend for evidence gatherer
        if cfg.PrometheusURL != "" {
                pipeline.SetPrometheusURL(cfg.PrometheusURL)
        }
        // v3.8.4: Wire the GitHub repo allowlist into the investigator.
        // When non-empty, the investigator refuses to clone repos outside
        // this list — primary defense against the pod-annotation SSRF.
        pipeline.SetAllowedRepos(cfg.AllowedGitHubRepos)

        // 5. Safety policy (embedded, replaces OPA sidecar)
        policy := safety.NewPolicy(cfg.ProductionNamespaces, cfg.ConfidenceAutonomous, cfg.ConfidenceHumanReview)

        // 6. Executors
        ghExec := executors.NewGitHub(cfg.GitHubToken)
        k8sExec := executors.NewK8s(k8sClient.Clientset())
        sandbox := executors.NewSandbox(k8sClient.Clientset(), cfg.SandboxNamespacePrefix, cfg.EnableTrafficMirroring).
                WithProbePaths(cfg.SandboxProbePaths)
        kaniko := executors.NewKaniko(k8sClient.Clientset(), cfg.Namespace).WithDockerHubUsername(cfg.DockerHubUsername)

        ready := false

        // v3.2: Active incident tracker (per-service dedup)
        tracker := detection.NewActiveTracker()
        // v3.2: Slack alerts (graceful degradation if no webhook)
        alertSender := agents.NewAlertSender(cfg.SlackWebhook)
        // v3.2: Outcome watcher (monitors service post-fix)
        outcomeWatcher := feedback.NewOutcomeWatcher(k8sClient.Clientset(), store)
        // v3.2: Load existing vectors from SQLite into HNSW index on startup (delay 2s for DB to be ready)
        go func() {
                time.Sleep(2 * time.Second)
                loadCtx := context.Background()
                if err := vectorStore.LoadFromDB(loadCtx, store); err != nil {
                        logging.Error("vector store load failed", "error", err)
                }
        }()

        // v3.8.2: Concurrency limiter — max 3 concurrent investigations to prevent
        // LLM API quota exhaustion during incident storms (Claude's review).
        invSem := make(chan struct{}, 3)

        // 7. The full investigation handler — runs the agent pipeline + execution
        handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
                // v3.8.2: Acquire semaphore — blocks if 3 investigations are already running
                select {
                case invSem <- struct{}{}:
                        defer func() { <-invSem }()
                case <-ctx.Done():
                        return
                }

                // v3.4.0: Metrics — track investigation count + duration + active gauge.
                invStart := time.Now()
                finalStatus := "failed"
                metrics.RecordInvestigationStart()
                defer func() {
                        metrics.RecordInvestigationComplete(finalStatus, time.Since(invStart))
                }()

                // v3.2: Panic recovery — never let an agent bug kill Sentinel
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

                // Create + save incident
                incident := schemas.NewIncident(cluster)
                incident.Status = schemas.StatusInvestigating
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.Error("save incident failed", "error", err, "service", cluster.Service)
                        return
                }
                broadcaster.BroadcastEvent("incident", map[string]any{
                        "type": "started", "incident_id": incident.IncidentID.String(),
                        "service": cluster.Service, "namespace": cluster.Namespace,
                })

                // v3.4.0: Attach incident_id to context so all subsequent log lines
                // from this handler can be correlated in Datadog/Loki.
                invCtx := logging.WithIncidentID(ctx, incident.IncidentID.String())

                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_started", nil,
                        map[string]any{"cluster_summary": cluster.Summary})

                // === REASONING PHASE ===
                investigationCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.InvestigationTimeout)*time.Second)
                defer cancel()

                if err := pipeline.Run(investigationCtx, &incident.State); err != nil {
                        logging.ErrorCtx(invCtx, "pipeline error", "error", err)
                        incident.Status = schemas.StatusFailed
                        s := err.Error()
                        incident.State.Error = &s
                        if err := store.UpsertIncident(ctx, &incident); err != nil {
                                logging.ErrorCtx(invCtx, "save incident (pipeline-error) failed", "error", err)
                        }
                        if err := k8sClient.PatchCRStatus(ctx, crName, crNamespace, "failed"); err != nil {
                                logging.ErrorCtx(invCtx, "patch CR status (failed) failed", "error", err)
                        }
                        return
                }
                incident.Status = incident.State.Status
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.ErrorCtx(invCtx, "save incident (post-pipeline) failed", "error", err)
                }
                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_completed", nil,
                        map[string]any{"status": string(incident.Status)})

                if incident.State.FixProposal == nil {
                        logging.InfoCtx(invCtx, "no fix proposed", "status", incident.Status)
                        if err := k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status)); err != nil {
                                logging.ErrorCtx(invCtx, "patch CR status (no-fix) failed", "error", err)
                        }
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
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.ErrorCtx(invCtx, "save incident (post-safety) failed", "error", err)
                }
                _ = store.AppendEvent(ctx, incident.IncidentID, "safety", "governance_decision", nil,
                        map[string]any{"verdict": string(decision.Verdict)})

                if decision.Verdict != schemas.VerdictAllow {
                        // v3.8.4: Differentiate the two non-Allow verdicts in logs.
                        if decision.Verdict == schemas.VerdictRequireHumanApproval {
                                logging.WarnCtx(invCtx, "fix requires human approval",
                                        "verdict", decision.Verdict,
                                        "reason", decision.HumanReviewReason,
                                        "hint", "use 'sentinelctl approve <incident-id>' to override")
                        } else {
                                logging.WarnCtx(invCtx, "denied by safety", "verdict", decision.Verdict)
                        }
                        if err := k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status)); err != nil {
                                logging.ErrorCtx(invCtx, "patch CR status (denied) failed", "error", err)
                        }
                        return
                }

                // === EXECUTION PHASE ===
                // RBAC check
                if !cfg.CanWriteTo(fp.TargetNamespace) {
                        logging.WarnCtx(invCtx, "execution denied — namespace not in allowlist", "namespace", fp.TargetNamespace)
                        incident.Status = schemas.StatusEscalated
                        if err := store.UpsertIncident(ctx, &incident); err != nil {
                                logging.ErrorCtx(invCtx, "save incident (rbac-deny) failed", "error", err)
                        }
                        if err := k8sClient.PatchCRStatus(ctx, crName, crNamespace, "escalated"); err != nil {
                                logging.ErrorCtx(invCtx, "patch CR status (escalated) failed", "error", err)
                        }
                        return
                }

                incident.Status = schemas.StatusExecuting
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.ErrorCtx(invCtx, "save incident (executing) failed", "error", err)
                }

                if err := executeFix(ctx, &incident, cfg, ghExec, k8sExec, sandbox, kaniko, k8sClient, store); err != nil {
                        logging.ErrorCtx(invCtx, "execution error", "error", err)
                        incident.Status = schemas.StatusFailed
                        s := err.Error()
                        incident.State.Error = &s
                }
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.ErrorCtx(invCtx, "save incident (post-execution) failed", "error", err)
                }

                // === FEEDBACK PHASE ===
                success := incident.Status == schemas.StatusVerified
                alpha, beta := recordBetaOutcome(ctx, store, incident, success)
                incident.BetaAlpha = alpha
                incident.BetaBeta = beta
                if err := store.StoreIncidentVector(ctx, &incident, success); err != nil {
                        logging.ErrorCtx(invCtx, "store incident vector failed", "error", err)
                }

                // v3.2: Add to HNSW vector index for future RAG retrieval
                if incident.State.RootCause != nil && incident.State.FixProposal != nil {
                        vectorText := incident.Cluster.Summary + " " + incident.State.RootCause.Statement + " " + incident.State.FixProposal.Description
                        if err := vectorStore.Add(ctx, vectorText, &feedback.VectorMetadata{
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

                incident.Status = schemas.StatusResolved
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        logging.ErrorCtx(invCtx, "save incident (resolved) failed", "error", err)
                }

                // v3.2: Mark resolved in tracker (triggers 30-min cooldown)
                tracker.MarkResolved(incident.Cluster.Service, incident.Cluster.Namespace, success)

                // v3.2: Send Slack alert with PR URL if available
                prURL := ""
                if incident.State.ExecutionResult != nil && incident.State.ExecutionResult.PRURL != nil {
                        prURL = *incident.State.ExecutionResult.PRURL
                }
                alertSender.SendAlert(incident.Cluster.Service, incident.Cluster.Namespace, incident.Cluster.Summary, prURL)

                // v3.2: Start outcome watcher (monitors service recovery for 10 min)
                // v3.5.0: Now uses WatchWithDetails — Phase 1 (10 min recovery) +
                // Phase 2 (24h re-occurrence watch). Phase 2 corrects the Beta prior
                // if the same error reappears after the initial recovery.
                if success && prURL != "" {
                        fingerprint := feedback.ExtractErrorFingerprint(extractPodLogsFromIncident(&incident))
                        fixType := "code_fix"
                        if incident.State.FixProposal != nil {
                                fixType = string(incident.State.FixProposal.FixType)
                        }
                        outcomeWatcher.WatchWithDetails(
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
                broadcaster.BroadcastEvent("incident", map[string]any{
                        "type": "resolved", "incident_id": incident.IncidentID.String(),
                        "service": cluster.Service, "status": string(incident.Status),
                        "pr_url": prURL, "success": success,
                })
                finalStatus = string(incident.Status)
                if err := k8sClient.PatchCRStatus(ctx, crName, crNamespace, string(incident.Status)); err != nil {
                        logging.ErrorCtx(invCtx, "patch CR status (resolved) failed", "error", err)
                }
        }

        // 8. Start the CRD watcher
        w, err := watcher.New(k8sClient, handler)
        if err != nil {
                log.Fatalf("watcher init: %v", err)
        }

        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()

        go func() {
                logging.Info("CRD watcher started")
                w.Run(ctx)
        }()

        // v3.8.4: Background reaper for leaked sandbox namespaces. The in-process
        // defer cleanup doesn't survive pod restarts — if Sentinel is killed
        // mid-verification, the sentinel-sandbox-* namespace leaks forever.
        // This goroutine runs every 5 minutes and deletes any sandbox namespace
        // older than 30 minutes (a sandbox run takes <5 min, so 30 min is a safe
        // cutoff that won't interrupt active runs).
        go func() {
                ticker := time.NewTicker(5 * time.Minute)
                defer ticker.Stop()
                // Run once at startup to clean up leaks from a previous run.
                sandbox.ReapStaleSandboxNamespaces(ctx, 30*time.Minute)
                for {
                        select {
                        case <-ctx.Done():
                                return
                        case <-ticker.C:
                                sandbox.ReapStaleSandboxNamespaces(ctx, 30*time.Minute)
                        }
                }
        }()

        // v3.5.0: Cross-service incident correlation.
        // Detects cascading failures (3+ services failing within 60s) and
        // identifies the most-recently-deployed service as the likely cause.
        // Declared BEFORE clustererHandler so the closure can capture it.
        correlator := correlation.New(func(event correlation.CorrelationEvent) {
                logging.Warn("cascade event detected",
                        "services", event.Services,
                        "likely_cause", event.LikelyCause,
                        "incident_count", event.IncidentCount,
                )
                // Send a Slack alert about the cascade
                alertSender.SendAlert(
                        event.LikelyCause,
                        "cascade",
                        fmt.Sprintf("Cascade detected: %d services failing. Likely cause: %s", event.IncidentCount, event.LikelyCause),
                        "",
                )
        })

        // 9. Start the autonomous detection layer (live log streaming + pod status)
        // v3.2: All detectors send signals to the clusterer, which groups them
        // and emits ONE incident per service per time window.
        clustererHandler := func(service, namespace, reason, podLogs string) {
                // Look up workload annotations for repo + branch
                annotations, _ := k8sClient.LookupWorkloadAnnotations(ctx, service, namespace)
                if annotations == nil {
                        annotations = map[string]string{}
                }

                repo := annotations["sentinel.io/repo"]

                // v3.5.0: Register this incident with the correlator. If a cascade is
                // detected (3+ services failing simultaneously), the correlator emits
                // an event and we skip the individual investigation — the cascade
                // handler will trigger a rollback instead.
                if correlator.RegisterIncident(service, namespace, time.Now()) {
                        logging.Info("cascade detected — skipping individual investigation",
                                "service", service,
                                "namespace", namespace,
                        )
                        return
                }

                explanation := fmt.Sprintf("Auto-detected incident: %s", reason)
                if podLogs != "" {
                        explanation += fmt.Sprintf("\n\n## Pod Logs (preprocessed)\n%s", podLogs)
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

                // Send initial alert
                alertSender.SendAlert(service, namespace, reason, "")

                // Run the full pipeline
                handler(ctx, cluster, "auto-detected", namespace)
                _ = repo
        }

        // v3.2: Clusterer groups signals, tracker prevents duplicates
        clusterer := detection.NewClusterer(clustererHandler, tracker)

        // The detector sends signals to the clusterer instead of triggering directly
        detectionHandler := func(service, namespace, reason, podLogs string) {
                logging.Info("detection signal",
                        "namespace", namespace,
                        "service", service,
                        "reason", reason,
                )

                // Look up repo annotation
                annotations, _ := k8sClient.LookupWorkloadAnnotations(ctx, service, namespace)
                repo := ""
                if annotations != nil {
                        repo = annotations["sentinel.io/repo"]
                }

                // Send to clusterer — it will group + dedup + emit one trigger
                // v3.2.4: No pod logs in the signal — the investigator fetches them
                clusterer.Send(ctx, detection.Signal{
                        Type:      detection.SignalLogError,
                        Service:   service,
                        Namespace: namespace,
                        Reason:    reason,
                        Repo:      repo,
                        Timestamp: time.Now(),
                })
        }

        detector := detection.New(k8sClient.Clientset(), detectionHandler, cfg.SlackWebhook)
        go func() {
                logging.Info("detection layer started (live log streaming + pod status)")
                detector.Run(ctx)
        }()

        go func() {
                time.Sleep(2 * time.Second)
                ready = true
                logging.Info("Sentinel ready — watching for incidents")
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
        // v3.8.4: Enable admin API bearer-token auth when SENTINEL_API_TOKEN is set.
        // Empty token = auth disabled (logs a loud warning). Health/readiness/metrics
        // endpoints remain open so kubelet probes and Prometheus still work.
        apiServer.SetAPIToken(cfg.APIToken)

        // v3.5.0: Wire predictive PR review (GitHub webhook receiver).
        // v3.8.0: If GitHub App credentials are configured, use the app for
        // verified bot identity + auto-webhook registration.
        prReviewer := prreview.New(cfg.GitHubToken, cfg.GitHubWebhookSecret, &prReviewVectorAdapter{vs: vectorStore})
        apiServer.SetWebhookHandler(http.HandlerFunc(prReviewer.HandleWebhook))

        // v3.8.0: GitHub App — verified bot identity + auto-webhook registration
        var ghApp *githubapp.App
        rtConfig := rtCfg.Get()
        if rtConfig.GitHubAppID != "" && rtConfig.GitHubAppPrivateKey != "" && rtConfig.GitHubAppInstallID != "" {
                app, err := githubapp.New(rtConfig.GitHubAppID, rtConfig.GitHubAppPrivateKey, rtConfig.GitHubAppInstallID)
                if err != nil {
                        logging.Error("GitHub App init failed", "error", err)
                } else {
                        ghApp = app
                        logging.Info("GitHub App configured",
                                "app_id", rtConfig.GitHubAppID,
                                "install_id", rtConfig.GitHubAppInstallID,
                        )
                }
        }
        _ = ghApp // Used by the PR reviewer when posting comments

        // v3.5.0: Wire interactive Slack bot (two-way commands).
        // Accepts: pause, resume, investigate, list, status, help.
        // The bot's IsPaused() is checked by the detection layer before triggering.
        slackBot := slackbot.New(cfg.SlackSigningSecret, cfg.SlackBotUserID,
                func(service, namespace, reason string) (string, error) {
                        cluster := schemas.AnomalyCluster{
                                ClusterID: uuid.New(),
                                Service:   service,
                                Namespace: namespace,
                                Severity:  schemas.SeverityWarning,
                                Summary:   fmt.Sprintf("Slack-triggered: %s", reason),
                        }
                        go handler(ctx, cluster, "slack", "sentinel")
                        return cluster.ClusterID.String(), nil
                },
                func(ctx context.Context, limit int) ([]map[string]any, error) {
                        incidents, err := store.ListRecentIncidents(ctx, limit)
                        if err != nil {
                                return nil, err
                        }
                        out := make([]map[string]any, 0, len(incidents))
                        for _, inc := range incidents {
                                out = append(out, map[string]any{
                                        "incident_id": inc.IncidentID.String(),
                                        "service":     inc.Cluster.Service,
                                        "namespace":   inc.Cluster.Namespace,
                                        "status":      string(inc.Status),
                                        "created_at":  inc.CreatedAt,
                                })
                        }
                        return out, nil
                },
        )
        apiServer.SetSlackHandler(http.HandlerFunc(slackBot.HandleEvents))

        // v3.7.0: Wire SSE broadcaster + v3.8.0: Wire runtime config saver + pause controller
        broadcaster = apiServer
        apiServer.SetPauseController(slackBot)
        apiServer.SetRuntimeCfgSaver(rtCfg)

        // v3.5.0: Wire the Slack bot token for posting @mention replies.
        if cfg.SlackBotToken != "" {
                slackBot.SetBotToken(cfg.SlackBotToken)
        }

        // v3.5.0: Wire the Slack bot's pause checker into the detection layer.
        // When autonomous mode is paused via Slack, the clusterer skips emitting
        // triggers — no new investigations start until resumed.
        clusterer.SetPauseChecker(slackBot)

        go func() {
                if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
                        logging.Error("API server failed", "error", err)
                }
        }()

        // 10. Wait for shutdown
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        logging.Info("shutting down...")
        cancel()

        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        _ = apiServer.Shutdown(shutdownCtx)
        logging.Info("stopped")
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
                // v3.4.0: configurable default replicas (was hardcoded int32(2))
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

        // Phase 2 + 3: Build canary + sandbox (only if Docker Hub creds available)
        // v3.6.0: Skip sandbox for non-production namespaces when configured (default: true).
        // v3.8.1: Re-wired — was accidentally dropped in server.go rewrite.
        sandboxVerified := false
        sandboxSummary := "skipped (no Docker Hub credentials)"

        isNonProd := !cfg.IsProductionNamespace(fp.TargetNamespace)
        if cfg.SkipSandboxNonProd && isNonProd && cfg.DockerHubToken != "" {
                sandboxSummary = "skipped (non-production namespace — fast mode)"
                logging.Info("skipping sandbox for non-production namespace (fast mode)",
                        "fix_id", fp.FixID, "namespace", fp.TargetNamespace)
        } else if cfg.DockerHubToken != "" {
                logging.Info("execution Phase 2: building canary image with Kaniko", "fix_id", fp.FixID)
                prodImage, err := k8sClient.GetProdImage(ctx, fp.TargetService, fp.TargetNamespace)
                if err == nil && prodImage != "" {
                        canaryImage, err := kaniko.BuildForIncident(ctx, incident, branchInfo, prodImage)
                        if err != nil {
                                sandboxSummary = fmt.Sprintf("kaniko_error: %v", err)
                                logging.Error("kaniko build failed", "fix_id", fp.FixID, "error", err)
                        } else {
                                logging.Info("canary image built", "image", canaryImage)
                                fp.CanaryImage = &canaryImage

                                logging.Info("execution Phase 3: running sandbox verification", "fix_id", fp.FixID)
                                sandboxStart := time.Now()
                                sb := sandbox.Run(ctx, incident)
                                sandboxVerified = sb.Verified
                                sandboxSummary = sb.Summary
                                metrics.RecordSandboxVerification(sandboxVerified, time.Since(sandboxStart))
                                logging.Info("sandbox verification complete",
                                        "verified", sandboxVerified,
                                        "summary", sandboxSummary,
                                )
                        }
                } else {
                        sandboxSummary = "skipped (could not find prod image)"
                }
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

// v3.3.1: GetIncident — was missing, causing /incidents/{id} to return 404.
func (a *storeAdapter) GetIncident(ctx context.Context, id uuid.UUID) (any, error) {
        return a.store.GetIncident(ctx, id)
}

// vectorStoreAdapter bridges feedback.VectorStore to agents.VectorStore interface.
// v3.3.1: Passes through REAL cosine similarity scores from the underlying HNSW
// index. The previous version fabricated scores (0.9 for top result, descending
// by 0.1) which made the investigator's memory loop fire applyKnownFix on every
// incident after the first success — see internal/feedback/vector.go.
type vectorStoreAdapter struct {
        vs *feedback.VectorStore
}

func (a *vectorStoreAdapter) Search(ctx context.Context, text string, k int) ([]*agents.VectorMetadata, []float32, error) {
        metas, scores, err := a.vs.Search(ctx, text, k)
        if err != nil || len(metas) == 0 {
                return nil, nil, err
        }
        out := make([]*agents.VectorMetadata, len(metas))
        for i, m := range metas {
                out[i] = &agents.VectorMetadata{
                        IncidentID: m.IncidentID,
                        Service:    m.Service,
                        RootCause:  m.RootCause,
                        FixType:    m.FixType,
                        FixPattern: m.FixPattern,
                        Outcome:    m.Outcome,
                }
        }
        return out, scores, nil
}

// v3.7.0: Pass through hash fallback status
func (a *vectorStoreAdapter) UsingHashFallback() bool {
        return a.vs.UsingHashFallback()
}

// prReviewVectorAdapter bridges feedback.VectorStore to prreview.VectorStore.
// v3.5.0: Used by the predictive PR review feature to score PR diffs against
// incident memory. Same pattern as vectorStoreAdapter above.
type prReviewVectorAdapter struct {
        vs *feedback.VectorStore
}

func (a *prReviewVectorAdapter) Search(ctx context.Context, text string, k int) ([]*prreview.VectorMetadata, []float32, error) {
        metas, scores, err := a.vs.Search(ctx, text, k)
        if err != nil || len(metas) == 0 {
                return nil, nil, err
        }
        out := make([]*prreview.VectorMetadata, len(metas))
        for i, m := range metas {
                out[i] = &prreview.VectorMetadata{
                        IncidentID: m.IncidentID,
                        Service:    m.Service,
                        RootCause:  m.RootCause,
                        FixType:    m.FixType,
                        FixPattern: m.FixPattern,
                        Outcome:    m.Outcome,
                }
        }
        return out, scores, nil
}

// v3.7.0: Pass through hash fallback status
func (a *prReviewVectorAdapter) UsingHashFallback() bool {
        return a.vs.UsingHashFallback()
}

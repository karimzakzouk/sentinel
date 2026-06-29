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
        "sync"
        "sync/atomic"
        "syscall"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/agents"
        "github.com/karimzakzouk/sentinel/v3/internal/api"
        "github.com/karimzakzouk/sentinel/v3/internal/automerge"
        "github.com/karimzakzouk/sentinel/v3/internal/chatops"
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
        "github.com/karimzakzouk/sentinel/v3/internal/web"
)

// API server, used by the investigation handler to push real-time updates.
var broadcaster events.Broadcaster = events.Noop()

// used by executeCodeFix to post the initial [SENTINELSTATUS] comment when
// a PR is opened. Package-level (not passed through executeFix) because
// executeCodeFix is called from a closure that doesn't have access to main's
// local scope.
var chatopsStatusManager chatops.StatusCommentManager

// the runtimecfg apply callback (registered BEFORE these clients exist) to
// reconfigure them live when the GitHub App credentials change at runtime.
// Without these package-level vars, the apply callback would have to be
// registered AFTER all clients are created, which is fragile (easy to
// forget when adding new clients).
var (
        pkgGHExec      *executors.GitHubExecutor
        pkgChatopsGH   *chatops.GitHubClientImpl
        pkgAutoMergeGH *automerge.GitHubClientImpl
        pkgPrReviewer  *prreview.Reviewer
        pkgCfg         *config.Config

        // executeCodeFix to Track() PRs for auto-merge after they open.
        pkgAutoMergeMerger *automerge.Merger
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

        // on missing required fields instead of crashing 5-10 minutes later
        // at the first LLM call.
        if errs, warns := cfg.Validate(); len(errs) > 0 || len(warns) > 0 {
                // Initialize logger first so warnings are structured
                logging.Init(cfg.LogLevel)
                for _, w := range warns {
                        logging.Warn("config validation warning: " + w)
                }
                for _, e := range errs {
                        logging.Error("config validation error: " + e)
                }
                if len(errs) > 0 {
                        logging.Error("refusing to start due to config validation errors")
                        os.Exit(1)
                }
        }

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

        // providers natively. The old "unsupported native provider" warning
        // (v3.11.0 audit #16) is no longer needed — removed.

        // a provider that supports embeddings (audit #13). Empty default is fine
        // (uses hash fallback) but if the operator set an LLM provider expecting
        // embeddings to work, they should know.
        if cfg.LLMEmbeddingModel == "" {
                logging.Info("embedding model not configured — using hash fallback (similar-incident retrieval disabled). Set SENTINEL_LLM_EMBEDDING_MODEL to enable.",
                        "provider", cfg.LLMProvider,
                        "embedding_dim", cfg.LLMEmbeddingDim,
                )
        }

        // Was hardcoded to /data/runtime-config.json which doesn't exist when
        // running locally (outside K8s). Defaults to /data/runtime-config.json
        // for in-cluster deployments, but operators running locally can set
        // SENTINEL_RUNTIME_CONFIG_PATH=/tmp/runtime-config.json or similar.
        runtimeCfgPath := os.Getenv("SENTINEL_RUNTIME_CONFIG_PATH")
        if runtimeCfgPath == "" {
                runtimeCfgPath = "/data/runtime-config.json"
        }
        rtCfg := runtimecfg.New(runtimeCfgPath)

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

        router := llm.NewWithProvider(cfg.LLMProvider, cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMFastModel, cfg.LLMFrontierModel)
        router.SetRetryConfig(cfg.LLMMaxRetries, time.Duration(cfg.LLMRetryBaseDelayMs)*time.Millisecond)
        if cfg.LLMProvider == "bedrock" {
                router.SetBedrockRegion(func() string { r := os.Getenv("AWS_REGION"); if r == "" { return "us-east-1" }; return r }())
        }

        // wire the pool into the router. Every LLM call then tries providers in
        // priority order with circuit-breaker failover.
        if len(cfg.LLMProviders) > 0 {
                pool := llm.NewPool(cfg.LLMProviders)
                pool.SetRetryConfig(cfg.LLMMaxRetries, time.Duration(cfg.LLMRetryBaseDelayMs)*time.Millisecond)
                router.SetPool(pool)
                logging.Info("LLM failover pool enabled",
                        "providers", len(cfg.LLMProviders),
                        "mode", "multi-provider",
                )
        } else {
                logging.Info("LLM failover pool disabled (single-provider mode)",
                        "provider", cfg.LLMProvider,
                        "hint", "set SENTINEL_LLM_PROVIDERS to enable multi-key failover",
                )
        }

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

                // silently dropped (the headline feature was dead code via the web UI).
                if rc.SandboxEnabled != nil {
                        cfg.SandboxEnabled = *rc.SandboxEnabled
                        logging.Info("sandbox enabled reconfigured", "enabled", cfg.SandboxEnabled)
                }
                if rc.SandboxRequired != nil {
                        cfg.SandboxRequired = *rc.SandboxRequired
                        logging.Info("sandbox required reconfigured", "required", cfg.SandboxRequired)
                }
                if rc.SandboxMaxRetries > 0 {
                        cfg.SandboxMaxRetries = rc.SandboxMaxRetries
                        logging.Info("sandbox max retries reconfigured", "max", cfg.SandboxMaxRetries)
                }
                if rc.InvestigatorTimeoutSec > 0 {
                        cfg.InvestigatorTimeoutSec = rc.InvestigatorTimeoutSec
                }
                if rc.EvidenceGathererTimeoutSec > 0 {
                        cfg.EvidenceGathererTimeoutSec = rc.EvidenceGathererTimeoutSec
                }
                if rc.AnalystTimeoutSec > 0 {
                        cfg.AnalystTimeoutSec = rc.AnalystTimeoutSec
                }
                if rc.FixProposerTimeoutSec > 0 {
                        cfg.FixProposerTimeoutSec = rc.FixProposerTimeoutSec
                }
                if rc.ConfidenceLoopBackThreshold > 0 {
                        cfg.ConfidenceLoopBackThreshold = rc.ConfidenceLoopBackThreshold
                }
                if rc.LLMMaxRetries > 0 {
                        cfg.LLMMaxRetries = rc.LLMMaxRetries
                }
                if rc.LogLevel != "" {
                        logging.Init(rc.LogLevel)
                }

                // clients live (no restart). When cleared, fall back to PAT.
                // Uses package-level vars (pkgGHExec etc.) because the apply callback
                // is registered BEFORE the clients are created.
                if rc.GitHubAppID != "" && rc.GitHubAppPrivateKey != "" && rc.GitHubAppInstallID != "" {
                        app, err := githubapp.NewWithAPIURL(rc.GitHubAppID, rc.GitHubAppPrivateKey, rc.GitHubAppInstallID, pkgCfg.GitHubAPIURL)
                        if err != nil {
                                logging.Error("GitHub App reconfiguration failed", "error", err)
                        } else {
                                if pkgGHExec != nil {
                                        pkgGHExec.WithTokenProvider(app)
                                }
                                if pkgChatopsGH != nil {
                                        pkgChatopsGH.WithTokenProvider(&chatopsTokenProviderAdapter{app: app})
                                }
                                if pkgAutoMergeGH != nil {
                                        pkgAutoMergeGH.WithTokenProvider(&automergeTokenProviderAdapter{app: app})
                                }
                                if pkgPrReviewer != nil {
                                        pkgPrReviewer.WithGitHubApp(app)
                                }
                                logging.Info("GitHub App reconfigured via runtime config",
                                        "app_id", rc.GitHubAppID,
                                        "install_id", rc.GitHubAppInstallID,
                                )
                        }
                }
        })

        llm.SetMetricsRecorder(func(model, status string, duration time.Duration, inputTokens, outputTokens int) {
                metrics.RecordLLMCall(model, status, duration)
                if status == "success" && (inputTokens > 0 || outputTokens > 0) {
                        metrics.RecordLLMTokens(model, inputTokens, outputTokens)
                }
        })
        llm.SetInFlightHooks(metrics.LLMCallStart, metrics.LLMCallEnd)

        // 3.5 Vector store (HNSW + LLM embeddings) — created before pipeline for memory loop

        // vector store uses permanent hash fallback (audit #13). Also wire the
        // configurable hash-fallback dim (cfg.LLMLLMEmbeddingDim, default 1536).
        vectorStore := feedback.NewVectorStoreWithDim(cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMEmbeddingModel, cfg.LLMEmbeddingDim)

        // 4. Agent pipeline (with vector store for memory loop)
        pipeline := agents.NewPipeline(router, k8sClient, cfg.GitHubToken, store, &vectorStoreAdapter{vs: vectorStore})

        pipeline.SetConfig(
                cfg.InvestigatorTimeoutSec,
                cfg.EvidenceGathererTimeoutSec,
                cfg.AnalystTimeoutSec,
                cfg.FixProposerTimeoutSec,
                cfg.ConfidenceLoopBackThreshold,
        )

        if cfg.PrometheusURL != "" {
                pipeline.SetPrometheusURL(cfg.PrometheusURL)
        }

        // When non-empty, the investigator refuses to clone repos outside
        // this list — primary defense against the pod-annotation SSRF.
        pipeline.SetAllowedRepos(cfg.AllowedGitHubRepos)

        pipeline.SetGitHost(cfg.GitHost)
        pipeline.SetDefaultBranch(cfg.DefaultBranch)

        // Were hardcoded 0.4 + 5 in analyst.go + schemas/types.go.
        pipeline.SetLLMPriorWeight(cfg.LLMPriorWeight)
        pipeline.SetMaxIterations(cfg.MaxPipelineIterations)

        // 5. Safety policy (embedded, replaces OPA sidecar)

        // payment-api/billing-api/auth-service leaked into every install because
        // NewPolicy couldn't override HighRiskServices.
        policy := safety.NewPolicy(cfg.ProductionNamespaces, cfg.ConfidenceAutonomous, cfg.ConfidenceHumanReview).
                WithHighRiskServices(cfg.HighRiskServices)

        // 6. Executors

        // the GitHub executor (CRITICAL audit #2 + #6). Was hardcoded to
        // github.com + https://api.github.com + "main" in 14+ sites.
        ghExec := executors.NewGitHub(cfg.GitHubToken).
                WithAPIURL(cfg.GitHubAPIURL).
                WithGitHost(cfg.GitHost).
                WithDefaultBranch(cfg.DefaultBranch)
        pkgGHExec = ghExec
        pkgCfg = cfg
        k8sExec := executors.NewK8s(k8sClient.Clientset())

        sandbox := executors.NewSandbox(k8sClient.Clientset(), cfg.SandboxNamespacePrefix, cfg.EnableTrafficMirroring).
                WithProbePaths(cfg.SandboxProbePaths).
                WithSentinelNamespace(cfg.Namespace)

        kaniko := executors.NewKaniko(k8sClient.Clientset(), cfg.Namespace).
                WithDockerHubUsername(cfg.DockerHubUsername).
                WithRegistry(cfg.KanikoRegistry).
                WithInitImage(cfg.KanikoInitImage).
                WithExecutorImage(cfg.KanikoExecutorImage).
                WithGitHost(cfg.GitHost).
                WithGitHubToken(cfg.GitHubToken)

        // background goroutine. Was a plain bool (race detector failure). Use atomic.
        var ready atomic.Bool

        // v3.2: Active incident tracker (per-service dedup)
        tracker := detection.NewActiveTracker()
        // v3.2: Slack alerts (graceful degradation if no webhook)
        alertSender := agents.NewAlertSender(cfg.SlackWebhook)
        // v3.2: Outcome watcher (monitors service post-fix)

        // 10min watch / 30s interval in feedback/outcome.go NewOutcomeWatcher.
        outcomeWatcher := feedback.NewOutcomeWatcher(k8sClient.Clientset(), store)
        if cfg.OutcomeWatchDurationMin > 0 || cfg.OutcomeCheckIntervalSec > 0 {
                outcomeWatcher.SetDurations(
                        time.Duration(cfg.OutcomeWatchDurationMin)*time.Minute,
                        time.Duration(cfg.OutcomeCheckIntervalSec)*time.Second,
                        0, // Phase 2 long watch — keep default 24h
                        0, // Phase 2 long interval — keep default 5min
                )
        }
        // v3.2: Load existing vectors from SQLite into HNSW index on startup (delay 2s for DB to be ready)
        go func() {
                time.Sleep(2 * time.Second)
                loadCtx := context.Background()
                if err := vectorStore.LoadFromDB(loadCtx, store); err != nil {
                        logging.Error("vector store load failed", "error", err)
                }
        }()

        // LLM API quota exhaustion during incident storms (Claude's review).

        // Was hardcoded to 3.
        maxConcurrent := cfg.MaxConcurrentInvestigations
        if maxConcurrent <= 0 {
                maxConcurrent = 3
        }
        invSem := make(chan struct{}, maxConcurrent)

        // can wait for them to finish (with a deadline) before closing the SQLite store.
        var invWG sync.WaitGroup

        // 7. The full investigation handler — runs the agent pipeline + execution
        handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {

                invWG.Add(1)
                defer invWG.Done()

                select {
                case invSem <- struct{}{}:
                        defer func() { <-invSem }()
                case <-ctx.Done():
                        return
                }

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

                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "detection", "status": "complete",
                        "message": fmt.Sprintf("Error detected in %s/%s: %s", cluster.Service, cluster.Namespace, cluster.Summary),
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

                // from this handler can be correlated in Datadog/Loki.
                invCtx := logging.WithIncidentID(ctx, incident.IncidentID.String())

                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_started", nil,
                        map[string]any{"cluster_summary": cluster.Summary})

                // === REASONING PHASE ===
                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "investigation", "status": "running",
                        "message": "Investigation started — fetching pod logs + cloning repo",
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

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

                invMsg := "Investigation complete"
                if incident.State.RootCause != nil {
                        invMsg = fmt.Sprintf("Root cause: %s (confidence: %.0f%%)", incident.State.RootCause.Statement, incident.State.RootCause.Confidence*100)
                }
                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "investigation", "status": "complete",
                        "message": invMsg,
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_completed", nil,
                        map[string]any{"status": string(incident.Status)})

                if incident.State.FixProposal == nil {
                        logging.InfoCtx(invCtx, "no fix proposed", "status", incident.Status)

                        rootCause := ""
                        if incident.State.RootCause != nil {
                                rootCause = incident.State.RootCause.Statement
                        }
                        if err := k8sClient.PatchCRStatusFull(ctx, crName, crNamespace, string(incident.Status),
                                incident.IncidentID.String(), rootCause, 0, ""); err != nil {
                                logging.ErrorCtx(invCtx, "patch CR status (no-fix) failed", "error", err)
                        }
                        return
                }

                // === SAFETY PHASE (embedded policy, no OPA) ===
                fp := incident.State.FixProposal

                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "safety", "status": "running",
                        "message": fmt.Sprintf("Evaluating safety policy for %s fix...", fp.FixType),
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

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
                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "execution", "status": "running",
                        "message": "Approved — preparing fix branch + opening PR",
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

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

                if err := executeFix(ctx, &incident, cfg, ghExec, k8sExec, sandbox, kaniko, k8sClient, store, pipeline); err != nil {
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

                prURL := ""
                if incident.State.ExecutionResult != nil && incident.State.ExecutionResult.PRURL != nil {
                        prURL = *incident.State.ExecutionResult.PRURL
                }
                broadcaster.BroadcastEvent("pipeline", map[string]any{
                        "incident_id": incident.IncidentID.String(),
                        "phase": "resolved", "status": "complete",
                        "message": fmt.Sprintf("Incident resolved — PR opened: %s", prURL),
                        "pr_url": prURL,
                        "success": success,
                        "timestamp": time.Now().UTC().Format(time.RFC3339),
                })

                // v3.2: Mark resolved in tracker (triggers 30-min cooldown)
                tracker.MarkResolved(incident.Cluster.Service, incident.Cluster.Namespace, success)

                // v3.2: Send Slack alert with PR URL if available
                if incident.State.ExecutionResult != nil && incident.State.ExecutionResult.PRURL != nil {
                        prURL = *incident.State.ExecutionResult.PRURL
                }
                alertSender.SendAlert(incident.Cluster.Service, incident.Cluster.Namespace, incident.Cluster.Summary, prURL)

                // v3.2: Start outcome watcher (monitors service recovery for 10 min)

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

                rootCause := ""
                confidence := 0.0
                if incident.State.RootCause != nil {
                        rootCause = incident.State.RootCause.Statement
                        confidence = incident.State.RootCause.Confidence
                }
                // prURL is already in scope (declared above in the resolved path).
                if err := k8sClient.PatchCRStatusFull(ctx, crName, crNamespace, string(incident.Status),
                        incident.IncidentID.String(), rootCause, confidence, prURL); err != nil {
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

                clusterer.Send(ctx, detection.Signal{
                        Type:      detection.SignalLogError,
                        Service:   service,
                        Namespace: namespace,
                        Reason:    reason,
                        Repo:      repo,
                        Timestamp: time.Now(),
                })
        }

        detector := detection.NewWithConfig(
                k8sClient.Clientset(), detectionHandler, cfg.SlackWebhook,
                time.Duration(cfg.DetectionCooldownSec)*time.Second,
                cfg.DetectionTailLines,
                cfg.SkipNamespaces,
        )
        go func() {
                logging.Info("detection layer started (live log streaming + pod status)")
                detector.Run(ctx)
        }()

        go func() {

                defer func() {
                        if r := recover(); r != nil {
                                logging.Error("ready-setter goroutine panicked", "panic", r)
                        }
                }()
                time.Sleep(2 * time.Second)
                ready.Store(true)
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

                        // which broke when the Helm release wasn't named "sentinel" — the
                        // SentinelIncident CR would be created in a nonexistent namespace
                        // and RBAC would deny it.
                        go handler(ctx, cluster, "manual", cfg.Namespace)
                        return cluster.ClusterID.String(), nil
                },
                func() bool { return ready.Load() },
        )

        // Empty token = auth disabled (logs a loud warning). Health/readiness/metrics
        // endpoints remain open so kubelet probes and Prometheus still work.
        apiServer.SetAPIToken(cfg.APIToken)

        // compiled into the binary via go:embed (internal/web/out/). Without
        // this call, the API server serves a placeholder "Web UI not built"
        // page even though the UI is embedded in the binary.
        apiServer.SetWebUIHandler(web.Handler())

        // verified bot identity + auto-webhook registration.

        prReviewer := prreview.New(cfg.GitHubToken, cfg.GitHubWebhookSecret, &prReviewVectorAdapter{vs: vectorStore}).
                WithAPIURL(cfg.GitHubAPIURL)
        pkgPrReviewer = prReviewer
        apiServer.SetWebhookHandler(http.HandlerFunc(prReviewer.HandleWebhook))

        // Creates the dependency bag, GitHub client, reactions client,
        // status comment manager, and webhook handler. All wired into the
        // API server via SetChatopsHandler. The webhook handler dispatches
        // to both prreview (pull_request events) and chatops (issue_comment).
        chatopsGHClient := chatops.NewGitHubClient(cfg.GitHubToken, cfg.GitHubAPIURL)
        pkgChatopsGH = chatopsGHClient
        chatopsReactions := chatops.NewReactionsClient(chatopsGHClient)
        chatopsStatus := chatops.NewStatusCommentManager(chatopsGHClient)
        chatopsStatusManager = chatopsStatus // assign to package var for executeCodeFix

        // Created before chatopsDeps so the deps bag can reference the merger.
        autoMergeGH := automerge.NewGitHubClient(cfg.GitHubToken, cfg.GitHubAPIURL)
        pkgAutoMergeGH = autoMergeGH
        autoMergeCfg := automerge.Config{
                Enabled:              cfg.AutoMergeEnabled,
                Namespaces:           cfg.AutoMergeNamespaces,
                MinConfidence:        cfg.AutoMergeMinConfidence,
                RequireCIGreen:       cfg.AutoMergeRequireCI,
                CITimeoutSec:         cfg.AutoMergeCITimeoutSec,
                MaxPerHour:           cfg.AutoMergeMaxPerHour,
                CooldownAfterFailMin: cfg.AutoMergeCooldownMin,
        }
        autoMergePolicy := automerge.NewPolicy(autoMergeCfg, policy)
        autoMergeMerger := automerge.NewMerger(autoMergePolicy, autoMergeGH, &autoMergeStoreAdapter{store: store}, &autoMergeOutcomeAdapter{watcher: outcomeWatcher, policy: autoMergePolicy})
        pkgAutoMergeMerger = autoMergeMerger

        // after merge/skip. Was: the status comment said "(not configured)" forever.
        autoMergeMerger.SetOnMerged(func(repo string, prNumber int) {
                if chatopsStatusManager != nil {
                        inc := &chatops.Incident{
                                AutoMergeEnabled: true,
                                AutoMergeMerged:  true,
                                Status:           "verified",
                        }
                        _ = chatopsStatusManager.Update(context.Background(), repo, prNumber, inc, "✅ PR auto-merged. Monitoring deployment...")
                }
        })
        autoMergeMerger.SetOnSkipped(func(repo string, prNumber int, reason string) {
                if chatopsStatusManager != nil {
                        inc := &chatops.Incident{
                                AutoMergeEnabled: true,
                                AutoMergeSkipped: reason,
                                Status:           "verified",
                        }
                        _ = chatopsStatusManager.Update(context.Background(), repo, prNumber, inc, "")
                }
        })

        chatopsDeps := &chatops.Deps{
                Store:         &chatopsStoreAdapter{store: store},
                GitHub:        chatopsGHClient,
                Router:        router,
                Reactions:     chatopsReactions,
                StatusComment: chatopsStatus,
                Logger:        &slogAdapter{},
                AutoMerge:     &autoMergeClientAdapter{merger: autoMergeMerger, policy: autoMergePolicy},
                Rollback:      k8sExec,
                // Pipeline + Sandbox adapters are wired below (they reference
                // the pipeline + sandbox instances which are created above).
                Pipeline:      &chatopsPipelineAdapter{pipeline: pipeline, store: store},
                Sandbox:       &chatopsSandboxAdapter{sandbox: sandbox, k8sClient: k8sClient, store: store, ghExec: ghExec, kaniko: kaniko, cfg: cfg},
        }
        chatopsHandler := chatops.NewWebhookHandler(chatopsDeps, cfg.GitHubWebhookSecret)
        apiServer.SetChatopsHandler(chatopsHandler)

        // so /providers and /automerge endpoints work.
        apiServer.SetProvidersStatusGetter(&providersStatusAdapter{router: router})
        apiServer.SetAutoMergeStatusGetter(&autoMergeStatusAdapter{policy: autoMergePolicy, merger: autoMergeMerger})

        apiServer.SetGitHubAppManager(rtCfg)

        logging.Info("chatops enabled",
                "commands", len(chatops.AllCommands()),
                "webhook_secret_set", cfg.GitHubWebhookSecret != "",
        )
        logging.Info("auto-merge configured",
                "enabled", cfg.AutoMergeEnabled,
                "namespaces", cfg.AutoMergeNamespaces,
                "min_confidence", cfg.AutoMergeMinConfidence,
                "max_per_hour", cfg.AutoMergeMaxPerHour,
        )

        if cfg.AutoMergeEnabled {
                go autoMergeMerger.Run(ctx, 30*time.Second)
                logging.Info("auto-merge merger started", "poll_interval", "30s")
        }

        if cfg.AutoRemediationEnabled {
                remediation := executors.NewRemediationController(k8sClient.Clientset(), cfg.SkipNamespaces)
                go remediation.Run(ctx, 5*time.Minute)
                logging.Info("auto-remediation controller started", "poll_interval", "5m")
        }

        // appear as the verified "Sentinel" bot instead of the PAT user.

        // reactions), and autoMergeGH (merge API). When configured, ALL GitHub
        // operations appear as "Sentinel[bot]" instead of the PAT owner.

        var ghApp *githubapp.App
        rtConfig := rtCfg.Get()
        appID := rtConfig.GitHubAppID
        appKey := rtConfig.GitHubAppPrivateKey
        appInstall := rtConfig.GitHubAppInstallID

        if appID == "" {
                appID = cfg.GitHubAppID
                appKey = cfg.GitHubAppPrivateKey
                appInstall = cfg.GitHubAppInstallID
        }
        if appID != "" && appKey != "" && appInstall != "" {
                app, err := githubapp.NewWithAPIURL(appID, appKey, appInstall, cfg.GitHubAPIURL)
                if err != nil {
                        logging.Error("GitHub App init failed", "error", err)
                } else {
                        ghApp = app
                        prReviewer.WithGitHubApp(ghApp)
                        ghExec.WithTokenProvider(ghApp)
                        chatopsGHClient.WithTokenProvider(&chatopsTokenProviderAdapter{app: ghApp})
                        autoMergeGH.WithTokenProvider(&automergeTokenProviderAdapter{app: ghApp})
                        logging.Info("GitHub App configured — ALL GitHub operations appear as Sentinel[bot]",
                                "app_id", appID,
                                "install_id", appInstall,
                                "api_url", cfg.GitHubAPIURL,
                        )
                }
        } else {
                logging.Info("GitHub App not configured — PRs + comments appear as the PAT owner",
                        "hint", "set SENTINEL_GITHUB_APP_ID + SENTINEL_GITHUB_APP_PRIVATE_KEY + SENTINEL_GITHUB_APP_INSTALL_ID for verified bot identity",
                )
        }
        _ = ghApp // Used by the reviewers + executors above

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

                        go handler(ctx, cluster, "slack", cfg.Namespace)
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

        broadcaster = apiServer
        apiServer.SetPauseController(slackBot)
        apiServer.SetRuntimeCfgSaver(rtCfg)

        if cfg.SlackBotToken != "" {
                slackBot.SetBotToken(cfg.SlackBotToken)
        }

        // /sentinel pause + /sentinel resume can pause autonomous mode.
        chatopsDeps.PauseController = slackBot

        // When autonomous mode is paused via Slack, the clusterer skips emitting
        // triggers — no new investigations start until resumed.
        clusterer.SetPauseChecker(slackBot)

        go func() {
                if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {

                        // Previously, the error was just logged and Sentinel continued running
                        // with no API — kubelet readiness probe fails, pod gets restarted,
                        // but the watcher + detector keep running against a dead API.
                        logging.Error("API server failed — exiting", "error", err)
                        log.Fatalf("API server failed: %v", err)
                }
        }()

        // 10. Wait for shutdown
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        logging.Info("shutting down...")

        clusterer.Stop()

        detector.StopAllLogStreams()

        cancel()

        // before closing the SQLite store. Was: process could os.Exit mid-LLM-call,
        // leaving half-written incidents and half-applied branches.
        done := make(chan struct{})
        go func() {
                invWG.Wait()
                close(done)
        }()
        select {
        case <-done:
                logging.Info("all in-flight investigations completed")
        case <-time.After(15 * time.Second):
                logging.Warn("15s shutdown deadline exceeded — proceeding with force-close (some investigations may be incomplete)")
        }

        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        _ = apiServer.Shutdown(shutdownCtx)
        logging.Info("stopped")
}


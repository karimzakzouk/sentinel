// Package main — wiring constructor.
//
// wire.go contains the wire() function that instantiates every Sentinel
// component in dependency order and returns a fully-constructed *wiring.
// Split from main.go so main.go stays a small, readable entry point
// (main + run); the construction logic is linear and reads top-to-bottom
// here.
//
// Construction is in five phases:
//
//  1. Storage + infra (store, k8s, router, embedder, vectorStore, pipeline).
//  2. Executors + safety (ghExec, k8sExec, sandbox, kaniko, policy).
//  3. Detection + correlation (tracker, clusterer, detector, correlator,
//     outcomeWatcher, alertmanager receiver, CRD watcher).
//  4. Tier 1/2 features (attest, prometheus, postmerge, slo, causal,
//     admission, llmcost-implicit, chaos).
//  5. API surface (prreview, chatops, automerge, slackbot, apiServer).
//
// The runtimecfg apply callback is registered LAST, after every component
// it mutates has been constructed.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/agent"
	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/alertmanager"
	"github.com/karimzakzouk/sentinel/internal/api"
	"github.com/karimzakzouk/sentinel/internal/attest"
	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/causal"
	"github.com/karimzakzouk/sentinel/internal/chaos"
	"github.com/karimzakzouk/sentinel/internal/chatops"
	"github.com/karimzakzouk/sentinel/internal/config"
	"github.com/karimzakzouk/sentinel/internal/correlation"
	"github.com/karimzakzouk/sentinel/internal/detection"
	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/events"
	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/feedback"
	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/metrics"
	"github.com/karimzakzouk/sentinel/internal/observability"
	"github.com/karimzakzouk/sentinel/internal/postmerge"
	"github.com/karimzakzouk/sentinel/internal/prreview"
	"github.com/karimzakzouk/sentinel/internal/runtimecfg"
	"github.com/karimzakzouk/sentinel/internal/safeactions"
	"github.com/karimzakzouk/sentinel/internal/safety"
	"github.com/karimzakzouk/sentinel/internal/schemas"
	"github.com/karimzakzouk/sentinel/internal/slackbot"
	"github.com/karimzakzouk/sentinel/internal/slo"
	"github.com/karimzakzouk/sentinel/internal/watcher"
	"github.com/karimzakzouk/sentinel/internal/web"
)

func wire(cfg *config.Config, listenAddr string) (*wiring, error) {
	w := &wiring{
		cfg:         cfg,
		broadcaster: events.Noop(),
		ready:       &atomic.Bool{},
	}

	// --- Phase 1: Storage + infra ---

	store, err := feedback.New(cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("storage init: %w", err)
	}
	w.store = store

	k8sClient, err := k8s.New()
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("k8s client: %w", err)
	}
	w.k8sClient = k8sClient

	router := llm.NewWithProvider(cfg.LLMProvider, cfg.ResolvedBaseURL(), cfg.LLMAPIKey, cfg.LLMFastModel, cfg.LLMFrontierModel)
	router.SetRetryConfig(cfg.LLMMaxRetries, time.Duration(cfg.LLMRetryBaseDelayMs)*time.Millisecond)
	if cfg.LLMProvider == "bedrock" {
		router.SetBedrockRegion(func() string {
			r := os.Getenv("AWS_REGION")
			if r == "" {
				return "us-east-1"
			}
			return r
		}())
	}
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
	w.router = router

	llm.SetMetricsRecorder(func(model, status string, duration time.Duration, inputTokens, outputTokens int) {
		metrics.RecordLLMCall(model, status, duration)
		if status == "success" && (inputTokens > 0 || outputTokens > 0) {
			metrics.RecordLLMTokens(model, inputTokens, outputTokens)
		}
	})
	llm.SetInFlightHooks(metrics.LLMCallStart, metrics.LLMCallEnd)

	// Embedder + vector store. Auto-detects remote embeddings when an API
	// key is set; falls back to TF-IDF otherwise.
	embAPIKey := cfg.EmbeddingAPIKey
	if embAPIKey == "" && cfg.LLMEmbeddingModel != "" {
		embAPIKey = cfg.LLMAPIKey
	}
	embBaseURL := cfg.EmbeddingBaseURL
	if embBaseURL == "" && cfg.LLMEmbeddingModel != "" {
		embBaseURL = cfg.ResolvedBaseURL()
	}
	embedder, err := emb.New(emb.Config{
		Provider:  cfg.EmbeddingProvider,
		Model:     cfg.LLMEmbeddingModel,
		APIKey:    embAPIKey,
		BaseURL:   embBaseURL,
		Dimension: cfg.LLMEmbeddingDim,
	})
	if err != nil {
		logging.Error("failed to create embedder, using TF-IDF fallback", "err", err)
		embedder, _ = emb.New(emb.Config{Provider: "tfidf", Dimension: cfg.LLMEmbeddingDim})
	}
	logging.Info("embedding pipeline configured", "provider", embedder.Name(), "semantic", embedder.IsSemantic())
	w.embedder = embedder

	vectorStore := feedback.NewVectorStore(embedder)
	w.vectorStore = vectorStore

	if cfg.LLMSemanticCacheEnabled && embedder.IsSemantic() {
		cache := llm.NewSemanticCache(embedder, float32(cfg.LLMSemanticCacheThreshold), 24*time.Hour, 1000)
		router.SetCache(cache)
		logging.Info("LLM semantic cache enabled", "threshold", cfg.LLMSemanticCacheThreshold)
	} else if cfg.LLMSemanticCacheEnabled && !embedder.IsSemantic() {
		logging.Warn("LLM semantic cache requested but embeddings are non-semantic (TF-IDF) — cache disabled. Set SENTINEL_LLM_EMBEDDING_MODEL to enable.")
	}

	// Pipeline + safety policy.
	// agentsVS is the agents.VectorStore adapter over vectorStore. Held
	// on the wiring struct so applyRuntimeConfig can reuse it when
	// hot-toggling the agent loop.
	agentsVS := &agentsVectorStore{vs: vectorStore}
	w.agentsVS = agentsVS
	pipeline := agents.NewPipeline(router, k8sClient, cfg.GitHubToken, store, agentsVS)
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
	pipeline.SetAllowedRepos(cfg.AllowedGitHubRepos)
	pipeline.SetGitHost(cfg.GitHost)
	pipeline.SetDefaultBranch(cfg.DefaultBranch)
	pipeline.SetLLMPriorWeight(cfg.LLMPriorWeight)
	pipeline.SetMaxIterations(cfg.MaxPipelineIterations)

	// Phase 11: when SENTINEL_USE_AGENT_LOOP=true, swap the pipeline's
	// hypothesis/root-cause phase to the N-turn agent loop. The adapter
	// reuses the same router, K8s client, GitHub token, and vector
	// store as the legacy investigator so there's no duplicate clone
	// rate-limit pressure. Toggable at runtime via /settings
	// (use_agent_loop) — see applyRuntimeConfig in callbacks.go.
	if cfg.UseAgentLoop {
		agentAdapter := agent.NewPipelineAdapter(
			router, k8sClient, cfg.GitHubToken,
			agentsVS,
			agent.AgentConfig{
				MaxTurns:         agent.DefaultMaxTurns,
				TurnTimeout:      time.Duration(cfg.InvestigatorTimeoutSec) * time.Second,
				CompactThreshold: agent.DefaultCompactThreshold,
			},
		)
		agentAdapter.SetAllowedRepos(cfg.AllowedGitHubRepos)
		agentAdapter.SetGitHost(cfg.GitHost)
		agentAdapter.SetDefaultBranch(cfg.DefaultBranch)
		pipeline.SetAgentLoopRunner(agentAdapter)
		logging.Info("agent loop enabled (Phase 11) — pipeline will use N-turn investigator",
			"max_turns", agent.DefaultMaxTurns,
			"turn_timeout_sec", cfg.InvestigatorTimeoutSec)
	}
	w.pipeline = pipeline

	policy := safety.NewPolicy(cfg.ProductionNamespaces, cfg.ConfidenceAutonomous, cfg.ConfidenceHumanReview).
		WithHighRiskServices(cfg.HighRiskServices)
	w.policy = policy

	// L2 safe-actions controller.
	w.safeActions = safeactions.New(k8sClient.Clientset(), safeactions.Config{
		Enabled:           cfg.SafeActionsEnabled,
		AllowedNamespaces: cfg.SafeActionsNamespaces,
		MaxActionsPerHour: cfg.SafeActionsMaxPerHour,
		MaxScaleDeltaPct:  cfg.SafeActionsMaxScaleDeltaPct,
	})
	if cfg.SafeActionsEnabled {
		logging.Info("L2 safe-actions enabled",
			"namespaces", cfg.SafeActionsNamespaces,
			"max_per_hour", cfg.SafeActionsMaxPerHour,
		)
	}

	// --- Phase 2: Executors ---

	ghExec := executors.NewGitHub(cfg.GitHubToken).
		WithAPIURL(cfg.GitHubAPIURL).
		WithGitHost(cfg.GitHost).
		WithDefaultBranch(cfg.DefaultBranch)
	w.ghExec = ghExec

	w.k8sExec = executors.NewK8s(k8sClient.Clientset())

	w.sandbox = executors.NewSandbox(k8sClient.Clientset(), cfg.SandboxNamespacePrefix, cfg.EnableTrafficMirroring).
		WithProbePaths(cfg.SandboxProbePaths).
		WithNetworkPolicyRequired(cfg.SandboxNetworkPolicyRequired).
		WithSentinelNamespace(cfg.Namespace)

	w.kaniko = executors.NewKaniko(k8sClient.Clientset(), cfg.Namespace).
		WithDockerHubUsername(cfg.DockerHubUsername).
		WithRegistry(cfg.KanikoRegistry).
		WithInitImage(cfg.KanikoInitImage).
		WithExecutorImage(cfg.KanikoExecutorImage).
		WithGitHost(cfg.GitHost).
		WithGitHubToken(cfg.GitHubToken)

	if cfg.AutoRemediationEnabled {
		w.remediation = executors.NewRemediationController(k8sClient.Clientset(), cfg.SkipNamespaces)
	}

	// --- Phase 3: Detection + correlation ---

	w.tracker = detection.NewActiveTracker()
	w.alertSender = agents.NewAlertSender(cfg.SlackWebhook)

	w.outcomeWatcher = feedback.NewOutcomeWatcher(k8sClient.Clientset(), store)
	if cfg.OutcomeWatchDurationMin > 0 || cfg.OutcomeCheckIntervalSec > 0 {
		w.outcomeWatcher.SetDurations(
			time.Duration(cfg.OutcomeWatchDurationMin)*time.Minute,
			time.Duration(cfg.OutcomeCheckIntervalSec)*time.Second,
			0, // Phase 2 long watch — keep default 24h
			0, // Phase 2 long interval — keep default 5min
		)
	}

	// Load existing vectors into the HNSW index in the background. Runs
	// after a 2s delay so the SQLite pool is warm.
	go func() {
		defer recoverPanic("vector store loader")
		time.Sleep(2 * time.Second)
		loadCtx, loadCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer loadCancel()
		if err := vectorStore.LoadFromDB(loadCtx, store); err != nil {
			logging.Error("vector store load failed", "error", err)
		}
	}()

	maxConcurrent := cfg.MaxConcurrentInvestigations
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	w.invSem = make(chan struct{}, maxConcurrent)

	// Correlator must be constructed before the clusterer handler because
	// the handler captures it. pkgCausalAnalyzer is set later in Phase 4
	// (Tier 1); early cascades just skip causal attribution.
	w.correlator = correlation.New(func(event correlation.CorrelationEvent) {
		logging.Warn("cascade event detected",
			"services", event.Services,
			"likely_cause", event.LikelyCause,
			"incident_count", event.IncidentCount,
		)
		w.alertSender.SendAlert(
			event.LikelyCause,
			"cascade",
			fmt.Sprintf("Cascade detected: %d services failing. Likely cause: %s", event.IncidentCount, event.LikelyCause),
			"",
		)
		if w.causalAnalyzer != nil {
			cascade := causal.Cascade{
				ID:        uuid.New().String(),
				StartedAt: event.Timestamp,
			}
			for _, svc := range event.Services {
				ns := "default"
				for _, n := range event.Namespaces {
					ns = n
					break
				}
				cascade.Anomalies = append(cascade.Anomalies, causal.Anomaly{
					Node:      causal.Node{Service: svc, Namespace: ns},
					Metric:    "error_rate",
					StartedAt: event.Timestamp,
					Magnitude: 1.0,
				})
			}
			if rc, err := w.causalAnalyzer.Analyze(context.Background(), cascade); err != nil {
				logging.Warn("causal: analysis failed", "error", err)
			} else {
				logging.Info("causal: root cause attributed",
					"node", rc.Node.Service+"/"+rc.Node.Namespace,
					"confidence", rc.Confidence,
					"reasoning", rc.Reasoning,
				)
				if w.attestor != nil {
					_ = w.attestor.Sign(context.Background(), attest.Attestation{
						IncidentID: cascade.ID,
						Action:     "causal.root_cause_attributed",
						Reason:     rc.Reasoning,
						Outcome:    true,
						Timestamp:  time.Now(),
						Evidence: []attest.Evidence{{
							Kind: "causal",
							Payload: map[string]any{
								"root_cause":   rc.Node.Service + "/" + rc.Node.Namespace,
								"confidence":   rc.Confidence,
								"alternatives": rc.AlternativeCandidates,
							},
						}},
					})
				}
			}
		}
	})

	// Clusterer handler: groups signals, dedupes via the tracker, and
	// invokes the investigation orchestrator.
	clustererHandler := func(service, namespace, reason, podLogs string) {
		annotations, _ := k8sClient.LookupWorkloadAnnotations(context.Background(), service, namespace)
		if annotations == nil {
			annotations = map[string]string{}
		}

		// When a cascade is detected (3+ services failing within the
		// correlator window), the correlator emits an event + we skip
		// the individual investigation — the cascade handler will
		// trigger a rollback instead.
		if w.correlator.RegisterIncident(service, namespace, time.Now()) {
			logging.Info("cascade detected — skipping individual investigation",
				"service", service,
				"namespace", namespace,
			)
			return
		}

		explanation := fmt.Sprintf("Auto-detected incident: %s", reason)
		if podLogs != "" {
			explanation = fmt.Sprintf("%s\n\n## Pod Logs (preprocessed)\n%s", explanation, podLogs)
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

		w.alertSender.SendAlert(service, namespace, reason, "")
		go w.handleInvestigation(context.Background(), cluster, "auto-detected", namespace)
	}

	w.clusterer = detection.NewClusterer(clustererHandler, w.tracker)

	detectionHandler := func(service, namespace, reason, _ string) {
		logging.Info("detection signal",
			"namespace", namespace,
			"service", service,
			"reason", reason,
		)
		annotations, _ := k8sClient.LookupWorkloadAnnotations(context.Background(), service, namespace)
		repo := ""
		if annotations != nil {
			repo = annotations["sentinel.io/repo"]
		}
		w.clusterer.Send(context.Background(), detection.Signal{
			Type:      detection.SignalLogError,
			Service:   service,
			Namespace: namespace,
			Reason:    reason,
			Repo:      repo,
			Timestamp: time.Now(),
		})
	}

	w.detector = detection.NewWithConfig(
		k8sClient.Clientset(), detectionHandler, cfg.SlackWebhook,
		time.Duration(cfg.DetectionCooldownSec)*time.Second,
		cfg.DetectionTailLines,
		cfg.SkipNamespaces,
	)

	// Alertmanager webhook receiver — converts firing alerts into
	// detection Signals. w satisfies alertmanager.SignalHandler.
	w.amReceiver = alertmanager.NewReceiver(w).WithSharedSecret(cfg.AlertmanagerWebhookSecret)

	// CRD watcher — invokes the investigation orchestrator on every new
	// SentinelIncident CR.
	w.crdWatcher, err = watcher.New(k8sClient, w.handleInvestigation)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("watcher init: %w", err)
	}

	// runtimecfg.Manager — reads /data/runtime-config.json (or the path
	// in SENTINEL_RUNTIME_CONFIG_PATH). The apply callback is registered
	// at the end of wire() so every component it mutates exists.
	runtimeCfgPath := os.Getenv("SENTINEL_RUNTIME_CONFIG_PATH")
	if runtimeCfgPath == "" {
		runtimeCfgPath = "/data/runtime-config.json"
	}
	w.rtCfg = runtimecfg.New(runtimeCfgPath)

	// --- Phase 4: Tier 1/2 features (all opt-in via env, default off) ---

	// Attest (signed audit trail) — created first because postmerge +
	// chaos depend on it.
	if cfg.AttestEnabled {
		signer, err := attest.NewSigner(cfg.AttestLogDir+"/signer.key", cfg.AttestLogDir)
		if err != nil {
			logging.Error("attest: init failed", "error", err)
		} else {
			w.attestor = signer
			logging.Info("attest: signed audit trail enabled",
				"log_dir", cfg.AttestLogDir,
				"public_key", signer.PublicKeyHex(),
			)
		}
	}

	// Observability (Prometheus + Loki + Tempo clients).
	if cfg.PrometheusURL != "" {
		w.promQuerier = observability.NewPrometheusQuerier(observability.PrometheusConfig{
			URL:     cfg.PrometheusURL,
			Timeout: 10 * time.Second,
		})
		logging.Info("observability: prometheus querier initialized", "url", cfg.PrometheusURL)
	}
	if cfg.LokiURL != "" {
		logging.Info("observability: loki URL configured (client not yet wired — see Tier 2 TODO)",
			"url", cfg.LokiURL)
	}

	// Proactive SLO burn-rate watcher.
	if cfg.SLOWatchEnabled && w.promQuerier != nil {
		sloW := slo.NewWatcher(w.promQuerier)
		sloW.OnIncident = func(inc slo.Incident) {
			w.handleSLOIncident(inc)
		}
		w.sloWatcher = sloW
		logging.Info("slo: proactive burn-rate watcher initialized", "interval", "1m")
	} else if cfg.SLOWatchEnabled && w.promQuerier == nil {
		logging.Warn("slo: watch enabled but PrometheusURL not set — feature disabled")
	}
	if cfg.PostMergeWatchEnabled && w.promQuerier == nil {
		logging.Warn("postmerge: watch enabled but PrometheusURL not set — feature disabled")
	}

	// Causal RCA — subscribes to correlator events.
	if cfg.CausalRCAEnabled {
		w.causalAnalyzer = causal.NewAnalyzer(w)
		logging.Info("causal: RCA enabled (Granger + topological sort) — subscribed to correlator events")
	}

	// Validating admission webhook.
	if cfg.AdmissionWebhookEnabled {
		w.admissionSrv = w.buildAdmissionServer()
	}

	// Cost-aware LLM routing. When enabled, implicitly turn on the
	// semantic cache (if embeddings are available) so trivial/medium
	// prompts hit the cache.
	if cfg.LLMCostRouterEnabled && !cfg.LLMSemanticCacheEnabled && embedder.IsSemantic() {
		cache := llm.NewSemanticCache(embedder, 0.95, 24*time.Hour, 1000)
		router.SetCache(cache)
		logging.Info("llmcost: implicitly enabled LLM semantic cache (0.95 threshold) for cost routing")
	}
	if cfg.LLMCostRouterEnabled {
		logging.Info("llmcost: cost-aware routing enabled (semantic cache + task-hint classification via existing router)")
	}

	// --- Phase 5: API surface (chatops, prreview, automerge, slackbot, api) ---

	// GitHub clients — created before the API server so the webhook
	// handlers can be wired.
	w.chatopsGH = chatops.NewGitHubClient(cfg.GitHubToken, cfg.GitHubAPIURL)
	w.chatopsReactions = chatops.NewReactionsClient(w.chatopsGH)
	w.chatopsStatus = chatops.NewStatusCommentManager(w.chatopsGH)

	w.autoMergeGH = automerge.NewGitHubClient(cfg.GitHubToken, cfg.GitHubAPIURL)
	autoMergeCfg := automerge.Config{
		Enabled:              cfg.AutoMergeEnabled,
		Namespaces:           cfg.AutoMergeNamespaces,
		MinConfidence:        cfg.AutoMergeMinConfidence,
		RequireCIGreen:       cfg.AutoMergeRequireCI,
		CITimeoutSec:         cfg.AutoMergeCITimeoutSec,
		MaxPerHour:           cfg.AutoMergeMaxPerHour,
		CooldownAfterFailMin: cfg.AutoMergeCooldownMin,
		MinSandboxLevel:      cfg.AutoMergeMinSandboxLevel,
	}
	w.autoMergePolicy = automerge.NewPolicy(autoMergeCfg, policy)
	w.autoMergeMerger = automerge.NewMerger(w.autoMergePolicy, w.autoMergeGH, w, w)
	// Tracked-PR persistence: survive restarts. The merger re-adopts
	// in-flight PRs at startup via Reconcile (called from main).
	if w.store != nil {
		w.autoMergeMerger.SetPersistence(w.store)
	}

	w.autoMergeMerger.SetOnMerged(func(repo string, prNumber int) {
		inc := &chatops.Incident{
			AutoMergeEnabled: true,
			AutoMergeMerged:  true,
			Status:           "verified",
		}
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer statusCancel()
		_ = w.chatopsStatus.Update(statusCtx, repo, prNumber, inc, "✅ PR auto-merged. Monitoring deployment...")
	})
	w.autoMergeMerger.SetOnSkipped(func(repo string, prNumber int, reason string) {
		inc := &chatops.Incident{
			AutoMergeEnabled: true,
			AutoMergeSkipped: reason,
			Status:           "verified",
		}
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer statusCancel()
		_ = w.chatopsStatus.Update(statusCtx, repo, prNumber, inc, "")
	})

	// Post-merge regression watcher — constructed after autoMergeGH so it
	// can use the same GitHub client as the reverter.
	if cfg.PostMergeWatchEnabled && w.promQuerier != nil {
		w.postMerge = postmerge.NewWatcher(w.promQuerier, w.autoMergeGH)
		if w.attestor != nil {
			// Attestor is wired via the OnRollback callback — the
			// postmerge watcher has no direct attestor field in v1.
			w.postMerge.OnRollback = func(ev postmerge.RollbackEvent) {
				_ = w.attestor.Sign(context.Background(), attest.Attestation{
					IncidentID: ev.IncidentID,
					Action:     "postmerge.rollback",
					Reason:     ev.Reason,
					Outcome:    ev.AutoRolledBack,
					Timestamp:  ev.DetectedAt,
					Evidence: []attest.Evidence{{
						Kind: "postmerge",
						Payload: map[string]any{
							"repo":             ev.Repo,
							"pr":               ev.PRNumber,
							"merge_commit":     ev.MergeCommit,
							"auto_rolled_back": ev.AutoRolledBack,
						},
					}},
				})
			}
		}
		logging.Info("postmerge: regression watcher initialized",
			"phase1", "10m",
			"phase2", "24h",
		)
	}

	prReviewer := prreview.New(cfg.GitHubToken, cfg.GitHubWebhookSecret, &prreviewVectorStore{vs: vectorStore}).
		WithAPIURL(cfg.GitHubAPIURL)
	w.prReviewer = prReviewer

	// Self-chaos runner. The trigger callback constructs a synthetic
	// AnomalyCluster from the chaos incident and feeds it into the
	// investigation orchestrator.
	if cfg.ChaosEnabled && len(cfg.ChaosNamespaces) > 0 {
		trigger := func(ctx context.Context, exp chaos.Experiment, namespace string) chaos.Outcome {
			return w.runChaosInvestigation(ctx, exp, namespace)
		}
		w.chaosRunner = chaos.New(w, cfg, trigger)
		w.chaosRunner.SetAttestor(w.attestor)
		logging.Info("chaos: runner initialized",
			"interval_min", cfg.ChaosIntervalMin,
			"namespaces", cfg.ChaosNamespaces,
		)
	} else if cfg.ChaosEnabled {
		logging.Warn("chaos: enabled but no namespaces configured — feature disabled")
	}

	// Slack bot — the investigate callback is what /sentinel investigate
	// from Slack invokes.
	w.slackBot = slackbot.New(cfg.SlackSigningSecret, cfg.SlackBotUserID,
		func(service, namespace, reason string) (string, error) {
			cluster := schemas.AnomalyCluster{
				ClusterID: uuid.New(),
				Service:   service,
				Namespace: namespace,
				Severity:  schemas.SeverityWarning,
				Summary:   fmt.Sprintf("Slack-triggered: %s", reason),
			}
			go w.handleInvestigation(context.Background(), cluster, "slack", cfg.Namespace)
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
	if cfg.SlackBotToken != "" {
		w.slackBot.SetBotToken(cfg.SlackBotToken)
	}

	// API server. The investigate func is what POST /admin/investigate
	// invokes.
	apiServer := api.New(
		listenAddr,
		store,
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
			go w.handleInvestigation(context.Background(), cluster, "manual", cfg.Namespace)
			return cluster.ClusterID.String(), nil
		},
		func() bool { return w.ready.Load() },
	)
	apiServer.SetAPIToken(cfg.APIToken)
	apiServer.SetWebUIHandler(web.Handler())
	apiServer.SetWebhookHandler(http.HandlerFunc(prReviewer.HandleWebhook))
	apiServer.SetAlertmanagerHandler(w.amReceiver)
	apiServer.SetChatopsHandler(chatops.NewWebhookHandler(w.buildChatopsDeps(), cfg.GitHubWebhookSecret))
	apiServer.SetProvidersStatusGetter(w)
	apiServer.SetAutoMergeStatusGetter(w)
	apiServer.SetGitHubAppManager(w.rtCfg)
	apiServer.SetSlackHandler(http.HandlerFunc(w.slackBot.HandleEvents))
	apiServer.SetPauseController(w.slackBot)

	apiServer.SetInvestigateWithLogs(func(service, namespace, reason, repo, podLogs string) (string, error) {
		annotations := map[string]string{}
		if repo != "" {
			annotations["sentinel.io/repo"] = repo
			annotations["sentinel.io/branch"] = cfg.DefaultBranch
		}
		cluster := schemas.AnomalyCluster{
			ClusterID:   uuid.New(),
			Service:     service,
			Namespace:   namespace,
			Severity:    schemas.SeverityWarning,
			Summary:     fmt.Sprintf("Manual investigation: %s", reason),
			Annotations: annotations,
			Anomalies: []schemas.Anomaly{
				{
					AnomalyID:   uuid.New(),
					SignalID:    uuid.New(),
					Service:     service,
					Namespace:   namespace,
					Detector:    "manual",
					Score:       1.0,
					Explanation: podLogs,
				},
			},
		}
		go w.handleInvestigation(context.Background(), cluster, "manual", cfg.Namespace)
		return cluster.ClusterID.String(), nil
	})
	apiServer.SetRuntimeCfgSaver(w.rtCfg)
	w.apiServer = apiServer
	w.broadcaster = apiServer

	// Wire the clusterer's pause checker to the Slack bot so /sentinel
	// pause via Slack suppresses new triggers.
	w.clusterer.SetPauseChecker(w.slackBot)

	// Optional GitHub App — when configured, all GitHub operations
	// appear as "Sentinel[bot]" instead of the PAT owner.
	w.configureGitHubApp()

	// Register the runtimecfg apply callback LAST so every component it
	// mutates has been constructed.
	w.rtCfg.SetApplyCallback(w.applyRuntimeConfig)

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

	return w, nil
}

// run spawns every long-running goroutine, starts the API server, and
// blocks until SIGTERM/SIGINT. On signal, it stops the clusterer +
// detector + Tier 1/2 features, cancels the root context, waits for
// in-flight investigations (15s deadline), then shuts down the API
// server (10s deadline) and closes the SQLite store.

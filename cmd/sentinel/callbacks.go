// Package main — runtime callbacks + helper constructors.
//
// callbacks.go groups the *wiring methods that are not adapter methods
// (those live in wiring.go) and not the investigation orchestrator (that
// lives in investigation.go). The methods here are:
//
//   - buildChatopsDeps     — assembles the chatops.Deps bag.
//   - buildAdmissionServer — assembles the validating admission webhook.
//   - configureGitHubApp   — wires a GitHub App into every GitHub client.
//   - applyRuntimeConfig   — the runtimecfg.Manager apply callback.
//   - handleSLOIncident    — the SLO watcher OnIncident callback.
//   - runChaosInvestigation — the chaos.Runner trigger callback.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/admission"
	"github.com/karimzakzouk/sentinel/internal/agent"
	"github.com/karimzakzouk/sentinel/internal/attest"
	"github.com/karimzakzouk/sentinel/internal/chaos"
	"github.com/karimzakzouk/sentinel/internal/chatops"
	"github.com/karimzakzouk/sentinel/internal/config"
	githubapp "github.com/karimzakzouk/sentinel/internal/github"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/runtimecfg"
	"github.com/karimzakzouk/sentinel/internal/schemas"
	"github.com/karimzakzouk/sentinel/internal/slo"
)

// buildChatopsDeps constructs the chatops.Deps bag from the wired
// components. *wiring satisfies Store, AutoMergeClient, PipelineRunner,
// and SandboxRunner; the rest are concrete pointers.
func (w *wiring) buildChatopsDeps() *chatops.Deps {
	return &chatops.Deps{
		Store:           w,
		GitHub:          w.chatopsGH,
		Router:          w.router,
		Reactions:       w.chatopsReactions,
		StatusComment:   w.chatopsStatus,
		AutoMerge:       w,
		Rollback:        w.k8sExec,
		Pipeline:        w,
		Sandbox:         w,
		PauseController: w.slackBot,
	}
}

// buildAdmissionServer constructs the validating admission webhook HTTP
// server. *wiring satisfies admission.IncidentMemory. The CVEScanner is
// pluggable: "trivy", "grype", or "noop" (default).
func (w *wiring) buildAdmissionServer() *http.Server {
	cfg := w.Config()
	var scanner admission.CVEScanner
	switch strings.ToLower(cfg.CVEScannerType) {
	case "trivy":
		scanner = admission.NewTrivyScanner(cfg.CVEScannerBinary, time.Duration(cfg.CVEScannerCacheTTL)*time.Second)
		logging.Info("admission: CVEScanner wired (trivy)", "binary", cfg.CVEScannerBinary, "cache_ttl", cfg.CVEScannerCacheTTL)
	case "grype":
		scanner = admission.NewGrypeScanner(cfg.CVEScannerBinary, time.Duration(cfg.CVEScannerCacheTTL)*time.Second)
		logging.Info("admission: CVEScanner wired (grype)", "binary", cfg.CVEScannerBinary, "cache_ttl", cfg.CVEScannerCacheTTL)
	default:
		scanner = nil // noop — BlockKnownBadImagesPolicy returns "scanner not configured" warning
		logging.Info("admission: CVEScanner disabled (noop) — set SENTINEL_CVE_SCANNER=trivy to enable")
	}

	admHandler := admission.NewHandler(w, scanner)
	mux := http.NewServeMux()
	mux.HandleFunc("/validate", func(rw http.ResponseWriter, r *http.Request) {
		var review admissionv1.AdmissionReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			logging.Error("admission: failed to decode request", "error", err)
			http.Error(rw, "invalid AdmissionReview", http.StatusBadRequest)
			return
		}
		resp := admHandler.Handle(r.Context(), &review)
		review.Response = resp
		rw.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(rw).Encode(&review); err != nil {
			logging.Error("admission: failed to encode response", "error", err)
		}
	})
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	return &http.Server{
		Addr:    cfg.AdmissionWebhookAddr,
		Handler: mux,
	}
}

// configureGitHubApp wires a GitHub App (if configured) into every GitHub
// client so all operations appear as "Sentinel[bot]". *github.App
// satisfies chatops.TokenProvider and automerge.TokenProvider (they're
// aliases of github.TokenProvider), so no adapter is needed.
func (w *wiring) configureGitHubApp() {
	cfg := w.Config()
	rtConfig := w.rtCfg.Get()
	appID := rtConfig.GitHubAppID
	appKey := rtConfig.GitHubAppPrivateKey
	appInstall := rtConfig.GitHubAppInstallID
	if appID == "" {
		appID = cfg.GitHubAppID
		appKey = cfg.GitHubAppPrivateKey
		appInstall = cfg.GitHubAppInstallID
	}
	if appID == "" || appKey == "" || appInstall == "" {
		logging.Info("GitHub App not configured — PRs + comments appear as the PAT owner",
			"hint", "set SENTINEL_GITHUB_APP_ID + SENTINEL_GITHUB_APP_PRIVATE_KEY + SENTINEL_GITHUB_APP_INSTALL_ID for verified bot identity",
		)
		return
	}
	app, err := githubapp.NewWithAPIURL(appID, appKey, appInstall, cfg.GitHubAPIURL)
	if err != nil {
		logging.Error("GitHub App init failed", "error", err)
		return
	}
	w.prReviewer.WithGitHubApp(app)
	w.ghExec.WithTokenProvider(app)
	w.chatopsGH.WithTokenProvider(app)
	w.autoMergeGH.WithTokenProvider(app)
	logging.Info("GitHub App configured — ALL GitHub operations appear as Sentinel[bot]",
		"app_id", appID,
		"install_id", appInstall,
		"api_url", cfg.GitHubAPIURL,
	)
}

// applyRuntimeConfig is the runtimecfg.Manager apply callback. It runs
// under w.mu's write lock and propagates settings into the live LLM
// router, config, and (when GitHub App credentials change) the GitHub
// clients. Called synchronously from rtCfg.Save — keep it fast.
func (w *wiring) applyRuntimeConfig(rc *runtimecfg.RuntimeConfig) {
	// Reconfigure the LLM router.
	baseURL := rc.LLMBaseURL
	if baseURL == "" && rc.LLMProvider != "" && rc.LLMProvider != w.Config().LLMProvider {
		logging.Warn("LLM provider changed via runtime config but no base URL set — keeping current base URL. Set llm_base_url in settings.",
			"old_provider", w.Config().LLMProvider,
			"new_provider", rc.LLMProvider,
		)
	}
	apiKey := rc.LLMAPIKey
	if apiKey == "" {
		apiKey = w.Config().LLMAPIKey
	}
	fastModel := rc.LLMFastModel
	if fastModel == "" {
		fastModel = w.Config().LLMFastModel
	}
	frontierModel := rc.LLMFrontierModel
	if frontierModel == "" {
		frontierModel = w.Config().LLMFrontierModel
	}
	if baseURL != "" {
		w.router.Update(baseURL, apiKey, fastModel, frontierModel)
	} else {
		curBase, _, _, _ := w.router.Snapshot()
		w.router.Update(curBase, apiKey, fastModel, frontierModel)
	}
	logging.Info("LLM router reconfigured from runtime settings",
		"fast_model", fastModel,
		"frontier_model", frontierModel,
		"api_key_set", apiKey != "",
		"base_url_changed", baseURL != "",
	)

	// Reconfigure mutable cfg fields under the write lock.
	w.mutateConfig(func(cfg *config.Config) {
		if rc.SandboxEnabled != nil {
			cfg.SandboxEnabled = *rc.SandboxEnabled
		}
		if rc.SandboxRequired != nil {
			cfg.SandboxRequired = *rc.SandboxRequired
		}
		if rc.SandboxMaxRetries > 0 {
			cfg.SandboxMaxRetries = rc.SandboxMaxRetries
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
		if rc.UseAgentLoop != nil {
			cfg.UseAgentLoop = *rc.UseAgentLoop
		}
	})

	// Phase 11: hot-toggle the agent loop. Construct or destroy the
	// PipelineAdapter in place — no restart needed. The adapter reuses
	// the same router, K8s client, GitHub token, and vector store that
	// wire.go already constructed, so we don't need to re-resolve them.
	if rc.UseAgentLoop != nil {
		want := *rc.UseAgentLoop
		cfgSnapshot := w.Config()
		if want && !w.pipeline.AgentLoopEnabled() {
			adapter := agent.NewPipelineAdapter(
				w.router, w.k8sClient, cfgSnapshot.GitHubToken,
				w.agentsVS,
				agent.AgentConfig{
					MaxTurns:         agent.DefaultMaxTurns,
					TurnTimeout:      time.Duration(cfgSnapshot.InvestigatorTimeoutSec) * time.Second,
					CompactThreshold: agent.DefaultCompactThreshold,
				},
			)
			adapter.SetAllowedRepos(cfgSnapshot.AllowedGitHubRepos)
			adapter.SetGitHost(cfgSnapshot.GitHost)
			adapter.SetDefaultBranch(cfgSnapshot.DefaultBranch)
			w.pipeline.SetAgentLoopRunner(adapter)
			logging.Info("agent loop enabled at runtime — pipeline will use N-turn investigator",
				"max_turns", agent.DefaultMaxTurns)
		} else if !want && w.pipeline.AgentLoopEnabled() {
			w.pipeline.SetAgentLoopRunner(nil)
			logging.Info("agent loop disabled at runtime — pipeline reverted to legacy investigator")
		}
	}

	if rc.LogLevel != "" {
		logging.Init(rc.LogLevel)
	}

	// Reconfigure GitHub App credentials when provided. *github.App
	// satisfies chatops.TokenProvider + automerge.TokenProvider directly,
	// so no adapter is needed.
	if rc.GitHubAppID != "" && rc.GitHubAppPrivateKey != "" && rc.GitHubAppInstallID != "" {
		cfgSnapshot := w.Config()
		app, err := githubapp.NewWithAPIURL(rc.GitHubAppID, rc.GitHubAppPrivateKey, rc.GitHubAppInstallID, cfgSnapshot.GitHubAPIURL)
		if err != nil {
			logging.Error("GitHub App reconfiguration failed", "error", err)
			return
		}
		w.ghExec.WithTokenProvider(app)
		w.chatopsGH.WithTokenProvider(app)
		w.autoMergeGH.WithTokenProvider(app)
		w.prReviewer.WithGitHubApp(app)
		logging.Info("GitHub App reconfigured via runtime config",
			"app_id", rc.GitHubAppID,
			"install_id", rc.GitHubAppInstallID,
		)
	}
}

// handleSLOIncident converts an SLO burn-rate incident into an
// AnomalyCluster and feeds it into the investigation orchestrator.
func (w *wiring) handleSLOIncident(inc slo.Incident) {
	logging.Warn("slo: burn-rate incident fired — feeding into investigation pipeline",
		"service", inc.Service,
		"namespace", inc.Namespace,
		"rule", inc.RuleName,
		"severity", inc.Severity,
		"long_burn", inc.LongBurn,
		"short_burn", inc.ShortBurn,
	)
	severity := schemas.SeverityWarning
	if inc.Severity == "page" {
		severity = schemas.SeverityCritical
	}
	cluster := schemas.AnomalyCluster{
		ClusterID: uuid.New(),
		Service:   inc.Service,
		Namespace: inc.Namespace,
		Anomalies: []schemas.Anomaly{
			{
				AnomalyID:   uuid.New(),
				SignalID:    uuid.New(),
				Service:     inc.Service,
				Namespace:   inc.Namespace,
				Detector:    "slo_burn_rate",
				Score:       inc.LongBurn / inc.Threshold,
				Dimensions:  map[string]float64{"long_burn": inc.LongBurn, "short_burn": inc.ShortBurn, "threshold": inc.Threshold},
				Explanation: inc.Reason,
				Annotations: map[string]string{
					"sentinel.io/detector": "slo",
					"sentinel.io/rule":     inc.RuleName,
					"sentinel.io/slo":      fmt.Sprintf("%.4f", inc.SLOTarget),
				},
			},
		},
		Summary:  fmt.Sprintf("SLO burn rate %.2fx (rule: %s)", inc.LongBurn, inc.RuleName),
		Severity: severity,
		Annotations: map[string]string{
			"sentinel.io/detector": "slo",
			"sentinel.io/rule":     inc.RuleName,
		},
	}
	go w.handleInvestigation(context.Background(), cluster, "slo", inc.Namespace)
}

// runChaosInvestigation is the chaos.Runner trigger callback. It
// constructs a synthetic AnomalyCluster from the chaos experiment,
// attests the injection, and runs the investigation orchestrator with a
// timeout. Returns the chaos.Outcome.
func (w *wiring) runChaosInvestigation(ctx context.Context, exp chaos.Experiment, namespace string) chaos.Outcome {
	outcome := chaos.Outcome{Experiment: exp.Name(), Namespace: namespace, InjectedAt: time.Now().UTC()}

	// Pick a service to attribute the synthetic incident to. The chaos
	// runner doesn't know which service lives in the namespace, so we
	// list deployments and pick the first.
	deps, err := w.ListDeployments(ctx, namespace)
	if err != nil || len(deps) == 0 {
		outcome.Notes = fmt.Sprintf("could not pick a service in %s: %v", namespace, err)
		return outcome
	}
	service := deps[0]

	detectedAt := time.Now()
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
				Detector:    "chaos",
				Score:       1.0,
				Dimensions:  map[string]float64{"chaos": 1.0},
				Explanation: fmt.Sprintf("Chaos experiment: %s", exp.Name()),
				Annotations: map[string]string{
					"sentinel.io/chaos":      "true",
					"sentinel.io/what-broke": exp.Name(),
				},
			},
		},
		Summary:     fmt.Sprintf("Chaos experiment: %s against %s", exp.Name(), namespace),
		Severity:    schemas.SeverityWarning,
		Annotations: map[string]string{"sentinel.io/chaos": "true"},
	}

	if w.attestor != nil {
		_ = w.attestor.Sign(ctx, attest.Attestation{
			IncidentID: cluster.ClusterID.String(),
			Action:     "chaos.investigation_triggered",
			Reason:     fmt.Sprintf("chaos experiment %s against %s", exp.Name(), namespace),
			Timestamp:  detectedAt,
			Evidence: []attest.Evidence{{
				Kind: "chaos",
				Payload: map[string]any{
					"service":    service,
					"namespace":  namespace,
					"experiment": exp.Name(),
				},
			}},
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverPanic("chaos investigation")
		w.handleInvestigation(ctx, cluster, "chaos", namespace)
	}()

	select {
	case <-done:
		outcome.Detected = true
		outcome.DetectedAt = detectedAt
		outcome.TimeToDetect = time.Since(detectedAt)
		outcome.Notes = "investigation pipeline triggered"
	case <-time.After(10 * time.Minute):
		outcome.Detected = true
		outcome.DetectedAt = detectedAt
		outcome.Notes = "investigation pipeline triggered but did not complete within 10m timeout"
	}
	return outcome
}

// Package config loads Sentinel's runtime configuration from environment
// variables. The Config struct is immutable at startup; runtime-mutable
// fields live in RuntimeSettings (hot-reloaded by the runtimecfg package).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// Config holds all startup configuration. Fields are populated by Load from
// environment variables; defaults are documented on each field's env var.
type Config struct {
	// ─── Storage ──────────────────────────────────────────────────────────
	SQLitePath string

	// ─── LLM (single provider; use LLMProviders for the failover pool) ────
	LLMProvider      string
	LLMBaseURL       string
	LLMAPIKey        string
	LLMFastModel     string
	LLMFrontierModel string

	// Embedding pipeline. Auto-detects: if EmbeddingAPIKey is set, uses the
	// remote API; otherwise falls back to local TF-IDF hash embeddings.
	LLMEmbeddingModel string
	EmbeddingProvider string
	EmbeddingAPIKey   string
	EmbeddingBaseURL  string
	LLMEmbeddingDim   int

	// Semantic cache. When enabled and embeddings are configured, LLM
	// responses are cached by prompt similarity (cosine >= threshold).
	LLMSemanticCacheEnabled   bool
	LLMSemanticCacheThreshold float64

	// ─── L2 safe actions (restart, rollback, scale) ──────────────────────
	SafeActionsEnabled          bool
	SafeActionsNamespaces       []string
	SafeActionsMaxPerHour       int
	SafeActionsMaxScaleDeltaPct int

	// ─── Fix verification ────────────────────────────────────────────────
	FixVerifyEnabled bool

	// ─── Kubernetes ──────────────────────────────────────────────────────
	Namespace   string
	ReleaseName string

	// ─── GitHub ──────────────────────────────────────────────────────────
	GitHubToken               string
	GitHubWebhookSecret       string
	AlertmanagerWebhookSecret string
	AllowedGitHubRepos        []string

	// GitHub App (optional). When configured, the App's installation token
	// is used for all GitHub API calls; GitHubToken is the fallback.
	GitHubAppID         string
	GitHubAppPrivateKey string
	GitHubAppInstallID  string

	// ─── Container registry (Kaniko) ─────────────────────────────────────
	DockerHubUsername string
	DockerHubToken    string
	KanikoRegistry    string

	// ─── Git host (GitHub Enterprise / GitLab GitHub-compat API) ─────────
	GitHost      string
	GitHubAPIURL string

	// DefaultBranch is the fallback when the sentinel.io/branch annotation
	// is absent. Defaults to "main".
	DefaultBranch string

	// ─── Namespace policy ────────────────────────────────────────────────
	HighRiskServices     []string
	SkipNamespaces       []string
	ProductionNamespaces []string
	WriteNamespaces      []string

	// ─── Kaniko images ───────────────────────────────────────────────────
	KanikoInitImage     string
	KanikoExecutorImage string

	// ─── Concurrency ─────────────────────────────────────────────────────
	MaxConcurrentInvestigations int

	// ─── Confidence thresholds ───────────────────────────────────────────
	ConfidenceAutonomous        float64
	ConfidenceHumanReview       float64
	ConfidenceLoopBackThreshold float64

	// ─── Panic-button heuristics ─────────────────────────────────────────
	PanicCPUThresholdPct      float64
	PanicConnectionsThreshold int

	// ─── Sandbox ─────────────────────────────────────────────────────────
	SandboxNamespacePrefix string
	EnableTrafficMirroring bool
	SandboxProbePaths      []string
	SkipSandboxNonProd     bool

	// SandboxEnabled: run the sandbox at all.
	// SandboxRequired: when sandbox fails, block the PR (true) or open with
	//   needs-review (false, default).
	// SandboxMaxRetries: LLM retries on sandbox failure.
	SandboxEnabled    bool
	SandboxRequired   bool
	SandboxMaxRetries int

	// SandboxNetworkPolicyRequired fails the sandbox run when the
	// default-deny NetworkPolicy cannot be created (fail-closed). The
	// policy is the last line of the prompt-injection defense, so a
	// canary with unrestricted egress must never run. Operators with
	// restricted clusters can set it to false to restore fail-open.
	SandboxNetworkPolicyRequired bool

	// ─── API ─────────────────────────────────────────────────────────────
	APIToken string
	APIHost  string
	APIPort  int

	// ─── Timeouts (seconds) ──────────────────────────────────────────────
	InvestigationTimeout       int
	InvestigatorTimeoutSec     int
	EvidenceGathererTimeoutSec int
	AnalystTimeoutSec          int
	FixProposerTimeoutSec      int
	MaxPipelineIterations      int
	OutcomeWatchDurationMin    int
	OutcomeCheckIntervalSec    int
	DetectionCooldownSec       int

	// ─── Agent loop (Phase 11) ───────────────────────────────────────────
	// UseAgentLoop, when true, swaps the pipeline's hypothesis/root-cause
	// phase from the legacy 2-round investigator to the N-turn agent loop
	// (internal/agent). Default false — the agent loop is opt-in until it
	// has been validated against real incidents. The runtime config
	// equivalent (runtimecfg.UseAgentLoop) hot-toggles this without a
	// restart via Pipeline.SetAgentLoopRunner.
	UseAgentLoop       bool
	DetectionTailLines int64

	ScaleFixDefaultReplicas int32
	LLMPriorWeight          float64

	LLMMaxRetries       int
	LLMRetryBaseDelayMs int

	// ─── Multi-provider LLM pool ─────────────────────────────────────────
	LLMProviders []schemas.ProviderConfig

	// ─── Auto-merge (11-gate) ────────────────────────────────────────────
	AutoMergeEnabled         bool
	AutoMergeNamespaces      []string
	AutoMergeMinConfidence   float64
	AutoMergeRequireCI       bool
	AutoMergeCITimeoutSec    int
	AutoMergeMaxPerHour      int
	AutoMergeMinSandboxLevel int
	AutoMergeCooldownMin     int

	AutoRemediationEnabled bool

	// ─── Observability backends ──────────────────────────────────────────
	PrometheusURL string
	LokiURL       string
	JaegerURL     string
	TempoURL      string

	// ─── Tier 1: post-merge regression watcher ───────────────────────────
	PostMergeWatchEnabled bool

	// ─── Tier 1: proactive SLO burn-rate watcher ─────────────────────────
	SLOWatchEnabled bool

	// ─── Tier 1: causal root-cause attribution ───────────────────────────
	CausalRCAEnabled bool

	// ─── Tier 1: validating admission webhook ────────────────────────────
	AdmissionWebhookEnabled bool
	AdmissionWebhookAddr    string
	AdmissionWebhookCertDir string

	// CVE scanner for the admission webhook. Values: "trivy", "grype",
	// "noop" (default). When "trivy" or "grype", the binary must be in
	// PATH (sidecar or in-process).
	CVEScannerType     string
	CVEScannerBinary   string // path to binary (default "trivy" or "grype")
	CVEScannerCacheTTL int    // seconds (default 1800 = 30min)

	// ─── Tier 2: Ed25519-signed audit trail ──────────────────────────────
	AttestEnabled bool
	AttestLogDir  string

	// ─── Tier 2: cost-aware LLM router + semantic cache ──────────────────
	LLMCostRouterEnabled bool

	// ─── Tier 2: self-chaos against staging ──────────────────────────────
	ChaosEnabled     bool
	ChaosNamespaces  []string
	ChaosIntervalMin int

	// ─── Slack ───────────────────────────────────────────────────────────
	SlackWebhook       string
	SlackSigningSecret string
	SlackBotUserID     string
	SlackBotToken      string

	// ─── Logging ─────────────────────────────────────────────────────────
	LogLevel string

	// ─── Observe-only mode ─────────────────────────────────────────────
	// ObserveOnly forces Sentinel into a read-only mode: it serves the API
	// and web UI but does NOT call the LLM, clone repos, open PRs, or execute
	// any fixes. Useful for demos, evaluation, and "dry-run" deployments.
	// When true, overrides all capability checks (LLM, GitHub, K8s).
	// When false (default), Sentinel auto-degrades: if LLM_API_KEY is empty
	// it uses a noop LLM (logs "would have called"); if GITHUB_TOKEN is empty
	// it skips the PR step; if no K8s cluster is reachable it serves the API
	// only.
	ObserveOnly bool
}

// RuntimeSettings is the runtime-mutable subset of Config. These can be
// changed via the API / CLI without restarting Sentinel.
type RuntimeSettings struct {
	LLMProvider               string  `json:"llm_provider"`
	LLMAPIKey                 string  `json:"llm_api_key"`
	LLMFastModel              string  `json:"llm_fast_model"`
	LLMFrontierModel          string  `json:"llm_frontier_model"`
	GitHubToken               string  `json:"github_token"`
	PrometheusURL             string  `json:"prometheus_url"`
	LokiURL                   string  `json:"loki_url"`
	JaegerURL                 string  `json:"jaeger_url"`
	ProductionNamespaces      string  `json:"production_namespaces"`
	WriteNamespaces           string  `json:"write_namespaces"`
	ConfidenceAutonomous      float64 `json:"confidence_threshold_autonomous"`
	ConfidenceHumanReview     float64 `json:"confidence_threshold_human_review"`
	PanicCPUThresholdPct      float64 `json:"panic_cpu_threshold_pct"`
	PanicConnectionsThreshold int     `json:"panic_connections_threshold"`
}

// Load reads configuration from environment variables.
func Load() *Config {
	return &Config{
		SQLitePath:       getenv("SENTINEL_SQLITE_PATH", "/data/sentinel.db"),
		LLMProvider:      getenv("SENTINEL_LLM_PROVIDER", "zai"),
		LLMBaseURL:       getenv("SENTINEL_LLM_BASE_URL", ""),
		LLMAPIKey:        getenv("SENTINEL_LLM_API_KEY", ""),
		LLMFastModel:     getenv("SENTINEL_LLM_FAST_MODEL", "glm-4-flash"),
		LLMFrontierModel: getenv("SENTINEL_LLM_FRONTIER_MODEL", "glm-4.5"),

		LLMEmbeddingModel: getenv("SENTINEL_LLM_EMBEDDING_MODEL", ""),
		EmbeddingProvider: getenv("SENTINEL_EMBEDDING_PROVIDER", ""),
		EmbeddingAPIKey:   getenv("SENTINEL_EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:  getenv("SENTINEL_EMBEDDING_BASE_URL", ""),

		LLMSemanticCacheEnabled:   getenvBool("SENTINEL_LLM_SEMANTIC_CACHE", false),
		LLMSemanticCacheThreshold: getenvFloat("SENTINEL_LLM_CACHE_THRESHOLD", 0.97),
		LLMEmbeddingDim:           getenvInt("SENTINEL_LLM_EMBEDDING_DIM", 1536),

		SafeActionsEnabled:          getenvBool("SENTINEL_SAFE_ACTIONS_ENABLED", false),
		SafeActionsNamespaces:       getenvCsv("SENTINEL_SAFE_ACTIONS_NAMESPACES"),
		SafeActionsMaxPerHour:       getenvInt("SENTINEL_SAFE_ACTIONS_MAX_PER_HOUR", 3),
		SafeActionsMaxScaleDeltaPct: getenvInt("SENTINEL_SAFE_ACTIONS_MAX_SCALE_DELTA_PCT", 50),

		FixVerifyEnabled: getenvBool("SENTINEL_FIX_VERIFY", true),

		Namespace:   getenv("SENTINEL_NAMESPACE", "sentinel"),
		ReleaseName: getenv("SENTINEL_RELEASE_NAME", "sentinel"),

		GitHubToken:               getenv("SENTINEL_GITHUB_TOKEN", ""),
		GitHubWebhookSecret:       getenv("SENTINEL_GITHUB_WEBHOOK_SECRET", ""),
		AlertmanagerWebhookSecret: getenv("SENTINEL_ALERTMANAGER_WEBHOOK_SECRET", ""),
		AllowedGitHubRepos:        splitCSV(getenv("SENTINEL_ALLOWED_GITHUB_REPOS", "")),
		APIToken:                  getenv("SENTINEL_API_TOKEN", ""),

		GitHubAppID:         getenv("SENTINEL_GITHUB_APP_ID", ""),
		GitHubAppPrivateKey: getenv("SENTINEL_GITHUB_APP_PRIVATE_KEY", ""),
		GitHubAppInstallID:  getenv("SENTINEL_GITHUB_APP_INSTALL_ID", ""),

		DockerHubUsername: getenv("SENTINEL_DOCKER_HUB_USERNAME", ""),
		DockerHubToken:    getenv("SENTINEL_DOCKER_HUB_TOKEN", ""),

		KanikoRegistry: getenv("SENTINEL_KANIKO_REGISTRY", "docker.io"),

		GitHost:      getenv("SENTINEL_GIT_HOST", "github.com"),
		GitHubAPIURL: getenv("SENTINEL_GITHUB_API_URL", "https://api.github.com"),

		DefaultBranch: getenv("SENTINEL_DEFAULT_BRANCH", "main"),

		HighRiskServices: splitCSV(getenv("SENTINEL_HIGH_RISK_SERVICES", "")),

		SkipNamespaces: splitCSV(getenv("SENTINEL_SKIP_NAMESPACES",
			"kube-system,kube-public,kube-node-lease,calico-system,calico-apiserver,cert-manager,ingress-nginx,monitoring,prometheus,grafana,loki,metallb-system,longhorn-system,cattle-system,fleet-system,gatekeeper-system,external-secrets,vault,argo-events,argo-rollouts,tekton-pipelines,istio-system,knative-serving,kubernetes-dashboard")),

		KanikoInitImage:     getenv("SENTINEL_KANIKO_INIT_IMAGE", "alpine/git:2.43.0"),
		KanikoExecutorImage: getenv("SENTINEL_KANIKO_EXECUTOR_IMAGE", "gcr.io/kaniko-project/executor:v1.24.0"),

		MaxConcurrentInvestigations: getenvInt("SENTINEL_MAX_CONCURRENT_INVESTIGATIONS", 3),

		ProductionNamespaces: splitCSV(getenv("SENTINEL_PRODUCTION_NAMESPACES", "prod,production")),
		WriteNamespaces:      splitCSV(getenv("SENTINEL_WRITE_NAMESPACES", "")),

		ConfidenceAutonomous:        getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS", 0.92),
		ConfidenceHumanReview:       getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW", 0.70),
		ConfidenceLoopBackThreshold: getenvFloat("SENTINEL_CONFIDENCE_LOOP_BACK", 0.3),

		PanicCPUThresholdPct:      getenvFloat("SENTINEL_PANIC_CPU_THRESHOLD_PCT", 95.0),
		PanicConnectionsThreshold: getenvInt("SENTINEL_PANIC_CONNECTIONS_THRESHOLD", 9000),

		EnableTrafficMirroring: getenvBool("SENTINEL_ENABLE_TRAFFIC_MIRRORING", true),
		SandboxNamespacePrefix: getenv("SENTINEL_SANDBOX_NAMESPACE_PREFIX", "sentinel-sandbox"),

		APIHost: getenv("SENTINEL_API_HOST", "0.0.0.0"),
		APIPort: getenvInt("PORT", 8000),

		InvestigationTimeout:       getenvInt("SENTINEL_INVESTIGATION_TIMEOUT", 600),
		InvestigatorTimeoutSec:     getenvInt("SENTINEL_INVESTIGATOR_TIMEOUT", 120),
		EvidenceGathererTimeoutSec: getenvInt("SENTINEL_EVIDENCE_GATHERER_TIMEOUT", 90),
		AnalystTimeoutSec:          getenvInt("SENTINEL_ANALYST_TIMEOUT", 15),
		FixProposerTimeoutSec:      getenvInt("SENTINEL_FIX_PROPOSER_TIMEOUT", 300),
		MaxPipelineIterations:      getenvInt("SENTINEL_MAX_PIPELINE_ITERATIONS", 5),

		UseAgentLoop: getenvBool("SENTINEL_USE_AGENT_LOOP", false),

		OutcomeWatchDurationMin: getenvInt("SENTINEL_OUTCOME_WATCH_DURATION_MIN", 10),
		OutcomeCheckIntervalSec: getenvInt("SENTINEL_OUTCOME_CHECK_INTERVAL_SEC", 30),

		DetectionCooldownSec: getenvInt("SENTINEL_DETECTION_COOLDOWN_SEC", 300),
		DetectionTailLines:   int64(getenvInt("SENTINEL_DETECTION_TAIL_LINES", 100)),

		ScaleFixDefaultReplicas: int32(getenvInt("SENTINEL_SCALE_FIX_REPLICAS", 2)),
		LLMPriorWeight:          getenvFloat("SENTINEL_LLM_PRIOR_WEIGHT", 0.4),

		SandboxProbePaths: splitCSV(getenv("SENTINEL_SANDBOX_PROBE_PATHS", "/,/healthz,/readyz")),

		SkipSandboxNonProd:           getenvBool("SENTINEL_SKIP_SANDBOX_NON_PROD", false),
		SandboxEnabled:               getenvBool("SENTINEL_SANDBOX_ENABLED", true),
		SandboxRequired:              getenvBool("SENTINEL_SANDBOX_REQUIRED", false),
		SandboxNetworkPolicyRequired: getenvBool("SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED", true),
		SandboxMaxRetries:            getenvInt("SENTINEL_SANDBOX_MAX_RETRIES", 2),

		LLMMaxRetries:       getenvInt("SENTINEL_LLM_MAX_RETRIES", 3),
		LLMRetryBaseDelayMs: getenvInt("SENTINEL_LLM_RETRY_BASE_DELAY_MS", 500),

		PrometheusURL: getenv("SENTINEL_PROMETHEUS_URL", ""),
		LokiURL:       getenv("SENTINEL_LOKI_URL", ""),
		JaegerURL:     getenv("SENTINEL_JAEGER_URL", ""),
		TempoURL:      getenv("SENTINEL_TEMPO_URL", ""),

		PostMergeWatchEnabled: getenvBool("SENTINEL_POSTMERGE_WATCH_ENABLED", false),
		SLOWatchEnabled:       getenvBool("SENTINEL_SLO_WATCH_ENABLED", false),
		CausalRCAEnabled:      getenvBool("SENTINEL_CAUSAL_RCA_ENABLED", false),

		AdmissionWebhookEnabled: getenvBool("SENTINEL_ADMISSION_WEBHOOK_ENABLED", false),
		AdmissionWebhookAddr:    getenv("SENTINEL_ADMISSION_WEBHOOK_ADDR", ":8443"),
		AdmissionWebhookCertDir: getenv("SENTINEL_ADMISSION_WEBHOOK_CERT_DIR", "/data/admission-tls"),

		CVEScannerType:     getenv("SENTINEL_CVE_SCANNER", "noop"),
		CVEScannerBinary:   getenv("SENTINEL_CVE_SCANNER_BINARY", ""),
		CVEScannerCacheTTL: getenvInt("SENTINEL_CVE_SCANNER_CACHE_TTL", 1800),

		AttestEnabled: getenvBool("SENTINEL_ATTEST_ENABLED", false),
		AttestLogDir:  getenv("SENTINEL_ATTEST_LOG_DIR", "/data/attest"),

		LLMCostRouterEnabled: getenvBool("SENTINEL_LLMCOST_ROUTER_ENABLED", false),

		ChaosEnabled:     getenvBool("SENTINEL_CHAOS_ENABLED", false),
		ChaosNamespaces:  getenvCsv("SENTINEL_CHAOS_NAMESPACES"),
		ChaosIntervalMin: getenvInt("SENTINEL_CHAOS_INTERVAL_MIN", 60),

		SlackWebhook:       getenv("SENTINEL_SLACK_WEBHOOK", ""),
		SlackSigningSecret: getenv("SENTINEL_SLACK_SIGNING_SECRET", ""),
		SlackBotUserID:     getenv("SENTINEL_SLACK_BOT_USER_ID", ""),
		SlackBotToken:      getenv("SENTINEL_SLACK_BOT_TOKEN", ""),

		LogLevel: getenv("SENTINEL_LOG_LEVEL", "INFO"),

		ObserveOnly: getenvBool("SENTINEL_OBSERVE_ONLY", false),

		AutoMergeEnabled:         getenvBool("SENTINEL_AUTO_MERGE_ENABLED", false),
		AutoMergeNamespaces:      splitCSV(getenv("SENTINEL_AUTO_MERGE_NAMESPACES", "")),
		AutoMergeMinConfidence:   getenvFloat("SENTINEL_AUTO_MERGE_MIN_CONFIDENCE", 0.85),
		AutoMergeRequireCI:       getenvBool("SENTINEL_AUTO_MERGE_REQUIRE_CI", true),
		AutoMergeCITimeoutSec:    getenvInt("SENTINEL_AUTO_MERGE_CI_TIMEOUT_SEC", 900),
		AutoMergeMaxPerHour:      getenvInt("SENTINEL_AUTO_MERGE_MAX_PER_HOUR", 3),
		AutoMergeMinSandboxLevel: getenvInt("SENTINEL_AUTOMERGE_MIN_SANDBOX_LEVEL", 3),
		AutoMergeCooldownMin:     getenvInt("SENTINEL_AUTO_MERGE_COOLDOWN_MIN", 60),

		AutoMergeEnabled:       getenvBool("SENTINEL_AUTO_MERGE_ENABLED", false),
		AutoMergeNamespaces:    splitCSV(getenv("SENTINEL_AUTO_MERGE_NAMESPACES", "")),
		AutoMergeMinConfidence: getenvFloat("SENTINEL_AUTO_MERGE_MIN_CONFIDENCE", 0.85),
		AutoMergeRequireCI:     getenvBool("SENTINEL_AUTO_MERGE_REQUIRE_CI", true),
		AutoMergeCITimeoutSec:  getenvInt("SENTINEL_AUTO_MERGE_CI_TIMEOUT_SEC", 900),
		AutoMergeMaxPerHour:    getenvInt("SENTINEL_AUTO_MERGE_MAX_PER_HOUR", 3),
		AutoMergeCooldownMin:   getenvInt("SENTINEL_AUTO_MERGE_COOLDOWN_MIN", 60),

		AutoRemediationEnabled: getenvBool("SENTINEL_AUTO_REMEDIATION_ENABLED", false),
	}
}

// ResolvedBaseURL returns the LLM base URL, deriving from the provider name
// when LLMBaseURL is unset.
func (c *Config) ResolvedBaseURL() string {
	if c.LLMBaseURL != "" {
		return c.LLMBaseURL
	}
	switch strings.ToLower(c.LLMProvider) {
	case "openai":
		return "https://api.openai.com/v1"
	case "zai":
		return "https://api.z.ai/api/paas/v4"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case "ollama":
		return "http://ollama:11434/v1"
	case "vllm":
		return "http://vllm:8000/v1"
	case "litellm":
		return "http://litellm:4000/v1"
	default:
		return "https://api.z.ai/api/paas/v4"
	}
}

// CanWriteTo reports whether Sentinel is allowed to apply fixes in this namespace.
func (c *Config) CanWriteTo(namespace string) bool {
	if len(c.WriteNamespaces) == 0 {
		return false
	}
	return slices.Contains(c.WriteNamespaces, namespace)
}

// IsProductionNamespace reports whether the namespace is in the production list.
func (c *Config) IsProductionNamespace(namespace string) bool {
	return slices.Contains(c.ProductionNamespaces, namespace)
}

// IsRepoAllowed reports whether repo is in the AllowedGitHubRepos allowlist.
// An empty allowlist allows all repos (NOT recommended for production).
// Matching is case-insensitive and ignores a trailing ".git".
func (c *Config) IsRepoAllowed(repo string) bool {
	if len(c.AllowedGitHubRepos) == 0 {
		return true
	}
	target := normalizeRepoForCompare(repo)
	for _, allowed := range c.AllowedGitHubRepos {
		if normalizeRepoForCompare(allowed) == target {
			return true
		}
	}
	return false
}

// Validate returns a list of blocking errors and a list of non-blocking
// warnings. A non-empty errors slice means Sentinel cannot start.
// Validate returns blocking errors and non-blocking warnings. In observe-only
// mode (SENTINEL_OBSERVE_ONLY=true) or when capabilities are auto-degraded,
// missing LLM/GitHub/K8s config produces warnings instead of errors.
//
// Sentinel never refuses to start for missing optional config. It starts in
// the most capable mode the available config allows:
//
//	Full autonomous mode: all env vars set.
//	Observe-only mode: SENTINEL_OBSERVE_ONLY=true, or all creds empty.
//	Degraded mode: some creds set, some empty (noop LLM, no-PR, etc.).
func (c *Config) Validate() (errors []string, warnings []string) {
	if c.ObserveOnly {
		warnings = append(warnings, "SENTINEL_OBSERVE_ONLY=true — running in observe-only mode (no LLM, no GitHub, no K8s writes). The API + web UI are served; no autonomous actions will be taken.")
		// In observe-only mode, everything is optional. Still validate
		// numeric ranges below.
	} else {
		// Auto-degrade: warn instead of error when creds are missing.
		if c.LLMAPIKey == "" {
			warnings = append(warnings, "SENTINEL_LLM_API_KEY is empty — LLM calls will be noop (logged, not sent). Investigations will detect incidents but cannot diagnose or fix them.")
		}
		if c.LLMProvider == "" {
			warnings = append(warnings, "SENTINEL_LLM_PROVIDER is empty — defaulting to 'zai'")
			c.LLMProvider = "zai"
		} else if !isKnownProvider(c.LLMProvider) {
			warnings = append(warnings, fmt.Sprintf(
				"SENTINEL_LLM_PROVIDER=%q is not in the known-providers list — "+
					"if this is a typo, LLM calls will be sent to the wrong endpoint with your API key attached",
				c.LLMProvider))
		}
		if c.LLMFastModel == "" {
			warnings = append(warnings, "SENTINEL_LLM_FAST_MODEL is empty — defaulting to 'glm-4-flash'")
			c.LLMFastModel = "glm-4-flash"
		}
		if c.LLMFrontierModel == "" {
			warnings = append(warnings, "SENTINEL_LLM_FRONTIER_MODEL is empty — defaulting to 'glm-4.5'")
			c.LLMFrontierModel = "glm-4.5"
		}
		if c.GitHubToken == "" {
			warnings = append(warnings, "SENTINEL_GITHUB_TOKEN is empty — Sentinel cannot clone repos or open PRs. Investigations will diagnose but cannot fix.")
		}
	}

	if len(c.WriteNamespaces) == 0 {
		warnings = append(warnings, "SENTINEL_WRITE_NAMESPACES is empty — Sentinel cannot write fixes to any namespace")
	}

	if c.APIToken == "" {
		warnings = append(warnings, "SENTINEL_API_TOKEN is empty — admin API is unauthenticated. Set this in production.")
	}

	if len(c.AllowedGitHubRepos) == 0 && c.GitHubToken != "" {
		warnings = append(warnings, "SENTINEL_ALLOWED_GITHUB_REPOS is empty — Sentinel will clone ANY repo annotated on a pod using the configured GitHub PAT. Set an explicit allowlist.")
	}

	if c.SlackBotToken != "" && c.SlackSigningSecret == "" {
		warnings = append(warnings, "SENTINEL_SLACK_SIGNING_SECRET is empty but SLACK_BOT_TOKEN is set — Slack requests will be rejected")
	}

	if c.SandboxMaxRetries < 0 {
		warnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (negative) — clamping to 0", c.SandboxMaxRetries))
		c.SandboxMaxRetries = 0
	}
	if c.SandboxMaxRetries > 10 {
		warnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (very high) — each retry takes ~5-10min", c.SandboxMaxRetries))
	}
	if c.InvestigatorTimeoutSec <= 0 {
		warnings = append(warnings, fmt.Sprintf("SENTINEL_INVESTIGATOR_TIMEOUT=%d (non-positive)", c.InvestigatorTimeoutSec))
	}
	if c.ConfidenceAutonomous < 0 || c.ConfidenceAutonomous > 1 {
		warnings = append(warnings, fmt.Sprintf("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS=%.2f out of [0,1] — clamping", c.ConfidenceAutonomous))
		c.ConfidenceAutonomous = clamp01(c.ConfidenceAutonomous)
	}

	return errors, warnings
}

// IsLLMConfigured reports whether the LLM is usable. When false, the LLM
// router will be a noop that logs "would have called LLM" instead of making
// API calls.
func (c *Config) IsLLMConfigured() bool {
	return !c.ObserveOnly && c.LLMAPIKey != "" && c.LLMProvider != ""
}

// IsGitHubConfigured reports whether GitHub operations are possible. When
// false, Sentinel detects and diagnoses but cannot open PRs.
func (c *Config) IsGitHubConfigured() bool {
	return !c.ObserveOnly && c.GitHubToken != ""
}

// IsObserveOnly reports whether Sentinel is in observe-only mode (no
// autonomous actions, API + UI only).
func (c *Config) IsObserveOnly() bool {
	return c.ObserveOnly
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// isKnownProvider is a non-exhaustive list of providers recognized by the
// goai mapping. Unknown providers are allowed (with a warning) so operators
// can use custom base URLs.
func isKnownProvider(p string) bool {
	switch p {
	case "openai", "anthropic", "zai", "openrouter", "gemini", "bedrock",
		"azure", "vertex", "mistral", "ollama", "deepseek", "groq",
		"together", "fireworks", "cohere", "perplexity", "anyscale",
		"lepton", "replicate", "novita", "siliconflow":
		return true
	}
	return false
}

func normalizeRepoForCompare(repo string) string {
	r := strings.ToLower(strings.TrimSpace(repo))
	r = strings.TrimSuffix(r, ".git")
	return r
}

// ─── env var helpers ────────────────────────────────────────────────────

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func getenvFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		return def
	}
	return f
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func getenvCsv(key string) []string {
	return splitCSV(os.Getenv(key))
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadLLMProviders parses SENTINEL_LLM_PROVIDERS as a JSON array of
// ProviderConfig. Entries missing ProviderType or APIKey (for non-local
// providers) are dropped. Returns nil on parse failure (logs a warning).
func loadLLMProviders() []schemas.ProviderConfig {
	raw := os.Getenv("SENTINEL_LLM_PROVIDERS")
	if raw == "" {
		return nil
	}
	var configs []schemas.ProviderConfig
	if err := json.Unmarshal([]byte(raw), &configs); err != nil {
		logging.Error("SENTINEL_LLM_PROVIDERS failed to parse as JSON — ignoring", "err", err)
		return nil
	}
	valid := make([]schemas.ProviderConfig, 0, len(configs))
	for _, c := range configs {
		if c.ProviderType == "" {
			logging.Info("LLM provider entry missing 'provider' field — skipping", "id", c.ID)
			continue
		}
		if c.APIKey == "" && c.ProviderType != "ollama" && c.ProviderType != "vllm" {
			logging.Info("LLM provider has no apiKey — skipping", "id", c.ID, "provider", c.ProviderType)
			continue
		}
		valid = append(valid, c)
	}
	return valid
}

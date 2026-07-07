// Package config holds all runtime configuration for Sentinel.
// Reads from env vars (set by Helm) and persists to a K8s ConfigMap
// for runtime updates via the API/CLI.
package config

import (
        "slices"

        "encoding/json"
        "fmt"
        "github.com/karimzakzouk/sentinel/internal/logging"
        "log"
        "os"
        "strings"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// Config is the immutable-at-startup config. Runtime-mutable settings
// (LLM key, namespaces, etc.) live in RuntimeSettings.

type Config struct {
        // Storage

        SQLitePath string // path to the SQLite DB file

        // LLM

        LLMProvider      string
        LLMBaseURL       string
        LLMAPIKey        string
        LLMFastModel     string
        LLMFrontierModel string

        // "text-embedding-3-small" to "" (empty) — the previous default was
        // OpenAI-specific but the default LLM provider is "zai", so every
        // default install was calling a non-existent OpenAI embeddings endpoint
        // and silently falling back to hash embeddings. With empty
        // default, the vector store uses hash fallback explicitly, which is
        // honest about what's happening. Operators who want real embeddings
        // must set both SENTINEL_LLM_EMBEDDING_MODEL and ensure their
        // LLMBaseURL/provider supports the /embeddings endpoint.

        LLMEmbeddingModel string

        // Embedding pipeline config. Auto-detects: if EmbeddingAPIKey is set,
        // uses remote API; else falls back to TF-IDF.
        EmbeddingProvider string
        EmbeddingAPIKey   string
        EmbeddingBaseURL  string

        // LLM semantic cache (Phase 2). When enabled + semantic embeddings
        // are configured, LLM responses are cached by prompt similarity.
        LLMSemanticCacheEnabled  bool
        LLMSemanticCacheThreshold float64 // default 0.97

        LLMEmbeddingDim int

        // L2 safe-actions (restart, rollback, scale)
        SafeActionsEnabled          bool
        SafeActionsNamespaces       []string
        SafeActionsMaxPerHour       int
        SafeActionsMaxScaleDeltaPct int

        // Fix verification (Phase 2). Scrapes kubelet cAdvisor for canary
        // pod metrics + optionally re-queries the original alert's PromQL.
        FixVerifyEnabled    bool

        // Kubernetes

        Namespace   string // the namespace Sentinel runs in
        ReleaseName string // the helm release name

        // GitHub

        GitHubToken string

        GitHubWebhookSecret string

        // AlertmanagerWebhookSecret, if set, requires callers of
        // POST /webhook/alertmanager to send it in the
        // X-Sentinel-Webhook-Secret header. Prevents unauthorized alert
        // injection. Empty = no auth (NOT recommended in production).

        AlertmanagerWebhookSecret string

        // is allowed to clone and open PRs against. Sourced from the
        // sentinel.io/repo pod annotation. Empty list = allow all (legacy
        // behavior, NOT recommended for production). When non-empty, any pod
        // annotated with a repo outside this list is skipped with a security
        // warning. This is the primary defense against a malicious or
        // compromised user annotating their own pod with an arbitrary repo
        // string and having Sentinel clone it (and exfiltrate the PAT to
        // github.com in the process).

        AllowedGitHubRepos []string

        // (/admin/*, /settings POST, /incidents POST, /pause, /resume).
        // Empty = auth disabled (NOT recommended for production; the API
        // will log a loud warning at startup). When set, every admin
        // request must carry 'Authorization: Bearer <token>'. Health/readiness
        // and metrics endpoints remain unauthenticated.

        APIToken string

        // the GitHub App's installation token for ALL GitHub API calls (opening
        // PRs, posting comments, reacting, merging) instead of the PAT. This makes
        // PRs and comments appear as the verified "Sentinel" bot identity instead
        // of the PAT owner's personal account.
        // To create a GitHub App:
        //   1. Go to https://github.com/settings/apps/new (or org settings → Developer settings → GitHub Apps)
        //   2. Name: "Sentinel", Homepage URL: your repo
        //   3. Webhook URL: https://your-sentinel/webhook/github (or leave empty for now)
        //   4. Permissions:
        //      - Repository: Contents (Read+Write), Pull requests (Read+Write), Issues (Read+Write), Metadata (Read)
        //      - Organization: Members (Read) — optional, for permission checks
        //   5. Generate a private key (.pem file) — set contents as SENTINEL_GITHUB_APP_PRIVATE_KEY
        //   6. Install the app on your repo/org → note the installation ID from the URL
        // When set, the PAT (GitHubToken) is used as a fallback if the App token call fails.

        GitHubAppID         string
        GitHubAppPrivateKey string
        GitHubAppInstallID  string

        // Docker Hub (for Kaniko sandbox image builder)

        DockerHubUsername string
        DockerHubToken    string

        // secrets.yaml:28, values.yaml:12), so ECR/GCR/ACR/Harbor/Quay users
        // got an invalid destination image:
        //   "docker.io/123456789.dkr.ecr.us-east-1.amazonaws.com/myapp:sentinel-..."
        // Now the registry is configurable. The Kaniko --destination and
        // --cache-repo use this value, and the chart's dockerconfigjson
        // Secret is templated against it.
        // Default: docker.io (backwards-compatible).

        KanikoRegistry string

        //com
        // Now the git host + API URL are configurable.
        // Default: github.com + https://api.github.com (backwards-compatible).

        GitHost      string // e.g. "github.com", "gh.mycorp.internal", "gitlab.com"
        GitHubAPIURL string // e.g. "https://api.github.com", "https://gh.mycorp.internal/api/v3"

        //go:51,
        // branch_resolver.go:48, fix_proposer.go:617), breaking repos whose
        // default branch is master/develop/trunk/release-X.Y.
        // Default: main (backwards-compatible). Per-repo override via
        // sentinel.io/branch annotation takes precedence.

        DefaultBranch string

        // as high-risk and NewPolicy() could not override them. Now configurable.
        // Default: empty (no services are high-risk by default — demo values
        // should not leak into production installs).

        HighRiskServices []string

        // common system namespaces like calico-system, cert-manager, ingress-nginx,
        // monitoring, etc. Sentinel would waste API QPS watching those.
        // Default: kube-system, kube-public, kube-node-lease, calico-system,
        // cert-manager, ingress-nginx, monitoring, prometheus, grafana, loki,
        // metallb-system, longhorn-system, cattle-system, fleet-system,
        // gatekeeper-system, external-secrets, vault, argo-events, argo-rollouts,
        // tekton-pipelines, istio-system, knative-serving, kubernetes-dashboard.

        SkipNamespaces []string

        //go.
        // Default: the previous pinned values (alpine/git:2.43.0,
        // gcr.io/kaniko-project/executor:v1.24.0).

        KanikoInitImage     string
        KanikoExecutorImage string

        //go:208.
        // Default: 3 (backwards-compatible).

        MaxConcurrentInvestigations int

        // RBAC

        ProductionNamespaces []string // namespaces treated as production
        WriteNamespaces      []string // namespaces Sentinel can apply fixes to

        // Confidence thresholds

        ConfidenceAutonomous  float64
        ConfidenceHumanReview float64

        // Panic button thresholds

        PanicCPUThresholdPct      float64
        PanicConnectionsThreshold int

        // Sandbox

        EnableTrafficMirroring bool
        SandboxNamespacePrefix string

        // API

        APIHost string
        APIPort int

        // Investigation timeout (seconds)

        InvestigationTimeout int

        InvestigatorTimeoutSec     int
        EvidenceGathererTimeoutSec int
        AnalystTimeoutSec          int
        FixProposerTimeoutSec      int

        ConfidenceLoopBackThreshold float64
        MaxPipelineIterations       int

        OutcomeWatchDurationMin int
        OutcomeCheckIntervalSec int

        DetectionCooldownSec int
        DetectionTailLines   int64

        ScaleFixDefaultReplicas int32

        LLMPriorWeight float64

        SandboxProbePaths []string

        // Default false — sandbox runs for all namespaces.
        // When SandboxEnabled=false, sandbox is skipped entirely (for all namespaces).
        // When SandboxEnabled=true + SkipSandboxNonProd=true, sandbox is skipped
        // for non-prod namespaces only.

        SkipSandboxNonProd bool

        // SandboxEnabled: should we run the sandbox at all?
        //   true (default) — build canary, deploy, verify, then open PR
        //   false — skip sandbox entirely, open PR immediately (fast mode)
        // SandboxRequired: when sandbox runs and FAILS, what happens?
        //   true — don't open the PR at all (strict mode — only ship verified fixes)
        //   false (default) — open the PR with needs-review label (lenient mode)
        // SandboxMaxRetries: how many times to retry on sandbox failure (Layer B).
        //   The sandbox failure details are fed back to the LLM which generates
        //   a new fix. 0 = no retry (fail once → open PR or block).

        SandboxEnabled    bool
        SandboxRequired   bool
        SandboxMaxRetries int

        LLMMaxRetries       int
        LLMRetryBaseDelayMs int

        // Operators can stack multiple API keys (e.g. 3 OpenRouter + 2 Groq + 1 Gemini)
        // and Sentinel will try them in priority order. If a provider fails, the
        // circuit breaker trips for that provider and subsequent calls skip it
        // until the reset window expires.
        // Loaded from SENTINEL_LLM_PROVIDERS env var (JSON array) — see
        // internal/llm/pool.go for the ProviderConfig schema. Empty = legacy
        // single-provider mode (uses LLMProvider + LLMAPIKey above).

        LLMProviders []schemas.ProviderConfig

        AutoMergeEnabled       bool
        AutoMergeNamespaces    []string
        AutoMergeMinConfidence float64
        AutoMergeRequireCI     bool
        AutoMergeCITimeoutSec  int
        AutoMergeMaxPerHour    int
        AutoMergeCooldownMin   int

        // stuck pods, etc.) without code changes. Default false.

        AutoRemediationEnabled bool

        PrometheusURL string
        LokiURL       string
        JaegerURL     string
        TempoURL      string // 2026: Tempo replaces Jaeger as the default trace backend

        // ─── Tier 1/2 feature toggles (v5.14.0+) ──────────────────────────────
        // All default OFF — operators opt in via env. Each feature is
        // self-contained and can run independently of the others.

        // PostMergeWatch enables the post-merge regression watcher + auto-rollback.
        // Phase 1 (10 min) + Phase 2 (24h) monitoring of every merged PR.
        PostMergeWatchEnabled bool

        // SLOWatch enables the proactive SLO burn-rate watcher.
        // Fires synthetic incidents BEFORE the crash.
        SLOWatchEnabled bool

        // CausalRCA enables causal root-cause attribution on cascading incidents.
        // Extends the existing correlation + depanalysis modules.
        CausalRCAEnabled bool

        // AdmissionWebhookEnabled starts the validating admission webhook server.
        // Listens on AdmissionWebhookAddr (default :8443).
        AdmissionWebhookEnabled bool
        AdmissionWebhookAddr    string
        AdmissionWebhookCertDir string

        // AttestEnabled signs every autonomous action with an Ed25519 keypair
        // and writes a tamper-evident hash-chained JSONL log to AttestLogDir.
        // Required for EU AI Act / SOC 2 compliance.
        AttestEnabled bool
        AttestLogDir  string

        // LLMCostRouterEnabled wraps the LLM router with a task-complexity
        // classifier + semantic cache. Cuts LLM cost per incident by ~70%.
        LLMCostRouterEnabled bool

        // ChaosEnabled runs scheduled chaos experiments against staging
        // namespaces (allow-listed via ChaosNamespaces). Sentinel
        // investigates its own injections.
        ChaosEnabled     bool
        ChaosNamespaces  []string
        ChaosIntervalMin int

        SlackWebhook string

        SlackSigningSecret string // verifies Slack webhook signatures
        SlackBotUserID     string // Sentinel's Slack user ID (for @mention parsing)
        SlackBotToken      string // OAuth bot token (xoxb-...) for posting replies

        LogLevel string
}

// RuntimeSettings is the runtime-mutable subset of Config.
// These can be changed via the API/CLI without restarting Sentinel.

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

                // to "". The previous default was OpenAI-specific but the
                // default provider is "zai", so every default install silently fell back
                // to hash embeddings. Empty default is honest about what's happening.

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

                Namespace:                 getenv("SENTINEL_NAMESPACE", "sentinel"),
                ReleaseName:               getenv("SENTINEL_RELEASE_NAME", "sentinel"),
                GitHubToken:               getenv("SENTINEL_GITHUB_TOKEN", ""),
                GitHubWebhookSecret:       getenv("SENTINEL_GITHUB_WEBHOOK_SECRET", ""),
                AlertmanagerWebhookSecret: getenv("SENTINEL_ALERTMANAGER_WEBHOOK_SECRET", ""),
                AllowedGitHubRepos:        splitCSV(getenv("SENTINEL_ALLOWED_GITHUB_REPOS", "")),
                APIToken:                  getenv("SENTINEL_API_TOKEN", ""),

                GitHubAppID:         getenv("SENTINEL_GITHUB_APP_ID", ""),
                GitHubAppPrivateKey: getenv("SENTINEL_GITHUB_APP_PRIVATE_KEY", ""),
                GitHubAppInstallID:  getenv("SENTINEL_GITHUB_APP_INSTALL_ID", ""),
                DockerHubUsername:   getenv("SENTINEL_DOCKER_HUB_USERNAME", ""),
                DockerHubToken:      getenv("SENTINEL_DOCKER_HUB_TOKEN", ""),

                KanikoRegistry: getenv("SENTINEL_KANIKO_REGISTRY", "docker.io"),

                GitHost:      getenv("SENTINEL_GIT_HOST", "github.com"),
                GitHubAPIURL: getenv("SENTINEL_GITHUB_API_URL", "https://api.github.com"),

                DefaultBranch: getenv("SENTINEL_DEFAULT_BRANCH", "main"),

                HighRiskServices: splitCSV(getenv("SENTINEL_HIGH_RISK_SERVICES", "")),

                SkipNamespaces: splitCSV(getenv("SENTINEL_SKIP_NAMESPACES", "kube-system,kube-public,kube-node-lease,calico-system,calico-apiserver,cert-manager,ingress-nginx,monitoring,prometheus,grafana,loki,metallb-system,longhorn-system,cattle-system,fleet-system,gatekeeper-system,external-secrets,vault,argo-events,argo-rollouts,tekton-pipelines,istio-system,knative-serving,kubernetes-dashboard")),

                KanikoInitImage:     getenv("SENTINEL_KANIKO_INIT_IMAGE", "alpine/git:2.43.0"),
                KanikoExecutorImage: getenv("SENTINEL_KANIKO_EXECUTOR_IMAGE", "gcr.io/kaniko-project/executor:v1.24.0"),

                MaxConcurrentInvestigations: getenvInt("SENTINEL_MAX_CONCURRENT_INVESTIGATIONS", 3),

                // "payments" namespace leaked into every install, causing non-demo
                // services in a namespace literally named "payments" to be treated
                // as production (stricter governance). Operators who actually use a
                // "payments" namespace should set SENTINEL_PRODUCTION_NAMESPACES
                // explicitly.

                ProductionNamespaces:      splitCSV(getenv("SENTINEL_PRODUCTION_NAMESPACES", "prod,production")),
                WriteNamespaces:           splitCSV(getenv("SENTINEL_WRITE_NAMESPACES", "")),
                ConfidenceAutonomous:      getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS", 0.92),
                ConfidenceHumanReview:     getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW", 0.70),
                PanicCPUThresholdPct:      getenvFloat("SENTINEL_PANIC_CPU_THRESHOLD_PCT", 95.0),
                PanicConnectionsThreshold: getenvInt("SENTINEL_PANIC_CONNECTIONS_THRESHOLD", 9000),
                EnableTrafficMirroring:    getenvBool("SENTINEL_ENABLE_TRAFFIC_MIRRORING", true),
                SandboxNamespacePrefix:    getenv("SENTINEL_SANDBOX_NAMESPACE_PREFIX", "sentinel-sandbox"),
                APIHost:                   getenv("SENTINEL_API_HOST", "0.0.0.0"),
                APIPort:                   getenvInt("PORT", 8000),
                InvestigationTimeout:      getenvInt("SENTINEL_INVESTIGATION_TIMEOUT", 600),

                InvestigatorTimeoutSec:     getenvInt("SENTINEL_INVESTIGATOR_TIMEOUT", 120),
                EvidenceGathererTimeoutSec: getenvInt("SENTINEL_EVIDENCE_GATHERER_TIMEOUT", 90),
                AnalystTimeoutSec:          getenvInt("SENTINEL_ANALYST_TIMEOUT", 15),
                FixProposerTimeoutSec:      getenvInt("SENTINEL_FIX_PROPOSER_TIMEOUT", 300),

                ConfidenceLoopBackThreshold: getenvFloat("SENTINEL_CONFIDENCE_LOOP_BACK", 0.3),
                MaxPipelineIterations:       getenvInt("SENTINEL_MAX_PIPELINE_ITERATIONS", 5),

                OutcomeWatchDurationMin: getenvInt("SENTINEL_OUTCOME_WATCH_DURATION_MIN", 10),
                OutcomeCheckIntervalSec: getenvInt("SENTINEL_OUTCOME_CHECK_INTERVAL_SEC", 30),

                DetectionCooldownSec: getenvInt("SENTINEL_DETECTION_COOLDOWN_SEC", 300),
                DetectionTailLines:   int64(getenvInt("SENTINEL_DETECTION_TAIL_LINES", 100)),

                ScaleFixDefaultReplicas: int32(getenvInt("SENTINEL_SCALE_FIX_REPLICAS", 2)),

                LLMPriorWeight: getenvFloat("SENTINEL_LLM_PRIOR_WEIGHT", 0.4),

                SandboxProbePaths: splitCSV(getenv("SENTINEL_SANDBOX_PROBE_PATHS", "/,/healthz,/readyz")),

                // The sandbox verifies the fix actually works before opening a PR.
                // Operators who want fast mode (skip sandbox for non-prod) can set
                // SENTINEL_SKIP_SANDBOX_NON_PROD=true.

                SkipSandboxNonProd: getenvBool("SENTINEL_SKIP_SANDBOX_NON_PROD", false),

                SandboxEnabled:    getenvBool("SENTINEL_SANDBOX_ENABLED", true),
                SandboxRequired:   getenvBool("SENTINEL_SANDBOX_REQUIRED", false),
                SandboxMaxRetries: getenvInt("SENTINEL_SANDBOX_MAX_RETRIES", 2),

                LLMMaxRetries:       getenvInt("SENTINEL_LLM_MAX_RETRIES", 3),
                LLMRetryBaseDelayMs: getenvInt("SENTINEL_LLM_RETRY_BASE_DELAY_MS", 500),

                PrometheusURL: getenv("SENTINEL_PROMETHEUS_URL", ""),
                LokiURL:       getenv("SENTINEL_LOKI_URL", ""),
                JaegerURL:     getenv("SENTINEL_JAEGER_URL", ""),
                TempoURL:      getenv("SENTINEL_TEMPO_URL", ""),

                // Tier 1/2 feature toggles — all default OFF.
                PostMergeWatchEnabled: getenvBool("SENTINEL_POSTMERGE_WATCH_ENABLED", false),
                SLOWatchEnabled:       getenvBool("SENTINEL_SLO_WATCH_ENABLED", false),
                CausalRCAEnabled:      getenvBool("SENTINEL_CAUSAL_RCA_ENABLED", false),
                AdmissionWebhookEnabled: getenvBool("SENTINEL_ADMISSION_WEBHOOK_ENABLED", false),
                AdmissionWebhookAddr:    getenv("SENTINEL_ADMISSION_WEBHOOK_ADDR", ":8443"),
                AdmissionWebhookCertDir: getenv("SENTINEL_ADMISSION_WEBHOOK_CERT_DIR", "/data/admission-tls"),
                AttestEnabled: getenvBool("SENTINEL_ATTEST_ENABLED", false),
                AttestLogDir:  getenv("SENTINEL_ATTEST_LOG_DIR", "/data/attest"),
                LLMCostRouterEnabled: getenvBool("SENTINEL_LLMCOST_ROUTER_ENABLED", false),
                ChaosEnabled:     getenvBool("SENTINEL_CHAOS_ENABLED", false),
                ChaosNamespaces:  getenvCsv("SENTINEL_CHAOS_NAMESPACES"),
                ChaosIntervalMin: getenvInt("SENTINEL_CHAOS_INTERVAL_MIN", 60),

                SlackWebhook: getenv("SENTINEL_SLACK_WEBHOOK", ""),

                SlackSigningSecret: getenv("SENTINEL_SLACK_SIGNING_SECRET", ""),
                SlackBotUserID:     getenv("SENTINEL_SLACK_BOT_USER_ID", ""),
                SlackBotToken:      getenv("SENTINEL_SLACK_BOT_TOKEN", ""),

                LogLevel: getenv("SENTINEL_LOG_LEVEL", "INFO"),

                // Empty = legacy single-provider mode (uses LLMProvider + LLMAPIKey).

                LLMProviders: loadLLMProviders(),

                // auto-merge is OFF by default, no namespaces allowed, requires
                // explicit opt-in per service via annotation.

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

// ResolvedBaseURL returns the actual LLM base URL, deriving from provider if needed.

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

// CanWriteTo returns true if Sentinel is allowed to apply fixes in this namespace.

func (c *Config) CanWriteTo(namespace string) bool {
        if len(c.WriteNamespaces) == 0 {
                return false
        }
        return slices.Contains(c.WriteNamespaces, namespace)
}

// IsProductionNamespace returns true if the namespace is in the production list.

func (c *Config) IsProductionNamespace(namespace string) bool {
        return slices.Contains(c.ProductionNamespaces, namespace)
}

// IsRepoAllowed returns true if the given GitHub repo (in "owner/name" form,
// optionally with a trailing ".git") is in the AllowedGitHubRepos allowlist.
// If the allowlist is empty, all repos are allowed (legacy behavior —
// NOT recommended for production; set the allowlist via
// SENTINEL_ALLOWED_GITHUB_REPOS=owner1/repo1,owner2/repo2).
// user with pod-edit permission annotates their pod with an arbitrary repo
// string and Sentinel dutifully clones it (sending the GitHub PAT to
// github.com for any repo the PAT can access). With the allowlist set, only
// explicitly-listed repos can be cloned.
// Matching is case-insensitive (GitHub repo paths are case-insensitive).
// A trailing ".git" on either side is stripped before comparison.

func (c *Config) IsRepoAllowed(repo string) bool {
        if len(c.AllowedGitHubRepos) == 0 {
                return true
        }
        normalized := normalizeRepoForCompare(repo)
        for _, allowed := range c.AllowedGitHubRepos {
                if normalizeRepoForCompare(allowed) == normalized {
                        return true
                }
        }
        return false
}

// Validate checks the config for missing required fields, invalid values, and
// insecure defaults. Returns a list of errors (empty if all checks pass) and
// a list of warnings (non-blocking).
// with empty required fields and failed 5-10 minutes later at the first LLM call.

func (c *Config) Validate() (errors []string, warnings []string) {
        // Critical: LLM credentials

        if c.LLMAPIKey == "" {
                errors = append(errors, "SENTINEL_LLM_API_KEY is empty — Sentinel cannot run without an LLM API key")
        }
        if c.LLMProvider == "" {
                errors = append(errors, "SENTINEL_LLM_PROVIDER is empty — set to a known provider (openai, anthropic, zai, openrouter, ...)")
        } else if !isKnownProvider(c.LLMProvider) {
                warnings = append(warnings, fmt.Sprintf("SENTINEL_LLM_PROVIDER=%q is not in the known-providers list — "+
                        "if this is a typo, LLM calls will be sent to the wrong endpoint with your API key attached", c.LLMProvider))
        }
        if c.LLMFastModel == "" {
                errors = append(errors, "SENTINEL_LLM_FAST_MODEL is empty — set a valid model name")
        }
        if c.LLMFrontierModel == "" {
                errors = append(errors, "SENTINEL_LLM_FRONTIER_MODEL is empty — set a valid model name")
        }

        // Critical: GitHub credentials

        if c.GitHubToken == "" {
                errors = append(errors, "SENTINEL_GITHUB_TOKEN is empty — Sentinel needs a GitHub PAT to clone repos and open PRs. "+
                        "(If using a GitHub App, configure it via runtime config or SENTINEL_GITHUB_APP_* env vars.)")
        }

        // Critical: namespace config

        if len(c.WriteNamespaces) == 0 {
                warnings = append(warnings, "SENTINEL_WRITE_NAMESPACES is empty — Sentinel cannot write fixes to any namespace")
        }

        // High: API token (security)

        if c.APIToken == "" {
                warnings = append(warnings, "SENTINEL_API_TOKEN is empty — admin API is unauthenticated. "+
                        "Anyone with cluster network access can trigger investigations, pause autonomous mode, "+
                        "and overwrite the LLM API key. Set this in production.")
        }

        // High: GitHub repo allowlist

        if len(c.AllowedGitHubRepos) == 0 && c.GitHubToken != "" {
                warnings = append(warnings, "SENTINEL_ALLOWED_GITHUB_REPOS is empty — Sentinel will clone ANY repo "+
                        "annotated on a pod using the configured GitHub PAT. Set an explicit allowlist.")
        }

        // High: Slack signing secret

        if c.SlackBotToken != "" && c.SlackSigningSecret == "" {
                warnings = append(warnings, "SENTINEL_SLACK_SIGNING_SECRET is empty but SLACK_BOT_TOKEN is set — "+
                        "Slack requests will be REJECTED (signature verification cannot run without the secret)")
        }

        // Range checks

        if c.SandboxMaxRetries < 0 {
                warnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (negative) — clamping to 0", c.SandboxMaxRetries))
                c.SandboxMaxRetries = 0
        }
        if c.SandboxMaxRetries > 10 {
                warnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (very high) — each retry takes ~5-10min; "+
                        "a single incident could spend hours in sandbox retries", c.SandboxMaxRetries))
        }
        if c.InvestigatorTimeoutSec <= 0 {
                warnings = append(warnings, fmt.Sprintf("SENTINEL_INVESTIGATOR_TIMEOUT=%d (non-positive) — using default", c.InvestigatorTimeoutSec))
        }
        if c.ConfidenceAutonomous < 0 || c.ConfidenceAutonomous > 1 {
                warnings = append(warnings, fmt.Sprintf("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS=%.2f out of [0,1] — "+
                        "clamping to [0,1]", c.ConfidenceAutonomous))
                if c.ConfidenceAutonomous < 0 {
                        c.ConfidenceAutonomous = 0
                } else if c.ConfidenceAutonomous > 1 {
                        c.ConfidenceAutonomous = 1
                }
        }

        return errors, warnings
}

// isKnownProvider returns true if the provider string is recognized by the
// goai_provider mapping. This is a non-exhaustive list — unknown providers
// are allowed (with a warning) because the user might be using a custom base URL.

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

// normalizeRepoForCompare lowercases the repo string and strips a trailing
// ".git" so "Acme/App.git" matches "acme/app".

func normalizeRepoForCompare(repo string) string {
        r := strings.ToLower(strings.TrimSpace(repo))
        r = strings.TrimSuffix(r, ".git")
        return r
}

func getenv(key, def string) string {
        if v := os.Getenv(key); v != "" {
                return v
        }
        return def
}

func getenvInt(key string, def int) int {
        if v := os.Getenv(key); v != "" {
                var n int
                if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
                        return n
                }
        }
        return def
}

func getenvFloat(key string, def float64) float64 {
        if v := os.Getenv(key); v != "" {
                var f float64
                if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
                        return f
                }
        }
        return def
}

func getenvBool(key string, def bool) bool {
        if v := os.Getenv(key); v != "" {
                switch strings.ToLower(v) {
                case "1", "true", "yes", "on":
                        return true
                case "0", "false", "no", "off":
                        return false
                }
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

// loadLLMProviders loads the multi-provider pool config from the
// SENTINEL_LLM_PROVIDERS env var. The value must be a JSON array of
// ProviderConfig objects (see internal/llm/pool.go).
// Example env value (single line, escaped):
//   SENTINEL_LLM_PROVIDERS='[{"id":"or-1","provider":"openrouter","apiKey":"sk-or-...","fastModel":"anthropic/claude-3.5-sonnet","frontierModel":"anthropic/claude-3.5-sonnet","priority":1},{"id":"groq-1","provider":"groq","apiKey":"gsk_...","fastModel":"llama-3.1-70b-versatile","frontierModel":"llama-3.1-70b-versatile","priority":2}]'
// For Helm, set via --set-json or a values file. Empty/unset = legacy
// single-provider mode (uses LLMProvider + LLMAPIKey fields).

func loadLLMProviders() []schemas.ProviderConfig {
        raw := os.Getenv("SENTINEL_LLM_PROVIDERS")
        if raw == "" {
                return nil
        }
        var configs []schemas.ProviderConfig
        if err := json.Unmarshal([]byte(raw), &configs); err != nil {
                logging.Error("SENTINEL_LLM_PROVIDERS is set but failed to parse as JSON:  — ignoring (falling back to single-provider mode)", "err", err)
                return nil
        }
        // Validate: drop entries with no API key (unless ollama/vllm which don't need one)

        valid := make([]schemas.ProviderConfig, 0, len(configs))
        for _, c := range configs {
                if c.ProviderType == "" {
                        logging.Info("LLM provider entry missing 'provider' field — skipping: %+v", "c", c)
                        continue
                }
                if c.APIKey == "" && c.ProviderType != "ollama" && c.ProviderType != "vllm" {
                        logging.Info("LLM provider has no apiKey, skipping", "value", c.ID, c.ProviderType)
                        continue
                }
                valid = append(valid, c)
        }
        if len(valid) > 0 {
                log.Printf("[config] loaded %d LLM providers for failover pool: %s", len(valid),
                        func() string {
                                ids := make([]string, len(valid))
                                for i, c := range valid {
                                        ids[i] = fmt.Sprintf("%s(%s,p%d)", c.ID, c.ProviderType, c.Priority)
                                }
                                return strings.Join(ids, ", ")
                        }())
        }
        return valid
}

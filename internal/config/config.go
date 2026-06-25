// Package config holds all runtime configuration for Sentinel.
// Reads from env vars (set by Helm) and persists to a K8s ConfigMap
// for runtime updates via the API/CLI.
package config

import (
        "fmt"
        "os"
        "strings"
)

// Config is the immutable-at-startup config. Runtime-mutable settings
// (LLM key, namespaces, etc.) live in RuntimeSettings.
type Config struct {
        // Storage
        SQLitePath string // path to the SQLite DB file

        // LLM
        LLMProvider     string
        LLMBaseURL      string
        LLMAPIKey       string
        LLMFastModel    string
        LLMFrontierModel string
        LLMEmbeddingModel string // v3.4.0: configurable embedding model (was hardcoded "text-embedding-3-small")

        // Kubernetes
        Namespace   string // the namespace Sentinel runs in
        ReleaseName string // the helm release name

        // GitHub
        GitHubToken string
        // v3.5.0: GitHub webhook secret for PR review signature verification.
        GitHubWebhookSecret string
        // v3.8.4: Optional allowlist of GitHub repos (owner/name) that Sentinel
        // is allowed to clone and open PRs against. Sourced from the
        // sentinel.io/repo pod annotation. Empty list = allow all (legacy
        // behavior, NOT recommended for production). When non-empty, any pod
        // annotated with a repo outside this list is skipped with a security
        // warning. This is the primary defense against a malicious or
        // compromised user annotating their own pod with an arbitrary repo
        // string and having Sentinel clone it (and exfiltrate the PAT to
        // github.com in the process).
        AllowedGitHubRepos []string
        // v3.8.4: Bearer token required to call admin API endpoints
        // (/admin/*, /settings POST, /incidents POST, /pause, /resume).
        // Empty = auth disabled (NOT recommended for production; the API
        // will log a loud warning at startup). When set, every admin
        // request must carry 'Authorization: Bearer <token>'. Health/readiness
        // and metrics endpoints remain unauthenticated.
        APIToken string

        // Docker Hub (for Kaniko sandbox image builder)
        DockerHubUsername string
        DockerHubToken    string

        // RBAC
        ProductionNamespaces []string // namespaces treated as production
        WriteNamespaces      []string // namespaces Sentinel can apply fixes to

        // Confidence thresholds
        ConfidenceAutonomous   float64
        ConfidenceHumanReview  float64

        // Panic button thresholds
        PanicCPUThresholdPct     float64
        PanicConnectionsThreshold int

        // Sandbox
        EnableTrafficMirroring bool
        SandboxNamespacePrefix string

        // API
        APIHost string
        APIPort int

        // Investigation timeout (seconds)
        InvestigationTimeout int

        // v3.4.0: Agent timeouts (seconds). Previously hardcoded in pipeline.go.
        InvestigatorTimeoutSec  int
        EvidenceGathererTimeoutSec int
        AnalystTimeoutSec       int
        FixProposerTimeoutSec   int

        // v3.4.0: Confidence routing threshold (was hardcoded 0.3, comment said 0.5).
        ConfidenceLoopBackThreshold float64
        MaxPipelineIterations       int

        // v3.4.0: Outcome watcher (was hardcoded 10min / 30s).
        OutcomeWatchDurationMin    int
        OutcomeCheckIntervalSec    int

        // v3.4.0: Detection layer (was hardcoded 5min cooldown / 100 tail lines).
        DetectionCooldownSec    int
        DetectionTailLines      int64

        // v3.4.0: Scale fix default replicas (was hardcoded int32(2)).
        ScaleFixDefaultReplicas int32

        // v3.4.0: LLM prior weight in analyst (was hardcoded 0.4).
        LLMPriorWeight float64

        // v3.4.0: Sandbox probe config (was hardcoded /pay?amount=...).
        SandboxProbePaths []string

        // v3.6.0: Skip sandbox verification for non-production namespaces.
        // Default true — sim/staging don't need canary verification, just open the PR.
        SkipSandboxNonProd bool

        // v3.4.0: LLM retry config (was no retry — single HTTP call).
        LLMMaxRetries        int
        LLMRetryBaseDelayMs  int

        // v3.4.0: Observability backends (for Evidence Gatherer).
        PrometheusURL string
        LokiURL       string
        JaegerURL     string

        // v3.4.0: Slack webhook (was read via os.Getenv in 2 places).
        SlackWebhook string

        // v3.5.0: Interactive Slack bot (Events API + slash commands)
        SlackSigningSecret string // verifies Slack webhook signatures
        SlackBotUserID     string // Sentinel's Slack user ID (for @mention parsing)
        SlackBotToken      string // OAuth bot token (xoxb-...) for posting replies

        // v3.4.0: Structured logging level (DEBUG/INFO/WARN/ERROR). Default INFO.
        LogLevel string
}

// RuntimeSettings is the runtime-mutable subset of Config.
// These can be changed via the API/CLI without restarting Sentinel.
type RuntimeSettings struct {
        LLMProvider      string  `json:"llm_provider"`
        LLMAPIKey        string  `json:"llm_api_key"`
        LLMFastModel     string  `json:"llm_fast_model"`
        LLMFrontierModel string  `json:"llm_frontier_model"`
        GitHubToken      string  `json:"github_token"`
        PrometheusURL    string  `json:"prometheus_url"`
        LokiURL          string  `json:"loki_url"`
        JaegerURL        string  `json:"jaeger_url"`
        ProductionNamespaces string  `json:"production_namespaces"`
        WriteNamespaces  string  `json:"write_namespaces"`
        ConfidenceAutonomous  float64 `json:"confidence_threshold_autonomous"`
        ConfidenceHumanReview float64 `json:"confidence_threshold_human_review"`
        PanicCPUThresholdPct  float64 `json:"panic_cpu_threshold_pct"`
        PanicConnectionsThreshold int  `json:"panic_connections_threshold"`
}

// Load reads configuration from environment variables.
func Load() *Config {
        return &Config{
                SQLitePath:             getenv("SENTINEL_SQLITE_PATH", "/data/sentinel.db"),
                LLMProvider:            getenv("SENTINEL_LLM_PROVIDER", "zai"),
                LLMBaseURL:             getenv("SENTINEL_LLM_BASE_URL", ""),
                LLMAPIKey:              getenv("SENTINEL_LLM_API_KEY", ""),
                LLMFastModel:           getenv("SENTINEL_LLM_FAST_MODEL", "glm-4-flash"),
                LLMFrontierModel:       getenv("SENTINEL_LLM_FRONTIER_MODEL", "glm-4.5"),
                LLMEmbeddingModel:      getenv("SENTINEL_LLM_EMBEDDING_MODEL", "text-embedding-3-small"),
                Namespace:              getenv("SENTINEL_NAMESPACE", "sentinel"),
                ReleaseName:            getenv("SENTINEL_RELEASE_NAME", "sentinel"),
                GitHubToken:            getenv("SENTINEL_GITHUB_TOKEN", ""),
                GitHubWebhookSecret:    getenv("SENTINEL_GITHUB_WEBHOOK_SECRET", ""),
                AllowedGitHubRepos:     splitCSV(getenv("SENTINEL_ALLOWED_GITHUB_REPOS", "")),
                APIToken:               getenv("SENTINEL_API_TOKEN", ""),
                DockerHubUsername:      getenv("SENTINEL_DOCKER_HUB_USERNAME", ""),
                DockerHubToken:         getenv("SENTINEL_DOCKER_HUB_TOKEN", ""),
                ProductionNamespaces:   splitCSV(getenv("SENTINEL_PRODUCTION_NAMESPACES", "prod,production,payments")),
                WriteNamespaces:        splitCSV(getenv("SENTINEL_WRITE_NAMESPACES", "")),
                ConfidenceAutonomous:   getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS", 0.92),
                ConfidenceHumanReview:  getenvFloat("SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW", 0.70),
                PanicCPUThresholdPct:   getenvFloat("SENTINEL_PANIC_CPU_THRESHOLD_PCT", 95.0),
                PanicConnectionsThreshold: getenvInt("SENTINEL_PANIC_CONNECTIONS_THRESHOLD", 9000),
                EnableTrafficMirroring: getenvBool("SENTINEL_ENABLE_TRAFFIC_MIRRORING", true),
                SandboxNamespacePrefix: getenv("SENTINEL_SANDBOX_NAMESPACE_PREFIX", "sentinel-sandbox"),
                APIHost:                getenv("SENTINEL_API_HOST", "0.0.0.0"),
                APIPort:                getenvInt("PORT", 8000),
                InvestigationTimeout:   getenvInt("SENTINEL_INVESTIGATION_TIMEOUT", 600),

                // v3.4.0: Agent timeouts (were hardcoded in pipeline.go)
                InvestigatorTimeoutSec:     getenvInt("SENTINEL_INVESTIGATOR_TIMEOUT", 120),
                EvidenceGathererTimeoutSec: getenvInt("SENTINEL_EVIDENCE_GATHERER_TIMEOUT", 90),
                AnalystTimeoutSec:          getenvInt("SENTINEL_ANALYST_TIMEOUT", 15),
                FixProposerTimeoutSec:      getenvInt("SENTINEL_FIX_PROPOSER_TIMEOUT", 120),

                // v3.4.0: Confidence routing (was hardcoded 0.3; comment said 0.5)
                ConfidenceLoopBackThreshold: getenvFloat("SENTINEL_CONFIDENCE_LOOP_BACK", 0.3),
                MaxPipelineIterations:       getenvInt("SENTINEL_MAX_PIPELINE_ITERATIONS", 5),

                // v3.4.0: Outcome watcher (was hardcoded 10min / 30s)
                OutcomeWatchDurationMin: getenvInt("SENTINEL_OUTCOME_WATCH_DURATION_MIN", 10),
                OutcomeCheckIntervalSec: getenvInt("SENTINEL_OUTCOME_CHECK_INTERVAL_SEC", 30),

                // v3.4.0: Detection layer (was hardcoded 5min / 100 lines)
                DetectionCooldownSec: getenvInt("SENTINEL_DETECTION_COOLDOWN_SEC", 300),
                DetectionTailLines:   int64(getenvInt("SENTINEL_DETECTION_TAIL_LINES", 100)),

                // v3.4.0: Scale fix (was hardcoded int32(2))
                ScaleFixDefaultReplicas: int32(getenvInt("SENTINEL_SCALE_FIX_REPLICAS", 2)),

                // v3.4.0: Analyst LLM weight (was hardcoded 0.4)
                LLMPriorWeight: getenvFloat("SENTINEL_LLM_PRIOR_WEIGHT", 0.4),

                // v3.4.0: Sandbox probe paths (was hardcoded /pay?amount=...)
                SandboxProbePaths: splitCSV(getenv("SENTINEL_SANDBOX_PROBE_PATHS", "/,/healthz,/readyz")),

                // v3.6.0: Skip sandbox for non-prod (default true — faster PRs in sim/staging)
                // v3.10.0: Sandbox ON by default for ALL namespaces (was true=skip).
                // The sandbox verifies the fix actually works before opening a PR.
                // Operators who want fast mode (skip sandbox for non-prod) can set
                // SENTINEL_SKIP_SANDBOX_NON_PROD=true.
                SkipSandboxNonProd: getenvBool("SENTINEL_SKIP_SANDBOX_NON_PROD", false),

                // v3.4.0: LLM retry (was no retry)
                LLMMaxRetries:       getenvInt("SENTINEL_LLM_MAX_RETRIES", 3),
                LLMRetryBaseDelayMs: getenvInt("SENTINEL_LLM_RETRY_BASE_DELAY_MS", 500),

                // v3.4.0: Observability backends (for Evidence Gatherer)
                PrometheusURL: getenv("SENTINEL_PROMETHEUS_URL", ""),
                LokiURL:       getenv("SENTINEL_LOKI_URL", ""),
                JaegerURL:     getenv("SENTINEL_JAEGER_URL", ""),

                // v3.4.0: Slack (was os.Getenv in 2 places)
                SlackWebhook: getenv("SENTINEL_SLACK_WEBHOOK", ""),

                // v3.5.0: Interactive Slack bot
                SlackSigningSecret: getenv("SENTINEL_SLACK_SIGNING_SECRET", ""),
                SlackBotUserID:     getenv("SENTINEL_SLACK_BOT_USER_ID", ""),
                SlackBotToken:      getenv("SENTINEL_SLACK_BOT_TOKEN", ""),

                // v3.4.0: Structured logging level
                LogLevel: getenv("SENTINEL_LOG_LEVEL", "INFO"),
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
        for _, ns := range c.WriteNamespaces {
                if ns == namespace {
                        return true
                }
        }
        return false
}

// IsProductionNamespace returns true if the namespace is in the production list.
func (c *Config) IsProductionNamespace(namespace string) bool {
        for _, ns := range c.ProductionNamespaces {
                if ns == namespace {
                        return true
                }
        }
        return false
}

// IsRepoAllowed returns true if the given GitHub repo (in "owner/name" form,
// optionally with a trailing ".git") is in the AllowedGitHubRepos allowlist.
// If the allowlist is empty, all repos are allowed (legacy behavior —
// NOT recommended for production; set the allowlist via
// SENTINEL_ALLOWED_GITHUB_REPOS=owner1/repo1,owner2/repo2).
//
// v3.8.4: This is the primary defense against the SSRF-style attack where a
// user with pod-edit permission annotates their pod with an arbitrary repo
// string and Sentinel dutifully clones it (sending the GitHub PAT to
// github.com for any repo the PAT can access). With the allowlist set, only
// explicitly-listed repos can be cloned.
//
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

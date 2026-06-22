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

	// Kubernetes
	Namespace   string // the namespace Sentinel runs in
	ReleaseName string // the helm release name

	// GitHub
	GitHubToken string

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
		Namespace:              getenv("SENTINEL_NAMESPACE", "sentinel"),
		ReleaseName:            getenv("SENTINEL_RELEASE_NAME", "sentinel"),
		GitHubToken:            getenv("SENTINEL_GITHUB_TOKEN", ""),
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

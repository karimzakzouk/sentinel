// Package runtimecfg provides hot-reloadable runtime configuration.
// v3.8.0: Settings changed via the web UI are persisted to /data/runtime-config.json
// and applied immediately — no restart needed.
package runtimecfg

import (
        "encoding/json"
        "fmt"
        "log"
        "os"
        "sync"
)

// RuntimeConfig holds editable settings. Only non-empty fields override env vars.
type RuntimeConfig struct {
        LLMProvider       string `json:"llm_provider,omitempty"`
        LLMBaseURL        string `json:"llm_base_url,omitempty"`
        LLMAPIKey         string `json:"llm_api_key,omitempty"`
        LLMFastModel      string `json:"llm_fast_model,omitempty"`
        LLMFrontierModel  string `json:"llm_frontier_model,omitempty"`
        LLMEmbeddingModel string `json:"llm_embedding_model,omitempty"`

        InvestigatorTimeoutSec      int     `json:"investigator_timeout_sec,omitempty"`
        EvidenceGathererTimeoutSec  int     `json:"evidence_gatherer_timeout_sec,omitempty"`
        AnalystTimeoutSec           int     `json:"analyst_timeout_sec,omitempty"`
        FixProposerTimeoutSec       int     `json:"fix_proposer_timeout_sec,omitempty"`
        ConfidenceLoopBackThreshold float64 `json:"confidence_loop_back_threshold,omitempty"`
        LLMMaxRetries               int     `json:"llm_max_retries,omitempty"`
        LLMRetryBaseDelayMs         int     `json:"llm_retry_base_delay_ms,omitempty"`
        SkipSandboxNonProd          *bool   `json:"skip_sandbox_non_prod,omitempty"`

        PrometheusURL string `json:"prometheus_url,omitempty"`
        LokiURL       string `json:"loki_url,omitempty"`
        JaegerURL     string `json:"jaeger_url,omitempty"`

        GitHubAppID         string `json:"github_app_id,omitempty"`
        GitHubAppPrivateKey string `json:"github_app_private_key,omitempty"`
        GitHubAppInstallID  string `json:"github_app_install_id,omitempty"`

        LogLevel string `json:"log_level,omitempty"`
}

// Manager handles loading/saving runtime config.
type Manager struct {
        mu       sync.RWMutex
        config   *RuntimeConfig
        filePath string
}

// New creates a Manager that reads from filePath.
func New(filePath string) *Manager {
        m := &Manager{filePath: filePath, config: &RuntimeConfig{}}
        m.Load()
        return m
}

func (m *Manager) Load() {
        m.mu.Lock()
        defer m.mu.Unlock()
        data, err := os.ReadFile(m.filePath)
        if err != nil {
                if !os.IsNotExist(err) {
                        log.Printf("[runtimecfg] read error: %v", err)
                }
                return
        }
        var cfg RuntimeConfig
        if err := json.Unmarshal(data, &cfg); err != nil {
                log.Printf("[runtimecfg] parse error: %v", err)
                return
        }
        m.config = &cfg
        log.Printf("[runtimecfg] loaded from %s (%d overrides)", m.filePath, countOverrides(&cfg))
}

func (m *Manager) Save(cfg *RuntimeConfig) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        data, err := json.MarshalIndent(cfg, "", "  ")
        if err != nil {
                return err
        }
        if err := os.WriteFile(m.filePath, data, 0600); err != nil {
                return err
        }
        m.config = cfg
        log.Printf("[runtimecfg] saved to %s (%d overrides)", m.filePath, countOverrides(cfg))
        return nil
}

// SaveFromJSON saves config from raw JSON bytes (implements api.RuntimeCfgSaver).
func (m *Manager) SaveFromJSON(jsonBytes []byte) error {
        var cfg RuntimeConfig
        if err := json.Unmarshal(jsonBytes, &cfg); err != nil {
                return fmt.Errorf("parse JSON: %w", err)
        }
        return m.Save(&cfg)
}

func (m *Manager) Get() *RuntimeConfig {
        m.mu.RLock()
        defer m.mu.RUnlock()
        if m.config == nil {
                return &RuntimeConfig{}
        }
        return m.config
}

func countOverrides(c *RuntimeConfig) int {
        n := 0
        if c.LLMProvider != "" { n++ }
        if c.LLMBaseURL != "" { n++ }
        if c.LLMAPIKey != "" { n++ }
        if c.LLMFastModel != "" { n++ }
        if c.LLMFrontierModel != "" { n++ }
        if c.LLMEmbeddingModel != "" { n++ }
        if c.PrometheusURL != "" { n++ }
        if c.LokiURL != "" { n++ }
        if c.JaegerURL != "" { n++ }
        if c.GitHubAppID != "" { n++ }
        if c.GitHubAppPrivateKey != "" { n++ }
        if c.GitHubAppInstallID != "" { n++ }
        if c.LogLevel != "" { n++ }
        if c.InvestigatorTimeoutSec > 0 { n++ }
        if c.EvidenceGathererTimeoutSec > 0 { n++ }
        if c.AnalystTimeoutSec > 0 { n++ }
        if c.FixProposerTimeoutSec > 0 { n++ }
        if c.ConfidenceLoopBackThreshold > 0 { n++ }
        if c.LLMMaxRetries > 0 { n++ }
        if c.LLMRetryBaseDelayMs > 0 { n++ }
        if c.SkipSandboxNonProd != nil { n++ }
        return n
}

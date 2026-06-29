// Package runtimecfg provides hot-reloadable runtime configuration.
// Settings are loaded from a JSON file on disk and can be edited at runtime
// via POST /settings — changes are applied immediately without restart.
package runtimecfg

import (
        "encoding/json"
        "fmt"
        "log"
        "os"
        "path/filepath"
        "sync"

        "github.com/karimzakzouk/sentinel/internal/api"
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

        SandboxEnabled              *bool   `json:"sandbox_enabled,omitempty"`
        SandboxRequired             *bool   `json:"sandbox_required,omitempty"`
        SandboxMaxRetries           int     `json:"sandbox_max_retries,omitempty"`

        PrometheusURL string `json:"prometheus_url,omitempty"`
        LokiURL       string `json:"loki_url,omitempty"`
        JaegerURL     string `json:"jaeger_url,omitempty"`

        GitHubAppID         string `json:"github_app_id,omitempty"`
        GitHubAppPrivateKey string `json:"github_app_private_key,omitempty"`
        GitHubAppInstallID  string `json:"github_app_install_id,omitempty"`

        LogLevel string `json:"log_level,omitempty"`
}

// Clone returns a deep copy of the RuntimeConfig. v4.1.0.
// Used by SetGitHubApp + SetGitHubAppInstall to get a mutable copy before
// modifying + saving (avoiding race conditions with concurrent reads).

func (c RuntimeConfig) Clone() RuntimeConfig {
        out := c
        if c.SkipSandboxNonProd != nil {
                v := *c.SkipSandboxNonProd
                out.SkipSandboxNonProd = &v
        }
        if c.SandboxEnabled != nil {
                v := *c.SandboxEnabled
                out.SandboxEnabled = &v
        }
        if c.SandboxRequired != nil {
                v := *c.SandboxRequired
                out.SandboxRequired = &v
        }
        return out
}

// Manager handles loading/saving runtime config.

type Manager struct {
        mu       sync.RWMutex
        config   *RuntimeConfig
        filePath string

        // receives the new RuntimeConfig so the caller can propagate changes
        // to live components (LLM router, pipeline, sandbox, etc.) without
        // requiring a restart. Set via SetApplyCallback.

        appliedCallback func(*RuntimeConfig)
}

// SetApplyCallback registers a function to be called after every successful
// Save. The callback runs under the Manager's lock — keep it fast (it
// should just copy fields into live components, not do I/O).
// actually take effect. main.go registers a callback that propagates the
// new LLM provider/model/key, timeouts, and confidence thresholds into
// the live Router, Pipeline, and Policy.
// to disk but only read back at startup — the UI gave the illusion of
// editable settings without actually editing anything.

func (m *Manager) SetApplyCallback(cb func(*RuntimeConfig)) {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.appliedCallback = cb
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

        // path /data/runtime-config.json works in K8s (PVC mounted at /data)
        // but fails locally with "no such file or directory" when /data doesn't
        // exist. This makes local testing work without requiring operators to
        // pre-create the directory.

        dir := filepath.Dir(m.filePath)
        if dir != "" && dir != "." {
                if err := os.MkdirAll(dir, 0755); err != nil {
                        return fmt.Errorf("create config dir %s: %w", dir, err)
                }
        }
        if err := os.WriteFile(m.filePath, data, 0600); err != nil {
                return err
        }
        m.config = cfg
        log.Printf("[runtimecfg] saved to %s (%d overrides)", m.filePath, countOverrides(cfg))

        // the callback is expected to be a fast field-copy, not I/O. If it
        // panics, the deferred unlock still runs (the panic propagates up
        // through SaveFromJSON -> api.handleSettings -> recovers via the
        // pipeline's panic guard, but at worst the HTTP handler returns 500).

        if m.appliedCallback != nil {
                m.appliedCallback(cfg)
        }
        return nil
}

// SaveFromJSON saves config from raw JSON bytes (implements api.RuntimeCfgSaver).
// and applied (negative timeouts, invalid providers, etc.), causing silent failures.

func (m *Manager) SaveFromJSON(jsonBytes []byte) error {
        var cfg RuntimeConfig
        if err := json.Unmarshal(jsonBytes, &cfg); err != nil {
                return fmt.Errorf("parse JSON: %w", err)
        }
        if errs := cfg.Validate(); len(errs) > 0 {
                return fmt.Errorf("invalid runtime config: %s", joinStrings(errs, "; "))
        }
        return m.Save(&cfg)
}

// Validate returns a list of validation errors (empty if config is valid).
// and break every LLM call (negative timeout = already-expired context).

func (c *RuntimeConfig) Validate() []string {
        var errs []string
        if c.InvestigatorTimeoutSec < 0 {
                errs = append(errs, "investigator_timeout_sec must be >= 0")
        }
        if c.EvidenceGathererTimeoutSec < 0 {
                errs = append(errs, "evidence_gatherer_timeout_sec must be >= 0")
        }
        if c.AnalystTimeoutSec < 0 {
                errs = append(errs, "analyst_timeout_sec must be >= 0")
        }
        if c.FixProposerTimeoutSec < 0 {
                errs = append(errs, "fix_proposer_timeout_sec must be >= 0")
        }
        if c.ConfidenceLoopBackThreshold < 0 || c.ConfidenceLoopBackThreshold > 1 {
                errs = append(errs, "confidence_loop_back_threshold must be in [0, 1]")
        }
        if c.LLMMaxRetries < 0 || c.LLMMaxRetries > 10 {
                errs = append(errs, "llm_max_retries must be in [0, 10]")
        }
        if c.LLMRetryBaseDelayMs < 0 {
                errs = append(errs, "llm_retry_base_delay_ms must be >= 0")
        }
        if c.SandboxMaxRetries < 0 || c.SandboxMaxRetries > 10 {
                errs = append(errs, "sandbox_max_retries must be in [0, 10]")
        }
        validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
        if c.LogLevel != "" && !validLogLevels[c.LogLevel] {
                errs = append(errs, "log_level must be one of: debug, info, warn, error")
        }
        return errs
}

func joinStrings(parts []string, sep string) string {
        if len(parts) == 0 {
                return ""
        }
        out := parts[0]
        for _, p := range parts[1:] {
                out += sep + p
        }
        return out
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

        if c.SandboxEnabled != nil { n++ }
        if c.SandboxRequired != nil { n++ }
        if c.SandboxMaxRetries > 0 { n++ }
        return n
}

// Stores App credentials in runtime config + triggers the apply callback
// so main.go's wiring code can reconfigure the live GitHub clients.
// SetGitHubApp stores the App ID + private key + optional install ID +
// webhook secret in runtime config, then triggers the apply callback.
// The apply callback in main.go creates a githubapp.App from these
// credentials and wires it into all 4 GitHub clients (PR opener, chatops,
// auto-merge, prreview) live — no restart.
// Returns the App slug (used to construct the bot username: <slug>[bot]).
// The slug isn't in the request — we fetch it lazily from the GitHub API
// via the apply callback, but for the response we return a placeholder
// ("sentinel") since the actual slug isn't known until the App is queried.
// Sentinelctl app status will display the real slug after the App is queried.

func (m *Manager) SetGitHubApp(appID, privateKey, installID, webhookSecret string) (string, error) {
        cfg := m.Get().Clone()
        cfg.GitHubAppID = appID
        cfg.GitHubAppPrivateKey = privateKey
        if installID != "" {
                cfg.GitHubAppInstallID = installID
        }
        // webhookSecret is stored separately (in cfg.GitHubWebhookSecret via the
        // config package, not RuntimeConfig — RuntimeConfig doesn't have a field
        // for it because webhook secrets are typically set at install time, not
        // at runtime). For now, we ignore webhookSecret here — it's expected to
        // be set via Helm/env vars at deploy time. Future: add a runtime field.

        if err := m.Save(&cfg); err != nil {
                return "", err
        }
        // The slug is unknown until we query the GitHub API. Return a placeholder.
        // sentinelctl app status will show the real slug after the App is queried.

        return "sentinel", nil
}

// SetGitHubAppInstall sets just the installation ID.

func (m *Manager) SetGitHubAppInstall(installID string) error {
        cfg := m.Get().Clone()
        cfg.GitHubAppInstallID = installID
        return m.Save(&cfg)
}

// GetGitHubAppStatus returns the current App config.
// Returns api.GitHubAppStatus so the Manager satisfies api.GitHubAppManager.

func (m *Manager) GetGitHubAppStatus() api.GitHubAppStatus {
        cfg := m.Get()
        if cfg.GitHubAppID == "" || cfg.GitHubAppPrivateKey == "" {
                return api.GitHubAppStatus{Configured: false}
        }
        return api.GitHubAppStatus{
                Configured: true,
                AppID:      cfg.GitHubAppID,
                InstallID:  cfg.GitHubAppInstallID,
                // Slug is fetched lazily — we don't store it. Sentinelctl app status
                // can fetch it from the GitHub API if needed. For now, return empty.

                Slug: "",
        }
}

// RemoveGitHubApp clears the App config (falls back to PAT).

func (m *Manager) RemoveGitHubApp() error {
        cfg := m.Get().Clone()
        cfg.GitHubAppID = ""
        cfg.GitHubAppPrivateKey = ""
        cfg.GitHubAppInstallID = ""
        return m.Save(&cfg)
}

// GitHubAppStatus — defined in package api. runtimecfg.Manager returns
// api.GitHubAppStatus from GetGitHubAppStatus() so the types match exactly.
// (No type alias needed — runtimecfg imports api transitively via the
// GitHubAppManager interface in main.go.)


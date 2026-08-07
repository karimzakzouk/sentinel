package runtimecfg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// RuntimeConfig holds editable settings. Only non-empty fields override
// env-var defaults at startup — empty fields leave the env-var value intact.
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

	SandboxEnabled    *bool `json:"sandbox_enabled,omitempty"`
	SandboxRequired   *bool `json:"sandbox_required,omitempty"`
	SandboxMaxRetries int   `json:"sandbox_max_retries,omitempty"`

	PrometheusURL string `json:"prometheus_url,omitempty"`
	LokiURL       string `json:"loki_url,omitempty"`
	JaegerURL     string `json:"jaeger_url,omitempty"`

	GitHubAppID         string `json:"github_app_id,omitempty"`
	GitHubAppPrivateKey string `json:"github_app_private_key,omitempty"`
	GitHubAppInstallID  string `json:"github_app_install_id,omitempty"`

	LogLevel string `json:"log_level,omitempty"`

	// ConfidenceAutonomous and ConfidenceHumanReview hot-reload the
	// governance policy thresholds without a restart.
	ConfidenceAutonomous  float64 `json:"confidence_threshold_autonomous,omitempty"`
	ConfidenceHumanReview float64 `json:"confidence_threshold_human_review,omitempty"`

	// ProductionNamespaces and WriteNamespaces hot-reload the
	// auto-merge + safe-actions namespace allowlists.
	ProductionNamespaces string `json:"production_namespaces,omitempty"`
	WriteNamespaces      string `json:"write_namespaces,omitempty"`
}

// Clone returns a deep copy of the RuntimeConfig. Used by SetGitHubApp and
// SetGitHubAppInstall to take a mutable copy before modifying + saving
// (avoiding races with concurrent reads).
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

// Manager handles loading, saving, and applying runtime config. It is the
// single source of truth for any setting that can change without a restart.
type Manager struct {
	mu       sync.RWMutex
	config   *RuntimeConfig
	filePath string

	// appliedCallback is invoked after every successful Save so the caller
	// can propagate changes to live components (LLM router, clusterer,
	// policy). Set via SetApplyCallback.
	appliedCallback func(*RuntimeConfig)
}

// SetApplyCallback registers a function to be called after every successful
// Save. The callback runs under the Manager's lock — keep it fast (it
// should just copy fields into live components, not do I/O).
func (m *Manager) SetApplyCallback(cb func(*RuntimeConfig)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appliedCallback = cb
}

// New creates a Manager that reads from filePath. The file is read
// immediately; a missing file is not an error (an empty config is used).
func New(filePath string) *Manager {
	m := &Manager{filePath: filePath, config: &RuntimeConfig{}}
	m.Load()
	return m
}

// Load reads the config file from disk and swaps it into the Manager. A
// missing file is silent (leaves the existing config intact); a parse
// error is logged but does not overwrite the in-memory config.
func (m *Manager) Load() {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Error("runtimecfg: read error", "err", err, "path", m.filePath)
		}
		return
	}
	var cfg RuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		logging.Error("runtimecfg: parse error", "err", err, "path", m.filePath)
		return
	}
	m.config = &cfg
	logging.Info("runtimecfg: loaded config", "path", m.filePath, "overrides", countOverrides(&cfg))
}

// Save writes cfg to disk and swaps it into the Manager. The apply
// callback (if set) is invoked under the lock so callers see a consistent
// view. The parent directory is created on demand so local dev works
// without pre-creating /data.
func (m *Manager) Save(cfg *RuntimeConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(m.filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create config dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(m.filePath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	m.config = cfg
	logging.Info("runtimecfg: saved config", "path", m.filePath, "overrides", countOverrides(cfg))

	if m.appliedCallback != nil {
		m.appliedCallback(cfg)
	}
	return nil
}

// SaveFromJSON saves config from raw JSON bytes. It implements the
// api.RuntimeCfgSaver interface. Validation runs before Save so invalid
// configs are rejected before reaching disk.
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
// Negative timeouts and out-of-range thresholds are rejected so a typo in
// POST /settings cannot silently break every LLM call (negative timeout =
// already-expired context).
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
	if c.ConfidenceAutonomous < 0 || c.ConfidenceAutonomous > 1 {
		errs = append(errs, "confidence_threshold_autonomous must be in [0, 1]")
	}
	if c.ConfidenceHumanReview < 0 || c.ConfidenceHumanReview > 1 {
		errs = append(errs, "confidence_threshold_human_review must be in [0, 1]")
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

// Get returns a pointer to the current config. Callers must not mutate the
// returned struct; use Save for that.
func (m *Manager) Get() *RuntimeConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config == nil {
		return &RuntimeConfig{}
	}
	return m.config
}

// countOverrides returns the number of non-empty/non-zero fields — useful
// as a quick "how much of the env-var defaults did this config override"
// signal in logs.
func countOverrides(c *RuntimeConfig) int {
	n := 0
	if c.LLMProvider != "" {
		n++
	}
	if c.LLMBaseURL != "" {
		n++
	}
	if c.LLMAPIKey != "" {
		n++
	}
	if c.LLMFastModel != "" {
		n++
	}
	if c.LLMFrontierModel != "" {
		n++
	}
	if c.LLMEmbeddingModel != "" {
		n++
	}
	if c.PrometheusURL != "" {
		n++
	}
	if c.LokiURL != "" {
		n++
	}
	if c.JaegerURL != "" {
		n++
	}
	if c.GitHubAppID != "" {
		n++
	}
	if c.GitHubAppPrivateKey != "" {
		n++
	}
	if c.GitHubAppInstallID != "" {
		n++
	}
	if c.LogLevel != "" {
		n++
	}
	if c.InvestigatorTimeoutSec > 0 {
		n++
	}
	if c.EvidenceGathererTimeoutSec > 0 {
		n++
	}
	if c.AnalystTimeoutSec > 0 {
		n++
	}
	if c.FixProposerTimeoutSec > 0 {
		n++
	}
	if c.ConfidenceLoopBackThreshold > 0 {
		n++
	}
	if c.ConfidenceAutonomous > 0 {
		n++
	}
	if c.ConfidenceHumanReview > 0 {
		n++
	}
	if c.ProductionNamespaces != "" {
		n++
	}
	if c.WriteNamespaces != "" {
		n++
	}
	if c.LLMMaxRetries > 0 {
		n++
	}
	if c.LLMRetryBaseDelayMs > 0 {
		n++
	}
	if c.SkipSandboxNonProd != nil {
		n++
	}
	if c.SandboxEnabled != nil {
		n++
	}
	if c.SandboxRequired != nil {
		n++
	}
	if c.SandboxMaxRetries > 0 {
		n++
	}
	return n
}

// --- GitHub App management ---

// SetGitHubApp stores the App ID + private key + optional install ID in
// runtime config, then triggers the apply callback so main.go's wiring
// code can reconfigure the live GitHub clients. Returns the App slug
// (used to construct the bot username: <slug>[bot]). The slug is fetched
// lazily from the GitHub API by the apply callback; here we return a
// placeholder so the HTTP response is immediate. sentinelctl app status
// displays the real slug after the App is queried.
func (m *Manager) SetGitHubApp(appID, privateKey, installID, webhookSecret string) (string, error) {
	cfg := m.Get().Clone()
	cfg.GitHubAppID = appID
	cfg.GitHubAppPrivateKey = privateKey
	if installID != "" {
		cfg.GitHubAppInstallID = installID
	}
	// webhookSecret is intentionally not stored in RuntimeConfig. Webhook
	// secrets are typically set at install time via Helm/env vars — runtime
	// mutation of the secret would invalidate existing Alertmanager configs
	// silently. Future: add a runtime field if operators ask for it.
	_ = webhookSecret

	if err := m.Save(&cfg); err != nil {
		return "", err
	}
	return "sentinel", nil
}

// SetGitHubAppInstall sets just the installation ID. Called separately
// from SetGitHubApp because the install ID isn't known until the user
// installs the App on their repo (which happens after credentials are
// stored).
func (m *Manager) SetGitHubAppInstall(installID string) error {
	cfg := m.Get().Clone()
	cfg.GitHubAppInstallID = installID
	return m.Save(&cfg)
}

// GetGitHubAppStatus returns the current App config. Returns
// schemas.GitHubAppStatus so the Manager satisfies api.GitHubAppManager.
func (m *Manager) GetGitHubAppStatus() schemas.GitHubAppStatus {
	cfg := m.Get()
	if cfg.GitHubAppID == "" || cfg.GitHubAppPrivateKey == "" {
		return schemas.GitHubAppStatus{Configured: false}
	}
	return schemas.GitHubAppStatus{
		Configured: true,
		AppID:      cfg.GitHubAppID,
		InstallID:  cfg.GitHubAppInstallID,
		// Slug is fetched lazily from the GitHub API; sentinelctl app
		// status can populate it. We return empty here so the API
		// response is immediate.
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

// --- K8s ConfigMap poller ---

// ConfigMapSource is the K8s ConfigMap location to poll for runtime config
// changes. The ConfigMap's data.runtime-config.json key holds the JSON
// payload; if absent, the data map is walked for any *.json key.
type ConfigMapSource struct {
	Name      string // ConfigMap name.
	Namespace string // ConfigMap namespace.
	Key       string // data key (defaults to "runtime-config.json").
}

// RunConfigMapPoller starts a background goroutine that polls the named
// ConfigMap every 10 seconds and applies any changes via SaveFromJSON —
// same code path as POST /settings, so the apply callback fires on every
// change. Exits when ctx is cancelled. Returns immediately.
//
// This is the K8s-native alternative to POST /settings: operators can
// `kubectl edit configmap sentinel-runtime -n sentinel` and the change
// applies without a restart.
func (m *Manager) RunConfigMapPoller(ctx context.Context, client *k8s.Client, src ConfigMapSource) {
	if client == nil {
		logging.Warn("runtimecfg: nil k8s client — ConfigMap poller disabled")
		return
	}
	if src.Key == "" {
		src.Key = "runtime-config.json"
	}
	cs := client.Clientset()
	go m.configMapPollLoop(ctx, cs, src)
}

func (m *Manager) configMapPollLoop(ctx context.Context, cs kubernetes.Interface, src ConfigMapSource) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastChecksum string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cs, err := pollConfigMapOnce(ctx, cs, src)
			if err != nil {
				logging.Info("runtimecfg: ConfigMap poll error", "err", err)
				continue
			}
			if cs == lastChecksum {
				continue
			}
			lastChecksum = cs
			// SaveFromJSON triggers the apply callback.
			if err := m.SaveFromJSON([]byte(cs)); err != nil {
				logging.Info("runtimecfg: apply ConfigMap update failed", "err", err)
			} else {
				logging.Info("runtimecfg: applied ConfigMap update", "name", src.Name, "namespace", src.Namespace)
			}
		}
	}
}

// pollConfigMapOnce reads the ConfigMap once and returns the JSON payload
// from src.Key. The string return is used as a checksum so the poller can
// skip no-op re-applies.
func pollConfigMapOnce(ctx context.Context, cs kubernetes.Interface, src ConfigMapSource) (string, error) {
	if cs == nil {
		return "", fmt.Errorf("nil kubernetes client")
	}
	cm, err := cs.CoreV1().ConfigMaps(src.Namespace).Get(ctx, src.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get configmap %s/%s: %w", src.Namespace, src.Name, err)
	}
	if data, ok := cm.Data[src.Key]; ok {
		return data, nil
	}
	// Fall back to any *.json key so operators can name the key whatever
	// they want.
	for k, v := range cm.Data {
		if len(k) > 5 && k[len(k)-5:] == ".json" {
			return v, nil
		}
	}
	return "", fmt.Errorf("configmap %s/%s has no key %q", src.Namespace, src.Name, src.Key)
}

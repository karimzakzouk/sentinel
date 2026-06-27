// Package llm — provider pool with priority-ordered failover + circuit breaker.
//
// v4.0.0: Replaces the single-provider Router config with a multi-provider pool.
// Operators can stack multiple API keys (e.g. 3 OpenRouter keys + 2 Groq + 1 Gemini)
// and Sentinel will try them in priority order. If a provider fails (auth error,
// rate limit, timeout, network), the circuit breaker trips for that provider
// and subsequent calls skip it until the reset window expires.
//
// Design goals:
//   - Zero-config backwards compat: if no providers are configured, the pool
//     degrades to a single-provider wrapper around the existing cfg.LLMProvider
//     + cfg.LLMAPIKey fields. Existing installs keep working unchanged.
//   - Honest failover: a provider is only marked healthy after a successful
//     call. Failures are categorized (auth = permanent, rate-limit = transient,
//     network = transient) and the breaker delay is set accordingly.
//   - No starvation: when all providers are tripped, the pool returns the
//     highest-priority one anyway (better to try and fail than to refuse).
//   - Observable: pool state is exposed via PoolStatus() for the web UI +
//     metrics for tripped breakers, failover count, per-provider call counts.
package llm

import (
        "context"
        "errors"
        "fmt"
        "log"
        "strings"
        "sync"
        "time"
)

// ProviderConfig describes one LLM provider entry in the pool.
// Operators stack multiple of these via the sentinel.llm.providers Helm value
// or the SENTINEL_LLM_PROVIDERS env var (JSON array).
type ProviderConfig struct {
        // ID is a unique identifier for this provider entry. Used in logs, metrics,
        // and the web UI. If empty, one is generated from ProviderType + index.
        ID string `json:"id" yaml:"id"`

        // ProviderType is the GoAI provider name: openai, anthropic, bedrock, azure,
        // google, gemini, vertex, openrouter, groq, mistral, cohere, xai, deepseek,
        // cerebras, fireworks, together, perplexity, ollama, vllm, zai, custom, litellm.
        ProviderType string `json:"provider" yaml:"provider"`

        // BaseURL is the OpenAI-compatible base URL. Optional — most native providers
        // derive it automatically. Used for custom, litellm, ollama, vllm, zai.
        BaseURL string `json:"baseUrl,omitempty" yaml:"baseUrl,omitempty"`

        // APIKey is the provider's API key. Required for all providers except ollama.
        APIKey string `json:"apiKey" yaml:"apiKey"`

        // FastModel is the cheaper/faster model used for evidence gathering, error
        // parsing, etc. If empty, inherits from the global default.
        FastModel string `json:"fastModel,omitempty" yaml:"fastModel,omitempty"`

        // FrontierModel is the more capable model used for investigation, fix
        // generation. If empty, inherits from the global default.
        FrontierModel string `json:"frontierModel,omitempty" yaml:"frontierModel,omitempty"`

        // Priority is the failover order — lower = tried first. Default = index+1.
        Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`

        // Weight is used for load-balancing among providers with the same priority.
        // Default 1. Higher weight = more traffic. (Reserved for future use —
        // current implementation is strict priority, not weighted round-robin.)
        Weight int `json:"weight,omitempty" yaml:"weight,omitempty"`

        // Enabled controls whether this provider is considered. Default true.
        // Set to false to drain a provider without removing it from config.
        Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

        // BedrockRegion is the AWS region for bedrock providers. Ignored otherwise.
        BedrockRegion string `json:"bedrockRegion,omitempty" yaml:"bedrockRegion,omitempty"`
}

// IsEnabled returns true unless Enabled is explicitly false.
func (c *ProviderConfig) IsEnabled() bool {
        return c.Enabled == nil || *c.Enabled
}

// providerHealth is the circuit-breaker state for one provider.
type providerHealth struct {
        mu                  sync.Mutex
        consecutiveFailures int
        lastFailure         time.Time
        lastFailureReason   string
        trippedUntil        time.Time
        totalCalls          int64
        totalSuccesses      int64
        totalFailures       int64
}

// trip marks the provider as tripped until the given reset time.
func (h *providerHealth) trip(until time.Time, reason string) {
        h.mu.Lock()
        defer h.mu.Unlock()
        h.trippedUntil = until
        h.lastFailure = time.Now()
        h.lastFailureReason = reason
}

// isTripped returns true if the circuit breaker is currently open.
func (h *providerHealth) isTripped() bool {
        h.mu.Lock()
        defer h.mu.Unlock()
        return time.Now().Before(h.trippedUntil)
}

// recordSuccess resets the failure counter and clears the tripped state.
func (h *providerHealth) recordSuccess() {
        h.mu.Lock()
        defer h.mu.Unlock()
        h.consecutiveFailures = 0
        h.trippedUntil = time.Time{}
        h.totalCalls++
        h.totalSuccesses++
}

// recordFailure increments the failure counter and returns the new count.
func (h *providerHealth) recordFailure(reason string) int {
        h.mu.Lock()
        defer h.mu.Unlock()
        h.consecutiveFailures++
        h.totalCalls++
        h.totalFailures++
        h.lastFailure = time.Now()
        h.lastFailureReason = reason
        return h.consecutiveFailures
}

// Pool manages a stack of LLM providers with priority-ordered failover.
type Pool struct {
        mu        sync.RWMutex
        providers []*ProviderConfig
        health    map[string]*providerHealth

        // retryConfig is shared with Router — same backoff behavior.
        maxRetries     int
        retryBaseDelay time.Duration
}

// NewPool creates a pool from the given provider configs.
// If the slice is empty, the pool is empty and Acquire will return nil —
// callers should fall back to the legacy single-provider config in that case.
func NewPool(configs []ProviderConfig) *Pool {
        p := &Pool{
                health: make(map[string]*providerHealth),
        }
        for i := range configs {
                c := configs[i] // copy
                if c.ID == "" {
                        c.ID = fmt.Sprintf("%s-%d", c.ProviderType, i+1)
                }
                if c.Priority == 0 {
                        c.Priority = i + 1
                }
                p.providers = append(p.providers, &c)
                p.health[c.ID] = &providerHealth{}
        }
        return p
}

// SetRetryConfig propagates retry settings from the Router.
func (p *Pool) SetRetryConfig(maxRetries int, baseDelay time.Duration) {
        p.mu.Lock()
        defer p.mu.Unlock()
        p.maxRetries = maxRetries
        p.retryBaseDelay = baseDelay
}

// HasProviders returns true if the pool has at least one configured provider.
func (p *Pool) HasProviders() bool {
        p.mu.RLock()
        defer p.mu.RUnlock()
        return len(p.providers) > 0
}

// Acquire returns the next healthy provider in priority order.
// If all providers are tripped, returns the highest-priority one anyway
// (better to try and fail than to refuse).
// Returns nil if the pool is empty.
func (p *Pool) Acquire() *ProviderConfig {
        p.mu.RLock()
        defer p.mu.RUnlock()

        if len(p.providers) == 0 {
                return nil
        }

        // Sort by priority (stable — preserves config order for ties).
        // We don't actually sort the slice; we walk it in priority order.
        // Build a priority-ordered view.
        ordered := make([]*ProviderConfig, len(p.providers))
        copy(ordered, p.providers)
        // stable insertion sort by priority (small N, no need for sort.Slice)
        for i := 1; i < len(ordered); i++ {
                for j := i; j > 0 && ordered[j].Priority < ordered[j-1].Priority; j-- {
                        ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
                }
        }

        // First pass: find the first healthy + enabled provider.
        for _, c := range ordered {
                if !c.IsEnabled() {
                        continue
                }
                h := p.health[c.ID]
                if h == nil || !h.isTripped() {
                        return c
                }
        }

        // Second pass: everything is tripped. Return the highest-priority enabled one.
        for _, c := range ordered {
                if c.IsEnabled() {
                        log.Printf("[llm.pool] all providers tripped — forcing use of %s (priority %d)", c.ID, c.Priority)
                        return c
                }
        }

        return nil
}

// MarkSuccess resets the circuit breaker for the given provider.
func (p *Pool) MarkSuccess(providerID string) {
        p.mu.RLock()
        h := p.health[providerID]
        p.mu.RUnlock()
        if h != nil {
                h.recordSuccess()
        }
}

// MarkFailed records a failure and trips the breaker if the threshold is hit.
// The duration of the trip depends on the failure category:
//   - auth error (401/403/invalid key): permanent — 1 hour. Operator must fix.
//   - rate limit (429): transient — 60s. Resets automatically.
//   - timeout / network: transient — 15s. Likely flaky.
//   - other: transient — 30s. Unknown cause.
func (p *Pool) MarkFailed(providerID string, err error) {
        p.mu.RLock()
        h := p.health[providerID]
        p.mu.RUnlock()
        if h == nil {
                return
        }

        reason := categorizeError(err)
        count := h.recordFailure(reason)

        var tripDuration time.Duration
        switch reason {
        case "auth":
                tripDuration = 1 * time.Hour
        case "rate_limit":
                tripDuration = 60 * time.Second
        case "timeout", "network":
                tripDuration = 15 * time.Second
        default:
                tripDuration = 30 * time.Second
        }

        // Only trip after 2 consecutive failures (avoid tripping on a single flake).
        // Auth errors trip immediately — a bad key won't fix itself.
        if reason == "auth" || count >= 2 {
                h.trip(time.Now().Add(tripDuration), reason)
                log.Printf("[llm.pool] provider %s tripped for %v (%s, consecutive failures: %d)",
                        providerID, tripDuration, reason, count)
        }
}

// categorizeError classifies an error into a bucket for breaker timing.
func categorizeError(err error) string {
        if err == nil {
                return ""
        }
        msg := strings.ToLower(err.Error())
        // Order matters: check more specific patterns before generic ones.
        // "i/o timeout" contains "timeout" but is a network error, not a context deadline.
        switch {
        case strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized") ||
                strings.Contains(msg, "403") || strings.Contains(msg, "forbidden") ||
                strings.Contains(msg, "invalid api key") || strings.Contains(msg, "invalid_api_key") ||
                strings.Contains(msg, "authentication"):
                return "auth"
        case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") ||
                strings.Contains(msg, "rate_limit") || strings.Contains(msg, "too many requests"):
                return "rate_limit"
        case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") ||
                strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "network") ||
                strings.Contains(msg, "dial tcp") || strings.Contains(msg, "eof"):
                return "network"
        case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") ||
                strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "context canceled"):
                return "timeout"
        }
        return "other"
}

// ProviderStatus is the public state of one provider in the pool.
// Used by the web UI's "LLM Providers" panel.
type ProviderStatus struct {
        ID                  string    `json:"id"`
        ProviderType        string    `json:"provider"`
        Priority            int       `json:"priority"`
        Enabled             bool      `json:"enabled"`
        Tripped             bool      `json:"tripped"`
        TrippedUntil        time.Time `json:"trippedUntil,omitempty"`
        LastFailure         time.Time `json:"lastFailure,omitempty"`
        LastFailureReason   string    `json:"lastFailureReason,omitempty"`
        ConsecutiveFailures int       `json:"consecutiveFailures"`
        TotalCalls          int64     `json:"totalCalls"`
        TotalSuccesses      int64     `json:"totalSuccesses"`
        TotalFailures       int64     `json:"totalFailures"`
}

// Status returns the state of every provider in the pool.
func (p *Pool) Status() []ProviderStatus {
        p.mu.RLock()
        defer p.mu.RUnlock()

        out := make([]ProviderStatus, 0, len(p.providers))
        for _, c := range p.providers {
                h := p.health[c.ID]
                if h == nil {
                        continue
                }
                h.mu.Lock()
                status := ProviderStatus{
                        ID:                  c.ID,
                        ProviderType:        c.ProviderType,
                        Priority:            c.Priority,
                        Enabled:             c.IsEnabled(),
                        Tripped:             time.Now().Before(h.trippedUntil),
                        TrippedUntil:        h.trippedUntil,
                        LastFailure:         h.lastFailure,
                        LastFailureReason:   h.lastFailureReason,
                        ConsecutiveFailures: h.consecutiveFailures,
                        TotalCalls:          h.totalCalls,
                        TotalSuccesses:      h.totalSuccesses,
                        TotalFailures:       h.totalFailures,
                }
                h.mu.Unlock()
                out = append(out, status)
        }
        return out
}

// Call executes the given function with failover. The function receives the
// acquired ProviderConfig and should return (result, error). If the function
// returns an error, the pool marks the provider failed and tries the next one.
// Returns ErrAllProvidersFailed if every provider was tried and failed.
//
// This is the core failover primitive. The Router wraps its LLM calls in this.
func (p *Pool) Call(ctx context.Context, fn func(cfg *ProviderConfig) error) error {
        if !p.HasProviders() {
                return errors.New("llm pool is empty — no providers configured")
        }

        // Walk providers in priority order, retrying each up to maxRetries times.
        p.mu.RLock()
        ordered := make([]*ProviderConfig, 0, len(p.providers))
        for _, c := range p.providers {
                if c.IsEnabled() {
                        ordered = append(ordered, c)
                }
        }
        p.mu.RUnlock()

        // stable sort by priority
        for i := 1; i < len(ordered); i++ {
                for j := i; j > 0 && ordered[j].Priority < ordered[j-1].Priority; j-- {
                        ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
                }
        }

        var lastErr error
        tried := make(map[string]bool)

        for _, c := range ordered {
                h := p.health[c.ID]
                if h != nil && h.isTripped() && len(tried) < len(ordered) {
                        // Skip tripped providers unless we've exhausted non-tripped ones.
                        // We'll come back to them in the fallback pass below.
                        continue
                }

                tried[c.ID] = true
                log.Printf("[llm.pool] trying provider %s (priority %d)", c.ID, c.Priority)

                err := fn(c)
                if err == nil {
                        p.MarkSuccess(c.ID)
                        return nil
                }

                lastErr = err
                p.MarkFailed(c.ID, err)
                log.Printf("[llm.pool] provider %s failed: %v — failing over", c.ID, err)

                // If context is cancelled, stop immediately.
                if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
                        return err
                }
        }

        // Fallback pass: everything healthy was tried. Now try tripped providers
        // (better to retry a tripped one than to fail outright).
        for _, c := range ordered {
                if tried[c.ID] {
                        continue
                }
                log.Printf("[llm.pool] fallback: retrying tripped provider %s", c.ID)
                err := fn(c)
                if err == nil {
                        p.MarkSuccess(c.ID)
                        return nil
                }
                lastErr = err
                p.MarkFailed(c.ID, err)
        }

        if lastErr == nil {
                return ErrAllProvidersFailed
        }
        return fmt.Errorf("%w: last error: %v", ErrAllProvidersFailed, lastErr)
}

// ErrAllProvidersFailed is returned when every provider in the pool was tried
// and none succeeded.
var ErrAllProvidersFailed = errors.New("all LLM providers failed")

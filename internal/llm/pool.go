// Package llm provides Sentinel's LLM router, multi-provider failover pool,
// and semantic cache.
//
// The Router picks fast vs frontier model and centralizes the API client.
// The Pool adds priority-ordered failover with a per-provider circuit
// breaker (auth errors trip 1h, rate limits 60s, network 15s, other 30s).
// The SemanticCache serves identical-ish prompts from an HNSW index.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// ProviderConfig describes one LLM provider entry in the pool. It is an
// alias for schemas.ProviderConfig so the config package can populate the
// pool without a circular import.
type ProviderConfig = schemas.ProviderConfig

// providerHealth is the per-provider circuit-breaker state.
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

func (h *providerHealth) trip(until time.Time, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trippedUntil = until
	h.lastFailure = time.Now()
	h.lastFailureReason = reason
}

func (h *providerHealth) isTripped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Before(h.trippedUntil)
}

func (h *providerHealth) recordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecutiveFailures = 0
	h.trippedUntil = time.Time{}
	h.totalCalls++
	h.totalSuccesses++
}

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
	mu             sync.RWMutex
	providers      []*ProviderConfig
	health         map[string]*providerHealth
	maxRetries     int
	retryBaseDelay time.Duration
}

// NewPool returns a pool from configs. Entries with empty ID get a synthetic
// ID ("<provider>-<index>"). Entries with zero Priority get index+1.
func NewPool(configs []ProviderConfig) *Pool {
	p := &Pool{health: make(map[string]*providerHealth)}
	for i := range configs {
		c := configs[i]
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

// HasProviders reports whether the pool has any configured provider.
func (p *Pool) HasProviders() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.providers) > 0
}

// Acquire returns the next healthy, enabled provider in priority order.
// If all providers are tripped, returns the highest-priority enabled one
// (better to try and fail than to refuse). Returns nil if the pool is
// empty or no providers are enabled.
func (p *Pool) Acquire() *ProviderConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.providers) == 0 {
		return nil
	}

	ordered := priorityOrdered(p.providers)

	for _, c := range ordered {
		if !c.IsEnabled() {
			continue
		}
		h := p.health[c.ID]
		if h == nil || !h.isTripped() {
			return c
		}
	}
	for _, c := range ordered {
		if c.IsEnabled() {
			logging.Info("llm pool: all providers tripped, forcing use", "provider", c.ID, "priority", c.Priority)
			return c
		}
	}
	return nil
}

// MarkSuccess resets the circuit breaker for providerID.
func (p *Pool) MarkSuccess(providerID string) {
	p.mu.RLock()
	h := p.health[providerID]
	p.mu.RUnlock()
	if h != nil {
		h.recordSuccess()
	}
}

// MarkFailed records a failure for providerID and trips the breaker if the
// threshold is hit. Trip durations:
//
//   - auth (401/403/invalid key): 1 hour, immediate trip
//   - rate limit (429): 60s, after 2 consecutive failures
//   - timeout / network: 15s, after 2 consecutive failures
//   - other: 30s, after 2 consecutive failures
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

	if reason == "auth" || count >= 2 {
		h.trip(time.Now().Add(tripDuration), reason)
		logging.Warn("llm pool: provider tripped",
			"provider", providerID, "duration", tripDuration, "reason", reason, "consecutive_failures", count)
	}
}

// categorizeError classifies err into a bucket for breaker timing.
// Order matters: more specific patterns are checked before generic ones
// (e.g. "i/o timeout" contains "timeout" but is a network error).
func categorizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
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

// ProviderStatus is the public state of one provider in the pool, exposed
// via Pool.Status() for the web UI.
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
		out = append(out, ProviderStatus{
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
		})
		h.mu.Unlock()
	}
	return out
}

// ErrAllProvidersFailed is returned by Call when every provider was tried
// and none succeeded.
var ErrAllProvidersFailed = errors.New("all LLM providers failed")

// Call executes fn with failover. fn receives the acquired ProviderConfig
// and should return an error on failure; the pool marks the provider failed
// and tries the next one. Returns ErrAllProvidersFailed (wrapping the last
// error) if every provider was tried and failed.
func (p *Pool) Call(ctx context.Context, fn func(cfg *ProviderConfig) error) error {
	if !p.HasProviders() {
		return errors.New("llm pool is empty — no providers configured")
	}

	p.mu.RLock()
	ordered := make([]*ProviderConfig, 0, len(p.providers))
	for _, c := range p.providers {
		if c.IsEnabled() {
			ordered = append(ordered, c)
		}
	}
	p.mu.RUnlock()
	ordered = priorityOrdered(ordered)

	var lastErr error
	tried := make(map[string]bool, len(ordered))

	for _, c := range ordered {
		h := p.health[c.ID]
		if h != nil && h.isTripped() && len(tried) < len(ordered) {
			continue
		}
		tried[c.ID] = true
		logging.Info("llm pool: trying provider", "provider", c.ID, "priority", c.Priority)

		err := fn(c)
		if err == nil {
			p.MarkSuccess(c.ID)
			return nil
		}
		lastErr = err
		p.MarkFailed(c.ID, err)
		logging.Error("llm pool: provider failed, failing over", "provider", c.ID, "err", err)

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}

	// Fallback pass: retry tripped providers rather than failing outright.
	for _, c := range ordered {
		if tried[c.ID] {
			continue
		}
		logging.Info("llm pool: fallback retry of tripped provider", "provider", c.ID)
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

// priorityOrdered returns providers sorted by Priority ascending. Stable
// for ties (preserves config order). Small N — uses insertion sort.
func priorityOrdered(in []*ProviderConfig) []*ProviderConfig {
	out := make([]*ProviderConfig, len(in))
	copy(out, in)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Priority < out[j-1].Priority; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

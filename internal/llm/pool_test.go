package llm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPool_AcquirePriority(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k1", Priority: 3},
		{ID: "b", ProviderType: "openai", APIKey: "k2", Priority: 1},
		{ID: "c", ProviderType: "openai", APIKey: "k3", Priority: 2},
	})
	got := pool.Acquire()
	if got == nil {
		t.Fatal("expected non-nil provider")
	}
	if got.ID != "b" {
		t.Errorf("expected b (priority 1), got %s", got.ID)
	}
}

func TestPool_AcquireEmptyReturnsNil(t *testing.T) {
	pool := NewPool(nil)
	if pool.Acquire() != nil {
		t.Error("expected nil from empty pool")
	}
}

func TestPool_FailoverOnFailure(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "primary", ProviderType: "openai", APIKey: "k1", Priority: 1},
		{ID: "fallback", ProviderType: "openai", APIKey: "k2", Priority: 2},
	})

	var calls int32
	err := pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		atomic.AddInt32(&calls, 1)
		if cfg.ID == "primary" {
			return errors.New("401 unauthorized")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error after failover, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (primary fail -> fallback success), got %d", calls)
	}
}

func TestPool_AllFailReturnsError(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k1", Priority: 1},
		{ID: "b", ProviderType: "openai", APIKey: "k2", Priority: 2},
	})

	err := pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Errorf("expected ErrAllProvidersFailed, got %v", err)
	}
}

func TestPool_CircuitBreakerTripsOnAuth(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "primary", ProviderType: "openai", APIKey: "bad", Priority: 1},
		{ID: "fallback", ProviderType: "openai", APIKey: "good", Priority: 2},
	})

	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		if cfg.ID == "primary" {
			return errors.New("401 unauthorized")
		}
		return nil
	})

	pool.mu.RLock()
	h := pool.health["primary"]
	pool.mu.RUnlock()
	if h == nil {
		t.Fatal("expected health entry for primary")
	}
	if !h.isTripped() {
		t.Error("expected primary to be tripped after auth error")
	}
}

func TestPool_CategorizeError(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{errors.New("401 unauthorized"), "auth"},
		{errors.New("403 forbidden"), "auth"},
		{errors.New("invalid api key"), "auth"},
		{errors.New("429 too many requests"), "rate_limit"},
		{errors.New("rate limit exceeded"), "rate_limit"},
		{errors.New("context deadline exceeded"), "timeout"},
		{errors.New("connection refused"), "network"},
		{errors.New("dial tcp: i/o timeout"), "network"},
		{errors.New("500 internal server error"), "other"},
		{nil, ""},
	}
	for _, tt := range tests {
		got := categorizeError(tt.err)
		if got != tt.expected {
			t.Errorf("categorizeError(%v) = %q, want %q", tt.err, got, tt.expected)
		}
	}
}

func TestPool_Status(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k1", Priority: 1},
		{ID: "b", ProviderType: "groq", APIKey: "k2", Priority: 2},
	})
	status := pool.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 providers in status, got %d", len(status))
	}
	if status[0].ID != "a" || status[1].ID != "b" {
		t.Errorf("unexpected status order: %v", status)
	}
}

func TestPool_SuccessResetsFailureCount(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k1", Priority: 1},
	})

	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	pool.mu.RLock()
	fails := pool.health["a"].consecutiveFailures
	pool.mu.RUnlock()
	if fails != 1 {
		t.Errorf("expected 1 consecutive failure, got %d", fails)
	}

	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return nil
	})
	pool.mu.RLock()
	fails = pool.health["a"].consecutiveFailures
	pool.mu.RUnlock()
	if fails != 0 {
		t.Errorf("expected 0 consecutive failures after success, got %d", fails)
	}
}

func TestPool_DoesNotTripOnSingleFlake(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k1", Priority: 1},
	})

	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	pool.mu.RLock()
	h := pool.health["a"]
	pool.mu.RUnlock()
	if h.isTripped() {
		t.Error("expected breaker NOT tripped on single non-auth failure")
	}
}

func TestPool_BedrockConfig(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{
			ID:            "bedrock-1",
			ProviderType:  "bedrock",
			APIKey:        "AKIA...",
			BedrockRegion: "us-west-2",
			Priority:      1,
		},
	})
	acquired := pool.Acquire()
	if acquired == nil {
		t.Fatal("expected to acquire bedrock provider")
	}
	if acquired.BedrockRegion != "us-west-2" {
		t.Errorf("expected region us-west-2, got %s", acquired.BedrockRegion)
	}
}

func TestPool_DisabledProviderSkipped(t *testing.T) {
	disabled := false
	pool := NewPool([]ProviderConfig{
		{ID: "disabled", ProviderType: "openai", APIKey: "k1", Priority: 1, Enabled: &disabled},
		{ID: "enabled", ProviderType: "openai", APIKey: "k2", Priority: 2},
	})
	acquired := pool.Acquire()
	if acquired == nil {
		t.Fatal("expected to acquire enabled provider")
	}
	if acquired.ID != "enabled" {
		t.Errorf("expected 'enabled', got %s", acquired.ID)
	}
}

func TestPriorityOrdered(t *testing.T) {
	in := []*ProviderConfig{
		{ID: "a", Priority: 3},
		{ID: "b", Priority: 1},
		{ID: "c", Priority: 2},
		{ID: "d", Priority: 1}, // tie with b
	}
	out := priorityOrdered(in)
	if len(out) != 4 {
		t.Fatalf("len = %d", len(out))
	}
	// priority 1 entries first (b, d in original order), then 2, then 3
	if out[0].Priority != 1 || out[1].Priority != 1 {
		t.Errorf("first two should have priority 1, got %d, %d", out[0].Priority, out[1].Priority)
	}
	if out[0].ID != "b" || out[1].ID != "d" {
		t.Errorf("tie should preserve original order: got %s, %s", out[0].ID, out[1].ID)
	}
	if out[2].Priority != 2 || out[3].Priority != 3 {
		t.Errorf("last two should be priority 2, 3: got %d, %d", out[2].Priority, out[3].Priority)
	}
}

// TestPool_AuthTrripsImmediately verifies that an auth error trips the
// breaker on the FIRST failure (not waiting for 2 consecutive failures).
func TestPool_AuthTripsImmediately(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k", Priority: 1},
	})
	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("401 unauthorized")
	})
	pool.mu.RLock()
	h := pool.health["a"]
	pool.mu.RUnlock()
	if !h.isTripped() {
		t.Error("auth error should trip immediately")
	}
}

// TestPool_NonAuthTripsAfterTwoFailures verifies that non-auth errors
// require 2 consecutive failures before tripping.
func TestPool_NonAuthTripsAfterTwoFailures(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k", Priority: 1},
	})

	// First failure: should not trip.
	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	pool.mu.RLock()
	h := pool.health["a"]
	pool.mu.RUnlock()
	if h.isTripped() {
		t.Error("single non-auth failure should not trip")
	}

	// Second consecutive failure: should trip.
	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	if !h.isTripped() {
		t.Error("two consecutive non-auth failures should trip")
	}
}

func TestPool_CallRespectsContextCancel(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k", Priority: 1},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := pool.Call(ctx, func(cfg *ProviderConfig) error {
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

// TestPool_MarkFailedDoesNotPanicOnUnknownProvider verifies MarkFailed
// gracefully handles unknown provider IDs (defensive).
func TestPool_MarkFailedDoesNotPanicOnUnknownProvider(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k", Priority: 1},
	})
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panicked: %v", r)
		}
	}()
	pool.MarkFailed("nonexistent", errors.New("test"))
	pool.MarkSuccess("nonexistent")
}

func TestPool_RetryConfigPropagation(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "a", ProviderType: "openai", APIKey: "k", Priority: 1},
	})
	pool.SetRetryConfig(7, 250*time.Millisecond)
	pool.mu.RLock()
	maxR := pool.maxRetries
	delay := pool.retryBaseDelay
	pool.mu.RUnlock()
	if maxR != 7 {
		t.Errorf("maxRetries = %d, want 7", maxR)
	}
	if delay != 250*time.Millisecond {
		t.Errorf("retryBaseDelay = %v, want 250ms", delay)
	}
}

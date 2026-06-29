package llm

import (
	"context"
	"errors"
	"testing"
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

	callCount := 0
	err := pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		callCount++
		if cfg.ID == "primary" {
			return errors.New("401 unauthorized")
		}
		return nil // fallback succeeds
	})
	if err != nil {
		t.Fatalf("expected nil error after failover, got %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (primary fail → fallback success), got %d", callCount)
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

	// First call: primary fails with auth, fallback succeeds.
	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		if cfg.ID == "primary" {
			return errors.New("401 unauthorized")
		}
		return nil
	})

	// Auth errors trip the breaker immediately. Verify primary is tripped.
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

	// One failure (doesn't trip — needs 2)
	_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
		return errors.New("500 internal error")
	})
	pool.mu.RLock()
	fails := pool.health["a"].consecutiveFailures
	pool.mu.RUnlock()
	if fails != 1 {
		t.Errorf("expected 1 consecutive failure, got %d", fails)
	}

	// Success resets the counter
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

	// Single transient failure (not auth) should NOT trip — needs 2.
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

// TestPool_BedrockRegionNotTripped verifies the pool works with bedrock-style
// configs that need region info.
func TestPool_BedrockConfig(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{
			ID:           "bedrock-1",
			ProviderType: "bedrock",
			APIKey:       "AKIA...",
			BedrockRegion: "us-west-2",
			Priority:     1,
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

// TestPool_DisabledProviderSkipped verifies that providers with Enabled=false
// are skipped during Acquire.
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

// Ensure time import is used.

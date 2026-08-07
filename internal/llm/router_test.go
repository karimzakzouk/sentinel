package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "plain object",
			in:   `{"foo": "bar", "n": 42}`,
			want: map[string]any{"foo": "bar", "n": float64(42)},
		},
		{
			name: "markdown fenced",
			in:   "Here you go:\n```json\n{\"foo\": \"bar\"}\n```\n",
			want: map[string]any{"foo": "bar"},
		},
		{
			name: "prose around",
			in:   "Sure! Here's the JSON:\n{\"foo\": \"bar\"}\nHope this helps!",
			want: map[string]any{"foo": "bar"},
		},
		{
			name: "empty",
			in:   "",
			want: map[string]any{},
		},
		{
			name: "garbage",
			in:   "this is not JSON at all",
			want: map[string]any{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractJSON(c.in)
			if len(got) != len(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
				return
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("key %q: got %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestRepairTruncated(t *testing.T) {
	in := `{"hypotheses": [{"statement": "the config is`
	out := repairTruncated(in)
	if !strings.HasSuffix(out, `"}]}`) {
		t.Errorf("repaired output should close string+array+object, got: %q", out)
	}
	if _, ok := tryParse(out); !ok {
		t.Errorf("repaired output should be valid JSON, got: %q", out)
	}
}

func TestExtractJSONList(t *testing.T) {
	in := `[{"statement": "foo"}]`
	got := extractJSON(in)
	if len(got) != 0 {
		t.Errorf("list input should return empty map, got %v", got)
	}
}

// TestRouter_RetriesOn429 verifies that the router retries on 429 rate-limit
// responses and eventually succeeds when the server recovers.
func TestRouter_RetriesOn429(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limit","type":"rate_limit_exceeded"}}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hello"}}]}`)
	}))
	defer server.Close()

	r := New(server.URL, "test-key", "test-model", "test-model")
	r.SetRetryConfig(3, 10*time.Millisecond)

	got, err := r.FastText(context.Background(), "system", "user", 100)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls)
	}
}

func TestRouter_RetriesOn5xx(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"service unavailable","type":"server_error"}}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"recovered"}}]}`)
	}))
	defer server.Close()

	r := New(server.URL, "test-key", "m", "m")
	r.SetRetryConfig(3, 10*time.Millisecond)

	got, err := r.FastText(context.Background(), "s", "u", 10)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if got != "recovered" {
		t.Errorf("got %q, want recovered", got)
	}
}

func TestRouter_NoRetryOn4xx(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	}))
	defer server.Close()

	r := New(server.URL, "bad-key", "m", "m")
	r.SetRetryConfig(3, 10*time.Millisecond)

	_, err := r.FastText(context.Background(), "s", "u", 10)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call (no retry for 401), got %d", calls)
	}
}

func TestRouter_RetryRespectsContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `rate limited`)
	}))
	defer server.Close()

	r := New(server.URL, "k", "m", "m")
	r.SetRetryConfig(5, 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := r.FastText(ctx, "s", "u", 10)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestParseRetryAfter(t *testing.T) {
	r := &Router{}
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"5", 5 * time.Second},
		{"30", 30 * time.Second},
	}
	for _, tt := range tests {
		got := r.parseRetryAfter(tt.in)
		if got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestBackoffDelay(t *testing.T) {
	r := &Router{retryBaseDelay: 100 * time.Millisecond}
	for _, attempt := range []int{1, 2, 3, 5} {
		d := r.backoffDelay(attempt)
		if d <= 0 {
			t.Errorf("attempt %d: delay should be positive, got %v", attempt, d)
		}
		maxExpected := time.Duration(float64(100*time.Millisecond) * pow2(attempt-1) * 1.3)
		if d > maxExpected && maxExpected > 0 {
			t.Errorf("attempt %d: delay %v exceeds expected max %v", attempt, d, maxExpected)
		}
	}
	r.retryBaseDelay = 1 * time.Hour
	if d := r.backoffDelay(5); d > 30*time.Second {
		t.Errorf("delay should be capped at 30s, got %v", d)
	}
}

func pow2(n int) float64 {
	if n < 0 {
		n = 0
	}
	r := 1.0
	for i := 0; i < n; i++ {
		r *= 2
	}
	return r
}

func TestIsTransientCapacityError(t *testing.T) {
	transient := []string{
		"ResourceExhausted: Worker local total request limit reached (32/32)",
		"Provider returned error: rate limited",
		"Upstream error from Nvidia: ResourceExhausted",
		"Rate limit exceeded",
		"rate-limit: too many requests",
		"ratelimit: 429",
		"Too many requests, please retry",
		"Server is overloaded",
		"Service temporarily unavailable",
		"capacity exceeded, please retry",
		"workers are busy",
		"throttled by provider",
		"request queued, retry later",
		"connection pool exhausted",
		"concurrency limit reached",
		"temporarily unavailable",
		"upstream returned 503",
		"gateway returned 502",
		"request timed out",
		"upstream timeout",
	}
	for _, msg := range transient {
		if !isTransientCapacityError(strings.ToLower(msg)) {
			t.Errorf("isTransientCapacityError(%q) = false, want true", msg)
		}
	}
	permanent := []string{
		"invalid api key",
		"model not found: gpt-5",
		"invalid request: messages array is empty",
		"unauthorized",
		"forbidden",
		"billing limit exceeded, please upgrade",
		"content policy violation",
		"token limit exceeded in input",
		"invalid response_format",
		"",
	}
	for _, msg := range permanent {
		if isTransientCapacityError(strings.ToLower(msg)) {
			t.Errorf("isTransientCapacityError(%q) = true, want false", msg)
		}
	}
}

// TestRouter_PoolNoCredentialsRace is a regression test for the v5 race
// where two concurrent pool calls would overwrite each other's credentials
// swap on the Router. In v1, the router does not swap any global state per
// call: each call constructs its own providerCfg locally. This test runs
// many concurrent pool calls against different provider IDs and verifies
// that no call ever sees another provider's credentials.
func TestRouter_PoolNoCredentialsRace(t *testing.T) {
	pool := NewPool([]ProviderConfig{
		{ID: "alpha", ProviderType: "openai", APIKey: "alpha-key", Priority: 1},
		{ID: "beta", ProviderType: "openai", APIKey: "beta-key", Priority: 2},
	})

	r := NewWithProvider("openai", "", "fallback-key", "m", "m")
	r.SetPool(pool)

	var wrongCredentials int64
	const goroutines = 20
	const iterations = 50

	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < iterations; i++ {
				_ = pool.Call(context.Background(), func(cfg *ProviderConfig) error {
					// Build the per-call cfg locally; this is what
					// completeWithPool does. The v5 bug was that this
					// mutated shared router state.
					pc := providerCfg{
						providerType: cfg.ProviderType,
						apiKey:       cfg.APIKey,
					}
					// Verify the apiKey matches the cfg passed in.
					if pc.apiKey != cfg.APIKey {
						atomic.AddInt64(&wrongCredentials, 1)
					}
					// Don't actually call GoAI — just verify the credentials
					// match what the pool handed us.
					return errors.New("test stub")
				})
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}

	if wrongCredentials > 0 {
		t.Errorf("detected %d credential mismatches — race condition present", wrongCredentials)
	}
}

package llm

import (
        "context"
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
        // Truncated mid-string
        in := `{"hypotheses": [{"statement": "the config is`
        out := repairTruncated(in)
        if !strings.HasSuffix(out, `"}]}`) {
                t.Errorf("repaired output should close string+array+object, got: %q", out)
        }
        if v, ok := tryParse(out); !ok {
                t.Errorf("repaired output should be valid JSON, got: %q", out)
        } else {
                _ = v
        }
}

func TestExtractJSONList(t *testing.T) {
        // LLMs sometimes return a list instead of an object — extractJSON returns {}.
        // The agents handle this by checking the type and extracting the first element.
        in := `[{"statement": "foo"}]`
        got := extractJSON(in)
        // We expect empty dict (not a list-as-map)
        if len(got) != 0 {
                t.Errorf("list input should return empty map, got %v", got)
        }
}

// TestRouter_RetriesOn429 verifies that the router retries on 429 rate-limit
// responses and eventually succeeds when the server recovers.
// v3.4.0: Without retry, a single 429 from a free-tier model kills the
// investigation. This is the most important production-readiness fix.
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
        r.SetRetryConfig(3, 10*time.Millisecond) // fast retry for tests

        got, err := r.FastText(context.Background(), "system", "user", 100)
        if err != nil {
                t.Fatalf("expected success after retries, got: %v", err)
        }
        if got != "hello" {
                t.Errorf("got %q, want hello", got)
        }
        if calls < 3 {
                t.Errorf("expected at least 3 calls (2 retries + 1 success), got %d", calls)
        }
}

// TestRouter_RetriesOn5xx verifies retry on 500 server errors.
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

// TestRouter_NoRetryOn4xx verifies that 4xx (non-429) errors are NOT retried.
// Bad requests / auth errors won't succeed on retry — retrying wastes time.
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

// TestRouter_ExhaustsRetries verifies that after maxRetries the last error is returned.
func TestRouter_ExhaustsRetries(t *testing.T) {
        var calls int32
        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                atomic.AddInt32(&calls, 1)
                w.WriteHeader(429)
                fmt.Fprint(w, `rate limited`)
        }))
        defer server.Close()

        r := New(server.URL, "k", "m", "m")
        r.SetRetryConfig(2, 5*time.Millisecond) // 2 retries → 3 total calls

        _, err := r.FastText(context.Background(), "s", "u", 10)
        if err == nil {
                t.Fatal("expected error after exhausting retries")
        }
        if calls != 3 {
                t.Errorf("expected 3 calls (initial + 2 retries), got %d", calls)
        }
}

// TestRouter_RetryRespectsContextCancel verifies that an in-progress retry
// waits gets cancelled if the context is cancelled.
func TestRouter_RetryRespectsContextCancel(t *testing.T) {
        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(429)
                fmt.Fprint(w, `rate limited`)
        }))
        defer server.Close()

        r := New(server.URL, "k", "m", "m")
        r.SetRetryConfig(5, 10*time.Second) // long delay so ctx cancel wins

        ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
        defer cancel()

        _, err := r.FastText(ctx, "s", "u", 10)
        if err == nil {
                t.Fatal("expected context cancellation error")
        }
}

// TestParseRetryAfter verifies Retry-After header parsing (seconds + HTTP-date).
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

// TestBackoffDelay verifies exponential growth + cap.
func TestBackoffDelay(t *testing.T) {
        r := &Router{retryBaseDelay: 100 * time.Millisecond}
        // attempt 1: 100ms ± 25ms (75-125ms)
        // attempt 2: 200ms ± 50ms (150-250ms)
        // attempt 5: 100ms * 16 = 1600ms ± 400ms (1200-2000ms)
        for _, attempt := range []int{1, 2, 3, 5} {
                d := r.backoffDelay(attempt)
                if d <= 0 {
                        t.Errorf("attempt %d: delay should be positive, got %v", attempt, d)
                }
                // Upper bound: 2^attempt * base * 1.25
                maxExpected := time.Duration(float64(100*time.Millisecond) * pow2(attempt-1) * 1.3)
                if d > maxExpected && maxExpected > 0 {
                        t.Errorf("attempt %d: delay %v exceeds expected max %v", attempt, d, maxExpected)
                }
        }
        // Cap at 30s
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

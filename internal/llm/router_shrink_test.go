package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goai "github.com/zendev-sh/goai"
)

// groqTPMError is the exact error class that permanently killed the
// shipping-api investigation in eval run #11 (chart 1.7.3): the request
// itself (13,271 tokens) exceeded the organization's entire per-minute
// budget (8,000). Retrying the same payload can never succeed; only
// shrinking helps.
const groqTPMError = `Request too large for model ` + "`openai/gpt-oss-120b`" + ` in organization ` +
	"`org_01k68jcv2te8mr0k1p18dgga2h`" + ` service tier ` + "`on_demand`" +
	` on tokens per minute (TPM): Limit 8000, Requested 13271, please reduce your message size and try again`

func TestIsRequestTooLargeMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"groq tpm (run #11)", groqTPMError, true},
		{"openai context length", "This model's maximum context length is 8192 tokens. However, you requested 13271 tokens", true},
		{"anthropic", "prompt is too long: 13271 tokens > 8192 maximum", true},
		{"generic context window", "input exceeds the context window of the model", true},
		{"goai groq pattern", "Please reduce the length of the messages", true},
		{"rate limit 429", "429 Too Many Requests: rate limit reached, try again in 34s", false},
		{"auth", "401 invalid api key", false},
		{"network", "connection refused", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestTooLargeMessage(tc.msg); got != tc.want {
				t.Errorf("isRequestTooLargeMessage(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestIsRequestTooLargeErrorTyped(t *testing.T) {
	if !isRequestTooLargeError(&goai.ContextOverflowError{Message: "exceeds the context window"}) {
		t.Error("typed ContextOverflowError should classify as too-large")
	}
	if isRequestTooLargeError(nil) {
		t.Error("nil should not classify as too-large")
	}
	if isRequestTooLargeError(errors.New(groqTPMError)) != true {
		t.Error("Groq TPM string should classify as too-large")
	}
}

func TestIsRateLimitWindowError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 typed", &goai.APIError{StatusCode: 429, Message: "rate limit reached"}, true},
		{"try again in", errors.New("Rate limit reached for tokens per minute. Please try again in 34s"), true},
		{"too-large is NOT window", errors.New(groqTPMError), false},
		{"auth", errors.New("401 unauthorized"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimitWindowError(tc.err); got != tc.want {
				t.Errorf("isRateLimitWindowError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRateLimitRetryDelay(t *testing.T) {
	// Explicit "try again in 34s" hint is honored (plus buffer).
	d := rateLimitRetryDelay(errors.New("Please try again in 34s"), 1)
	if d < 34*time.Second || d > 40*time.Second {
		t.Errorf("hint-based delay = %v, want ~36s", d)
	}
	// Fallback waits for TPM windows.
	if d := rateLimitRetryDelay(errors.New("429 too many requests"), 1); d != 25*time.Second {
		t.Errorf("attempt-1 fallback = %v, want 25s", d)
	}
	if d := rateLimitRetryDelay(errors.New("429 too many requests"), 2); d != 55*time.Second {
		t.Errorf("attempt-2 fallback = %v, want 55s", d)
	}
}

func TestShrinkUserPrompt(t *testing.T) {
	t.Run("preserves head and tail, drops middle", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("HEAD: service=shipping-api summary=NoSuchElementException\n")
		for i := 0; i < 500; i++ {
			fmt.Fprintf(&b, "middle context line %d with some padding text to make it long\n", i)
		}
		b.WriteString("TAIL: ## Your Task\nProduce hypotheses JSON\n")
		s := b.String()

		out := shrinkUserPrompt(s)
		if out == s {
			t.Fatal("expected shrink for a long prompt")
		}
		if !strings.HasPrefix(out, "HEAD: service=shipping-api") {
			t.Error("head (service identity) was not preserved")
		}
		if !strings.Contains(out, "TAIL: ## Your Task") {
			t.Error("tail (task instructions) was not preserved")
		}
		if !strings.Contains(out, "truncated to fit the model token limit") {
			t.Error("truncation marker missing")
		}
		if len(out) >= len(s) {
			t.Errorf("shrunk prompt is not smaller: %d -> %d", len(s), len(out))
		}
	})

	t.Run("roughly 60 percent per call converges under tpm budget", func(t *testing.T) {
		// 13,271 tokens ~= 53KB; three shrinks must bring it under the
		// 8,000-token ~= 32KB budget (60% compounding: 0.6^3 = 21.6%).
		s := strings.Repeat("x token token token\n", 2650) // ~53KB
		cur := s
		for range 3 {
			cur = shrinkUserPrompt(cur)
		}
		if len(cur) > 32*1024 {
			t.Errorf("after 3 shrinks still %d bytes, want <= 32KB", len(cur))
		}
	})

	t.Run("floor: short prompts returned unchanged", func(t *testing.T) {
		s := "short prompt"
		if out := shrinkUserPrompt(s); out != s {
			t.Errorf("short prompt should not shrink, got %q", out)
		}
	})

	t.Run("never cuts mid-line", func(t *testing.T) {
		s := strings.Repeat("0123456789\n", 1000)
		out := shrinkUserPrompt(s)
		for _, line := range strings.Split(out, "\n") {
			if line != "" && line != "0123456789" && !strings.HasPrefix(line, "[... Sentinel") {
				t.Errorf("mid-line cut detected: %q", line)
			}
		}
	})
}

// TestCompleteShrinksOnTooLarge exercises the full retry loop against a
// fake OpenAI-compatible server: first response reproduces the run-#11
// Groq TPM failure, second succeeds. The router must shrink and retry
// instead of failing the incident.
func TestCompleteShrinksOnTooLarge(t *testing.T) {
	var calls atomic.Int32
	var payloadSizes []int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := calls.Add(1)
		payloadSizes = append(payloadSizes, int32(len(body)))
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": groqTPMError, "type": "tokens_per_minute"},
			})
			return
		}
		// Shrunk request succeeds.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": `{"search_queries": ["carrierClient"]}`},
			}},
		})
	}))
	defer srv.Close()

	r := New(srv.URL, "test-key", "test-model", "test-model")
	r.SetRetryConfig(0, time.Millisecond) // disable lib-level retries; we test the router loop

	big := "Service: shipping-api\n" + strings.Repeat("pod log line with noise\n", 3000) + "\n## Your Task\nEmit queries."
	out, err := r.FrontierText(context.Background(), "system", big, 1000)
	if err != nil {
		t.Fatalf("expected shrink-retry to succeed, got error: %v", err)
	}
	if !strings.Contains(out, "carrierClient") {
		t.Errorf("unexpected response: %q", out)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected exactly 2 calls (fail + shrunk retry), got %d", got)
	}
	if len(payloadSizes) == 2 && payloadSizes[1] >= payloadSizes[0] {
		t.Errorf("retry payload (%d bytes) not smaller than original (%d bytes)", payloadSizes[1], payloadSizes[0])
	}
}

// TestCompleteGivesUpAfterShrinkBudget asserts the loop terminates when the
// provider keeps rejecting even shrunk payloads.
func TestCompleteGivesUpAfterShrinkBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": groqTPMError},
		})
	}))
	defer srv.Close()

	r := New(srv.URL, "test-key", "m", "m")
	r.SetRetryConfig(0, time.Millisecond)

	big := strings.Repeat("context line\n", 20000)
	_, err := r.FrontierText(context.Background(), "s", big, 100)
	if err == nil {
		t.Fatal("expected eventual error when provider always rejects")
	}
	if got := calls.Load(); got != int32(maxTooLargeShrinks+1) {
		t.Errorf("expected %d calls (1 + %d shrinks), got %d", maxTooLargeShrinks+1, maxTooLargeShrinks, got)
	}
	if !isRequestTooLargeError(err) {
		t.Errorf("final error should be the too-large error, got: %v", err)
	}
}

// TestCategorizeErrorTooLargeFirst guards the ordering: a Groq TPM error
// contains "try again" phrasing that could otherwise bucket it as a
// transient capacity error.
func TestCategorizeErrorTooLargeFirst(t *testing.T) {
	if got := categorizeError(errors.New(groqTPMError)); got != "too_large" {
		t.Errorf("categorizeError(groqTPM) = %q, want too_large", got)
	}
	if got := categorizeError(errors.New("429 rate limit exceeded")); got != "rate_limit" {
		t.Errorf("categorizeError(429) = %q, want rate_limit", got)
	}
}

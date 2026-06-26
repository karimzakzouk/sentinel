// Package llm provides the LLM router backed by GoAI.
// v3.12.0+: Supports 25+ native providers (OpenAI, Anthropic, Bedrock, Azure,
// Google, Vertex, OpenRouter, Groq, Mistral, Cohere, xAI, DeepSeek, Ollama,
// vLLM, etc.) via github.com/zendev-sh/goai. No proxy needed.
package llm

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "math"
        "regexp"
        "strings"
        "sync"
        "time"

        goai "github.com/zendev-sh/goai"
)

// Router picks fast vs frontier model and centralizes the API client.
//
// v3.12.0: Now backed by GoAI (github.com/zendev-sh/goai) for 25+ native
// LLM providers. The provider field determines which GoAI provider package
// is used (anthropic → native /v1/messages, bedrock → SigV4, azure →
// native Azure, etc.). For OpenAI-compatible providers (zai, custom,
// litellm, ollama, vllm), the openai provider package is used with a
// custom base URL.
type Router struct {
        providerType   string // v3.12.0: "openai", "anthropic", "bedrock", "azure", etc.
        baseURL        string
        apiKey         string
        fastModel      string
        frontierModel  string
        bedrockRegion  string // v3.12.0: AWS region for Bedrock
        // v3.4.0: retry config
        maxRetries     int
        retryBaseDelay time.Duration
        // v3.8.4: mu guards baseURL / apiKey / fastModel / frontierModel /
        // maxRetries / retryBaseDelay. These fields can be mutated at runtime
        // via Update (called from the runtimecfg apply callback when the user
        // changes LLM settings in the web UI). Without this lock, an in-flight
        // request could see a torn string pointer during an Update.
        mu sync.RWMutex
}

// New creates a Router with default retry settings (3 retries, 500ms base delay).
// v3.12.0: Defaults to provider="custom" (OpenAI-compatible with custom base URL).
// Use NewWithProvider() to specify a native provider like "anthropic" or "bedrock".
func New(baseURL, apiKey, fastModel, frontierModel string) *Router {
        return NewWithProvider("custom", baseURL, apiKey, fastModel, frontierModel)
}

// NewWithProvider creates a Router configured for a specific LLM provider.
// v3.12.0: The provider string maps to GoAI's provider packages:
//   "openai", "anthropic", "bedrock", "azure", "google", "vertex",
//   "zai", "openrouter", "groq", "mistral", "cohere", "xai",
//   "deepseek", "cerebras", "fireworks", "together", "perplexity",
//   "ollama", "vllm", "custom", "litellm"
//
// For native providers (anthropic, bedrock, azure, google, vertex), GoAI
// handles the provider-specific auth + request format. For OpenAI-compatible
// providers (zai, custom, litellm, ollama, vllm), the openai provider
// package is used with a custom base URL.
func NewWithProvider(providerType, baseURL, apiKey, fastModel, frontierModel string) *Router {
        return &Router{
                providerType:   providerType,
                baseURL:        strings.TrimRight(baseURL, "/"),
                apiKey:         apiKey,
                fastModel:      fastModel,
                frontierModel:  frontierModel,
                maxRetries:     3,
                retryBaseDelay: 500 * time.Millisecond,
        }
}

// SetBedrockRegion sets the AWS region for Bedrock provider calls. v3.12.0.
// Required when providerType="bedrock". Default: "us-east-1".
func (r *Router) SetBedrockRegion(region string) {
        r.mu.Lock()
        defer r.mu.Unlock()
        if region != "" {
                r.bedrockRegion = region
        }
}

// SetRetryConfig configures retry behavior.
// v3.4.0: maxRetries = 0 disables retry. baseDelay is the initial backoff
// (doubles each retry, with jitter).
func (r *Router) SetRetryConfig(maxRetries int, baseDelay time.Duration) {
        r.mu.Lock()
        defer r.mu.Unlock()
        r.maxRetries = maxRetries
        r.retryBaseDelay = baseDelay
}

// Update reconfigures the router (called when settings change at runtime).
// v3.8.4: Now thread-safe — takes the write lock so in-flight requests
// using the read methods below see a consistent snapshot.
// v3.12.0: Also updates the providerType (so runtime config can switch providers).
func (r *Router) Update(baseURL, apiKey, fastModel, frontierModel string) {
        r.mu.Lock()
        defer r.mu.Unlock()
        r.baseURL = strings.TrimRight(baseURL, "/")
        r.apiKey = apiKey
        r.fastModel = fastModel
        r.frontierModel = frontierModel
}

// UpdateProvider reconfigures the provider type. v3.12.0.
// Called when the user changes the LLM provider in the web UI.
func (r *Router) UpdateProvider(providerType string) {
        r.mu.Lock()
        defer r.mu.Unlock()
        r.providerType = providerType
}

// snapshot returns the current config under the read lock. Callers should
// use the returned values instead of touching the fields directly so that
// a concurrent Update can't tear a string mid-request.
func (r *Router) snapshot() (baseURL, apiKey, fastModel, frontierModel string, maxRetries int, retryBaseDelay time.Duration) {
        r.mu.RLock()
        defer r.mu.RUnlock()
        return r.baseURL, r.apiKey, r.fastModel, r.frontierModel, r.maxRetries, r.retryBaseDelay
}

// providerSnapshot returns the provider config under the read lock. v3.12.0.
// Used by buildGoAIModel to create a GoAI LanguageModel per-call.
func (r *Router) providerSnapshot() providerConfig {
        r.mu.RLock()
        defer r.mu.RUnlock()
        return providerConfig{
                providerType: r.providerType,
                baseURL:      r.baseURL,
                apiKey:       r.apiKey,
        }
}

// Snapshot is the exported version of snapshot, for use by main.go when
// applying runtime config changes (we need to read the current base URL
// to decide whether to override it). v3.8.4.
func (r *Router) Snapshot() (baseURL, apiKey, fastModel, frontierModel string) {
        b, k, f, fr, _, _ := r.snapshot()
        return b, k, f, fr
}

// FastJSON calls the fast model and parses JSON output.
func (r *Router) FastJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
        _, _, fastModel, _, _, _ := r.snapshot()
        return r.completeJSON(ctx, fastModel, system, user, maxTokens)
}

// FrontierJSON calls the frontier model and parses JSON output.
func (r *Router) FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
        _, _, _, frontierModel, _, _ := r.snapshot()
        return r.completeJSON(ctx, frontierModel, system, user, maxTokens)
}

// FastText calls the fast model and returns raw text.
func (r *Router) FastText(ctx context.Context, system, user string, maxTokens int) (string, error) {
        _, _, fastModel, _, _, _ := r.snapshot()
        return r.complete(ctx, fastModel, system, user, maxTokens)
}

// FrontierText calls the frontier model and returns raw text.
func (r *Router) FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error) {
        _, _, _, frontierModel, _, _ := r.snapshot()
        return r.complete(ctx, frontierModel, system, user, maxTokens)
}

// v3.12.0: The old chatRequest/chatMessage/responseFormat/chatResponse types
// and the do() function were removed — GoAI handles HTTP transport + request/
// response format internally. The extractJSON, backoffDelay, parseRetryAfter,
// and isTransientCapacityError helpers are kept (used by tests + completeJSON).

// complete calls the LLM model and returns the raw text response.
// v3.12.0: Now backed by GoAI (goai.GenerateText). GoAI handles:
//   - Provider-specific auth (Bearer, x-api-key, SigV4, Azure AD)
//   - Provider-specific request format (/chat/completions, /v1/messages, etc.)
//   - Retries with exponential backoff (via WithMaxRetries)
//   - Timeout (via WithTimeout)
func (r *Router) complete(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
        _, _, _, _, maxRetries, _ := r.snapshot()

        if recordLLMMetrics != nil {
                LLMCallStart()
                defer LLMCallEnd()
        }
        callStart := time.Now()

        goaiModel := r.buildGoAIModel(model)
        result, err := goai.GenerateText(ctx, goaiModel,
                goai.WithSystem(system),
                goai.WithPrompt(user),
                goai.WithMaxOutputTokens(maxTokens),
                goai.WithTemperature(0.2),
                goai.WithMaxRetries(maxRetries),
                goai.WithTimeout(300*time.Second),
                // v3.12.0: Force Chat Completions API (not Responses API). GoAI's openai
                // provider defaults to /responses which many OpenAI-compatible providers
                // (OpenRouter, z.ai, Ollama, vLLM) don't support. /chat/completions is
                // the universal standard. Native providers (Anthropic, Bedrock, etc.)
                // ignore this option — they have their own DoGenerate implementations.
                goai.WithProviderOptions(map[string]any{"useResponsesAPI": false}),
        )
        if err != nil {
                if recordLLMMetrics != nil {
                        recordLLMMetrics(model, "error", time.Since(callStart))
                }
                return "", fmt.Errorf("LLM call failed: %w", err)
        }
        if recordLLMMetrics != nil {
                recordLLMMetrics(model, "success", time.Since(callStart))
        }
        return result.Text, nil
}

// recordLLMMetrics is set by main.go via SetMetricsRecorder. This avoids a
// circular import (llm → metrics → ... → llm). If nil, metrics are skipped.
var recordLLMMetrics func(model, status string, duration time.Duration)

// SetMetricsRecorder wires in the metrics recorder. Called from main.go.
// v3.4.0: Allows the metrics package to observe LLM call latencies without
// creating a dependency cycle.
func SetMetricsRecorder(fn func(model, status string, duration time.Duration)) {
        recordLLMMetrics = fn
}

// LLMCallStart / LLMCallEnd are thin wrappers for the in-flight gauge.
// They call into the metrics recorder if set, avoiding a direct dependency.
var (
        llmCallStartHook func()
        llmCallEndHook   func()
)

// LLMCallStart increments the in-flight gauge (if wired).
func LLMCallStart() {
        if llmCallStartHook != nil {
                llmCallStartHook()
        }
}

// LLMCallEnd decrements the in-flight gauge (if wired).
func LLMCallEnd() {
        if llmCallEndHook != nil {
                llmCallEndHook()
        }
}

// SetInFlightHooks wires the in-flight gauge hooks. Called from main.go.
func SetInFlightHooks(start, end func()) {
        llmCallStartHook = start
        llmCallEndHook = end
}

// completeJSON calls the LLM model and parses the response as JSON.
// v3.12.0: Now backed by GoAI. The JSON instruction prompt is preserved
// (appends "Output ONLY a valid JSON object" to prevent reasoning prose).
// The extractJSON() function handles markdown fences + prose stripping.
func (r *Router) completeJSON(ctx context.Context, model, system, user string, maxTokens int) (map[string]any, error) {
        // v3.9.6: Stronger instruction to prevent the model from producing
        // extensive reasoning prose before the JSON. Some models (nemotron-3-super)
        // "think out loud" for 20K characters, use all max_tokens on reasoning,
        // and the actual JSON gets truncated.
        prompt := user + "\n\nIMPORTANT: Output ONLY a valid JSON object. Do NOT include reasoning, explanation, or prose. Start your response with { and end with }. No text before or after the JSON."

        text, err := r.complete(ctx, model, system, prompt, maxTokens)
        if err != nil {
                return nil, err
        }
        if text == "" {
                return map[string]any{}, nil
        }
        return extractJSON(text), nil
}

// backoffDelay computes the retry delay for the given attempt (1-indexed).
// v3.4.0: Exponential backoff: baseDelay * 2^(attempt-1), with ±25% jitter.
// Attempt 1: 500ms ± 125ms. Attempt 2: 1s ± 250ms. Attempt 3: 2s ± 500ms.
// Final result is capped at 30s (applied AFTER jitter to avoid overruns).
func (r *Router) backoffDelay(attempt int) time.Duration {
        if attempt < 1 {
                attempt = 1
        }
        if r.retryBaseDelay <= 0 {
                r.retryBaseDelay = 500 * time.Millisecond
        }
        // 2^(attempt-1) * baseDelay
        multiplier := math.Pow(2, float64(attempt-1))
        delay := time.Duration(float64(r.retryBaseDelay) * multiplier)
        // Jitter: ±25% — use attempt as a deterministic-ish seed variation.
        // (We avoid math/rand to keep this testable + dependency-free.)
        jitterRange := delay / 4
        if jitterRange > 0 {
                // Simple deterministic jitter based on time — good enough for retry spacing.
                jitter := time.Duration(time.Now().UnixNano() % int64(jitterRange*2))
                delay = delay - jitterRange + jitter
        }
        // Cap at 30s (applied AFTER jitter so the final value never exceeds 30s).
        if delay > 30*time.Second {
                delay = 30 * time.Second
        }
        return delay
}

// parseRetryAfter parses the HTTP Retry-After header (seconds or HTTP-date).
// Returns 0 if the header is absent or unparseable.
func (r *Router) parseRetryAfter(value string) time.Duration {
        if value == "" {
                return 0
        }
        // Try as seconds (most common)
        var secs int
        if _, err := fmt.Sscanf(value, "%d", &secs); err == nil && secs > 0 {
                return time.Duration(secs) * time.Second
        }
        // Try as HTTP-date (RFC1123)
        if t, err := time.Parse(time.RFC1123, value); err == nil {
                d := time.Until(t)
                if d > 0 {
                        return d
                }
        }
        return 0
}

// isTransientCapacityError returns true if the error message looks like a
// transient capacity/rate-limit issue that would benefit from a retry.
// v3.11.0: Used to detect 200-with-error-body responses where the provider
// returns HTTP 200 but puts a capacity error in the JSON body (e.g. OpenRouter
// routing to an upstream provider whose worker pool is full).
//
// This is provider-agnostic — no provider names are checked. Only generic
// transient-capacity keywords are matched. Anything else (auth errors, model
// not found, invalid request) returns false and is NOT retried.
func isTransientCapacityError(msg string) bool {
        if msg == "" {
                return false
        }
        patterns := []string{
                "rate limit", "rate-limit", "ratelimit",
                "too many requests",
                "resource exhausted", "exhausted",
                "capacity", "overload", "overloaded",
                "busy", "throttle", "throttled",
                "queue", "queued",
                "worker", // "worker limit reached"
                "temporarily", "temporary",
                "retry", "try again",
                "service unavailable", "unavailable",
                "timeout", "timed out",
                "503", "502", "504",
                "pool", // "connection pool exhausted"
                "concurrent", "concurrency",
        }
        for _, p := range patterns {
                if strings.Contains(msg, p) {
                        return true
                }
        }
        return false
}

// extractJSON defensively parses JSON from LLM output.
// Handles markdown fences, bare JSON, prose around JSON, and truncated JSON.
//
// v3.9.6: Handles the "reasoning prose before JSON" case. Some models
// (nemotron-3-super) produce 20K chars of reasoning, then the JSON at the
// end. If the JSON is truncated (max_tokens cutoff), we:
//  1. Find all candidate JSON start positions (lines starting with `{`)
//  2. Try to parse from each, starting from the LAST one (most likely to be
//     the actual JSON, not a `{` mentioned in prose)
//  3. If none parse, try repairTruncated on each candidate
func extractJSON(text string) map[string]any {
        // Try 1: markdown-fenced JSON block
        if m := jsonBlockRe.FindStringSubmatch(text); len(m) >= 2 {
                if v, ok := tryParse(m[1]); ok {
                        return v
                }
        }
        // Try 2: bare JSON object (greedy — matches first { to last })
        if m := bareJSONRe.FindStringSubmatch(text); len(m) >= 1 {
                if v, ok := tryParse(m[0]); ok {
                        return v
                }
        }
        // Try 3: the whole text
        if v, ok := tryParse(text); ok {
                return v
        }

        // v3.9.6: Try 4 — find all lines that start with { and try parsing from
        // each. This handles the case where the model produces prose like:
        //   "We need to add a check...\n\n{ \"fix_type\": \"code_fix\", ..."
        // The bareJSONRe above would match from the first { in the prose (if any),
        // not the actual JSON. By trying each { on its own line, we find the real
        // JSON start. Try in REVERSE order (last { first) because the JSON is
        // usually at the end of the response.
        lines := strings.Split(text, "\n")
        jsonStarts := []int{}
        for i, line := range lines {
                trimmed := strings.TrimSpace(line)
                if strings.HasPrefix(trimmed, "{") {
                        jsonStarts = append(jsonStarts, i)
                }
        }
        // Try from the last { to the end, then repair
        for i := len(jsonStarts) - 1; i >= 0; i-- {
                startLine := jsonStarts[i]
                candidate := strings.Join(lines[startLine:], "\n")
                // Try direct parse
                if v, ok := tryParse(candidate); ok {
                        return v
                }
                // Try repair truncated
                if repaired := repairTruncated(candidate); repaired != "" {
                        if v, ok := tryParse(repaired); ok {
                                log.Printf("[llm] extractJSON recovered via prose-strip + repair (line %d)", startLine)
                                return v
                        }
                }
        }

        // Try 5: original repairTruncated on the full text
        if repaired := repairTruncated(text); repaired != "" {
                if v, ok := tryParse(repaired); ok {
                        return v
                }
        }

        // Log the raw text when ALL parsing attempts fail.
        preview := text
        if len(preview) > 500 {
                preview = preview[:500] + "...(truncated)"
        }
        log.Printf("[llm] extractJSON FAILED — raw response (len=%d): %s", len(text), preview)
        return map[string]any{}
}

var (
        // v3.8.8: Broader fence regex. The old one only matched ```json or bare ```.
        // Some models (gpt-oss-20b:free) return ```jsoncjson or ```jsonc or other
        // language tags. Match any word characters after the opening fence.
        jsonBlockRe = regexp.MustCompile("(?s)" + "```(?:[a-zA-Z]+)?\\s*(\\{.*?\\}|\\[.*?\\])\\s*```")
        bareJSONRe  = regexp.MustCompile(`(?s)(\{.*\}|\[.*\])`)
)

func tryParse(s string) (map[string]any, bool) {
        var v map[string]any
        if err := json.Unmarshal([]byte(s), &v); err == nil {
                return v, true
        }
        return nil, false
}

// repairTruncated tries to close unclosed strings/braces/brackets in JSON
// that was cut off by max_tokens. Tracks opens as a stack and closes in
// reverse order.
func repairTruncated(text string) string {
        start := strings.Index(text, "{")
        if start == -1 {
                return ""
        }
        s := text[start:]

        inString := false
        escapeNext := false
        var stack []rune // tracks open braces/brackets in order

        for _, c := range s {
                if escapeNext {
                        escapeNext = false
                        continue
                }
                if c == '\\' {
                        escapeNext = true
                        continue
                }
                if c == '"' {
                        inString = !inString
                        continue
                }
                if inString {
                        continue
                }
                switch c {
                case '{', '[':
                        stack = append(stack, c)
                case '}':
                        if len(stack) > 0 && stack[len(stack)-1] == '{' {
                                stack = stack[:len(stack)-1]
                        }
                case ']':
                        if len(stack) > 0 && stack[len(stack)-1] == '[' {
                                stack = stack[:len(stack)-1]
                        }
                }
        }

        if inString {
                s += `"`
        }
        // Trim trailing comma
        s = strings.TrimRight(strings.TrimSpace(s), ",")
        // Close in reverse order: stack is [{, [, {] → close }, ], }
        for i := len(stack) - 1; i >= 0; i-- {
                if stack[i] == '{' {
                        s += "}"
                } else if stack[i] == '[' {
                        s += "]"
                }
        }
        return s
}


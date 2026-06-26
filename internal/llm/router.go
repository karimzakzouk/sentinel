// Package llm provides a minimal OpenAI-compatible LLM client.
// No SDK — just net/http. Works with OpenAI, z.ai, OpenRouter, Gemini-via-OpenAI,
// Ollama, vLLM, LiteLLM, any OpenAI-compatible endpoint.
//
// v3.4.0: Added retry with exponential backoff for 429 (rate limit) and 5xx
// (transient server errors). Free-tier models (gpt-oss-120b:free, glm-4-flash)
// rate-limit aggressively — without retry, a single 429 kills the investigation.
//
// v3.11.0: Known limitation (audit #16) — this router ONLY speaks the
// OpenAI-compatible API surface (POST /chat/completions, Authorization: Bearer,
// response_format: json_object). It does NOT support:
//   - Native Anthropic Claude API (uses /v1/messages with x-api-key auth)
//   - Native Google Gemini API (uses :generateContent with API-key-in-query)
//   - AWS Bedrock (per-model URL paths with SigV4 auth)
//   - Azure OpenAI (per-deployment URL paths with api-version query param)
//
// To use these providers, stand up a LiteLLM (https://github.com/BerriAI/litellm)
// or OneAPI proxy in front — they expose an OpenAI-compatible endpoint that
// this router can talk to. Configure SENTINEL_LLM_PROVIDER=litellm (or "custom")
// and SENTINEL_LLM_BASE_URL=http://<proxy>:4000/v1.
//
// When the configured provider is one of the unsupported native providers
// (anthropic, bedrock, azure), the router logs a startup warning directing
// the operator to the proxy workaround. This is a deliberate design choice
// (keeping the router minimal — no per-provider SDK deps) rather than a bug.
package llm

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "math"
        "net/http"
        "regexp"
        "strings"
        "sync"
        "time"
)

// Router picks fast vs frontier model and centralizes the API client.
type Router struct {
        baseURL      string
        apiKey       string
        fastModel    string
        frontierModel string
        http         *http.Client
        // v3.4.0: retry config
        maxRetries    int
        retryBaseDelay time.Duration
        // v3.8.4: mu guards baseURL / apiKey / fastModel / frontierModel /
        // maxRetries / retryBaseDelay. These fields can be mutated at runtime
        // via Update (called from the runtimecfg apply callback when the user
        // changes LLM settings in the web UI). Without this lock, an in-flight
        // request could see a torn string pointer during an Update.
        mu sync.RWMutex
}

// New creates a Router with default retry settings (3 retries, 500ms base delay).
// v3.8.8: HTTP timeout increased from 120s to 300s. Free-tier LLM models
// (gpt-oss-120b:free, etc.) can take 2-3 minutes to generate a full file's
// worth of content. 120s was too short — the fix proposer LLM call was
// timing out before the model finished responding, causing every
// investigation to fail with "context deadline exceeded".
func New(baseURL, apiKey, fastModel, frontierModel string) *Router {
        return &Router{
                baseURL:        strings.TrimRight(baseURL, "/"),
                apiKey:         apiKey,
                fastModel:      fastModel,
                frontierModel:  frontierModel,
                http: &http.Client{
                        Timeout: 300 * time.Second,
                },
                maxRetries:     3,
                retryBaseDelay: 500 * time.Millisecond,
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
func (r *Router) Update(baseURL, apiKey, fastModel, frontierModel string) {
        r.mu.Lock()
        defer r.mu.Unlock()
        r.baseURL = strings.TrimRight(baseURL, "/")
        r.apiKey = apiKey
        r.fastModel = fastModel
        r.frontierModel = frontierModel
}

// snapshot returns the current config under the read lock. Callers should
// use the returned values instead of touching the fields directly so that
// a concurrent Update can't tear a string mid-request.
func (r *Router) snapshot() (baseURL, apiKey, fastModel, frontierModel string, maxRetries int, retryBaseDelay time.Duration) {
        r.mu.RLock()
        defer r.mu.RUnlock()
        return r.baseURL, r.apiKey, r.fastModel, r.frontierModel, r.maxRetries, r.retryBaseDelay
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

type chatRequest struct {
        Model          string         `json:"model"`
        Messages       []chatMessage  `json:"messages"`
        Temperature    float64        `json:"temperature"`
        MaxTokens      int            `json:"max_tokens,omitempty"`
        ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
        Role    string `json:"role"`
        Content string `json:"content"`
}

type responseFormat struct {
        Type string `json:"type"`
}

type chatResponse struct {
        Choices []struct {
                Message chatMessage `json:"message"`
        } `json:"choices"`
        Error *struct {
                Message string `json:"message"`
                Type    string `json:"type"`
        } `json:"error,omitempty"`
}

func (r *Router) complete(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
        req := chatRequest{
                Model: model,
                Messages: []chatMessage{
                        {Role: "system", Content: system},
                        {Role: "user", Content: user},
                },
                Temperature: 0.2,
                MaxTokens:   maxTokens,
        }
        body, err := r.do(ctx, req)
        if err != nil {
                return "", err
        }
        if len(body.Choices) == 0 {
                return "", fmt.Errorf("LLM returned no choices")
        }
        return body.Choices[0].Message.Content, nil
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

func (r *Router) completeJSON(ctx context.Context, model, system, user string, maxTokens int) (map[string]any, error) {
        // v3.9.6: Stronger instruction to prevent the model from producing
        // extensive reasoning prose before the JSON. Some models (nemotron-3-super)
        // "think out loud" for 20K characters, use all max_tokens on reasoning,
        // and the actual JSON gets truncated.
        prompt := user + "\n\nIMPORTANT: Output ONLY a valid JSON object. Do NOT include reasoning, explanation, or prose. Start your response with { and end with }. No text before or after the JSON."
        req := chatRequest{
                Model: model,
                Messages: []chatMessage{
                        {Role: "system", Content: system},
                        {Role: "user", Content: prompt},
                },
                Temperature: 0.1,
                MaxTokens:   maxTokens,
                // Try JSON mode first; if the provider rejects it we retry without.
                ResponseFormat: &responseFormat{Type: "json_object"},
        }
        body, err := r.do(ctx, req)
        if err != nil {
                // Retry without response_format (some providers don't support it)
                req.ResponseFormat = nil
                body, err = r.do(ctx, req)
                if err != nil {
                        return nil, err
                }
        }
        if len(body.Choices) == 0 {
                return map[string]any{}, nil
        }
        return extractJSON(body.Choices[0].Message.Content), nil
}

// do executes the HTTP call with retry on 429/5xx.
// v3.4.0: Retries up to r.maxRetries times with exponential backoff + jitter.
// 429 (rate limit) and 5xx (server error) are retried. 4xx (bad request,
// auth error) are NOT retried — they won't succeed on retry.
//
// Retry delays: 500ms, 1s, 2s, 4s ... (doubled each time, ±25% jitter).
// Respects ctx cancellation between retries.
//
// v3.4.0: Records metrics (call count + duration) via the recorder set by
// SetMetricsRecorder. The status is "success", "rate_limited", "error", or
// "client_error" (4xx non-429).
func (r *Router) do(ctx context.Context, req chatRequest) (*chatResponse, error) {
        body, err := json.Marshal(req)
        if err != nil {
                return nil, fmt.Errorf("marshal request: %w", err)
        }

        // v3.8.4: Snapshot the string fields under the read lock. Strings in Go
        // are a (ptr, len) pair and concurrent read+write can tear — a concurrent
        // Update() changing the API key mid-request could cause the Authorization
        // header to contain a torn pointer (garbage bytes). The integer fields
        // (maxRetries, retryBaseDelay) are word-sized and atomically read on
        // 64-bit platforms, so we don't bother locking them.
        baseURL, apiKey, _, _, _, _ := r.snapshot()

        // v3.4.0: Track total duration across all retries for metrics.
        callStart := time.Now()
        if recordLLMMetrics != nil {
                LLMCallStart()
                defer LLMCallEnd()
        }

        var lastErr error
        for attempt := 0; attempt <= r.maxRetries; attempt++ {
                if attempt > 0 {
                        delay := r.backoffDelay(attempt)
                        select {
                        case <-ctx.Done():
                                return nil, ctx.Err()
                        case <-time.After(delay):
                        }
                }

                // Re-create the request body reader each attempt (body is consumed by Do).
                httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
                if err != nil {
                        return nil, fmt.Errorf("build request: %w", err)
                }
                httpReq.Header.Set("Content-Type", "application/json")
                httpReq.Header.Set("Authorization", "Bearer "+apiKey)

                resp, err := r.http.Do(httpReq)
                if err != nil {
                        lastErr = fmt.Errorf("LLM call failed: %w", err)
                        // Network errors are retried (could be transient DNS/TLS/connection)
                        if attempt < r.maxRetries {
                                log.Printf("[llm] attempt %d/%d failed (network): %v — retrying", attempt+1, r.maxRetries+1, err)
                                continue
                        }
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, "error", time.Since(callStart))
                        }
                        return nil, lastErr
                }

                respBody, readErr := io.ReadAll(resp.Body)
                resp.Body.Close()
                if readErr != nil {
                        lastErr = fmt.Errorf("read response: %w", readErr)
                        if attempt < r.maxRetries {
                                log.Printf("[llm] attempt %d/%d failed (read): %v — retrying", attempt+1, r.maxRetries+1, readErr)
                                continue
                        }
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, "error", time.Since(callStart))
                        }
                        return nil, lastErr
                }

                // v3.4.0: Retry on 429 (rate limit) and 5xx (server error).
                // Do NOT retry on 4xx (bad request, auth) — won't succeed.
                if resp.StatusCode == 429 || resp.StatusCode >= 500 {
                        lastErr = fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
                        if attempt < r.maxRetries {
                                // Honor Retry-After header if present (common for 429s).
                                retryAfter := r.parseRetryAfter(resp.Header.Get("Retry-After"))
                                if retryAfter > 0 {
                                        delay := retryAfter
                                        if delay > 30*time.Second {
                                                delay = 30 * time.Second // cap at 30s
                                        }
                                        log.Printf("[llm] attempt %d/%d got %d (Retry-After: %v) — retrying", attempt+1, r.maxRetries+1, resp.StatusCode, delay)
                                        select {
                                        case <-ctx.Done():
                                                return nil, ctx.Err()
                                        case <-time.After(delay):
                                        }
                                } else {
                                        log.Printf("[llm] attempt %d/%d got %d — retrying", attempt+1, r.maxRetries+1, resp.StatusCode)
                                }
                                continue
                        }
                        // Exhausted retries on 429/5xx
                        status := "error"
                        if resp.StatusCode == 429 {
                                status = "rate_limited"
                        }
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, status, time.Since(callStart))
                        }
                        return nil, lastErr
                }

                if resp.StatusCode >= 400 {
                        // 4xx (non-429) — don't retry, return immediately.
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, "client_error", time.Since(callStart))
                        }
                        return nil, fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
                }

                var chatResp chatResponse
                if err := json.Unmarshal(respBody, &chatResp); err != nil {
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, "error", time.Since(callStart))
                        }
                        return nil, fmt.Errorf("unmarshal response: %w", err)
                }
                if chatResp.Error != nil {
                        // v3.11.0: Some providers return HTTP 200 but put an error in the
                        // response body (e.g. OpenRouter/Nvidia returns 200 with
                        // {"error":{"message":"ResourceExhausted: Worker local total
                        // request limit reached (32/32)"}} when the upstream provider's
                        // worker pool is full). This is a transient capacity issue, not a
                        // client error — retry it.
                        //
                        // This is provider-agnostic: we check the error message for
                        // transient-capacity patterns (rate, limit, exhaust, capacity,
                        // overload, busy, throttle, queue, worker). No provider names
                        // are hardcoded — any provider that returns a 200-with-error
                        // matching these patterns gets retried.
                        errMsg := strings.ToLower(chatResp.Error.Message)
                        if isTransientCapacityError(errMsg) {
                                lastErr = fmt.Errorf("LLM capacity error (200 body): %s", chatResp.Error.Message)
                                if attempt < r.maxRetries {
                                        log.Printf("[llm] attempt %d/%d got 200 with transient error: %s — retrying",
                                                attempt+1, r.maxRetries+1, chatResp.Error.Message[:min(len(chatResp.Error.Message), 100)])
                                        select {
                                        case <-ctx.Done():
                                                return nil, ctx.Err()
                                        case <-time.After(r.backoffDelay(attempt + 1)):
                                        }
                                        continue
                                }
                        }
                        if recordLLMMetrics != nil {
                                recordLLMMetrics(req.Model, "error", time.Since(callStart))
                        }
                        return nil, fmt.Errorf("LLM error (%s): %s", chatResp.Error.Type, chatResp.Error.Message)
                }
                if recordLLMMetrics != nil {
                        recordLLMMetrics(req.Model, "success", time.Since(callStart))
                }
                return &chatResp, nil
        }

        if recordLLMMetrics != nil {
                recordLLMMetrics(req.Model, "error", time.Since(callStart))
        }
        return nil, lastErr
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

func min(a, b int) int {
        if a < b {
                return a
        }
        return b
}

package llm

// router.go: the LLM router. Picks fast vs frontier model, centralizes the
// API client, wraps each call in the pool's failover, and (optionally)
// serves from the semantic cache.
//
// Race note (v5 bug fixed here): the v5 router swapped r.providerType /
// r.baseURL / r.apiKey under the lock for the duration of each pool call so
// buildGoAIModel() would pick up the right credentials. Two concurrent
// pool calls could see each other's swap. The fix in v1: buildGoAIModel
// takes a providerCfg value (not a method on Router), and each call site
// constructs the cfg locally. No global state is mutated per-call.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
	goai "github.com/zendev-sh/goai"
)

// Router picks fast vs frontier model and centralizes the API client.
// Single-provider fields are the fallback when no Pool is wired. The Pool
// (SetPool) provides multi-provider failover with circuit breakers.
type Router struct {
	mu             sync.RWMutex
	providerType   string
	baseURL        string
	apiKey         string
	fastModel      string
	frontierModel  string
	bedrockRegion  string
	maxRetries     int
	retryBaseDelay time.Duration

	pool  *Pool
	cache *SemanticCache

	// noop, when true, makes every call return a stub response and logs the
	// prompt instead of making an API call. Used when SENTINEL_LLM_API_KEY is
	// empty (auto-degrade) or SENTINEL_OBSERVE_ONLY=true.
	noop bool
}

// New returns a Router with providerType "custom". Use NewWithProvider for
// a native provider like "anthropic" or "bedrock".
func New(baseURL, apiKey, fastModel, frontierModel string) *Router {
	return NewWithProvider("custom", baseURL, apiKey, fastModel, frontierModel)
}

// NewWithProvider returns a Router configured for a specific LLM provider.
// See goai_provider.go for the full list of supported providerType values.
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

// NewNoop returns a Router that never calls an LLM. Every call logs the
// prompt and returns a stub response. Used when LLM credentials are not
// configured (auto-degrade) or in observe-only mode.
func NewNoop() *Router {
	return &Router{
		noop:           true,
		providerType:   "noop",
		fastModel:      "noop",
		frontierModel:  "noop",
		maxRetries:     0,
		retryBaseDelay: 0,
	}
}

// IsNoop reports whether the router is in noop mode (no real LLM calls).
func (r *Router) IsNoop() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.noop
}

// SetBedrockRegion sets the AWS region for Bedrock calls.
func (r *Router) SetBedrockRegion(region string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if region != "" {
		r.bedrockRegion = region
	}
}

// SetPool wires a multi-provider failover pool. When set, every LLM call
// goes through pool.Call(); the Router's single-provider fields become
// the fallback.
func (r *Router) SetPool(pool *Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pool = pool
	if pool != nil {
		pool.SetRetryConfig(r.maxRetries, r.retryBaseDelay)
	}
}

// SetCache wires a SemanticCache. When set and Enabled(), LLM responses
// are cached by prompt similarity.
func (r *Router) SetCache(cache *SemanticCache) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = cache
}

// Pool returns the router's failover pool (nil if not configured).
func (r *Router) Pool() *Pool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pool
}

// SetRetryConfig configures retry behavior for LLM calls.
func (r *Router) SetRetryConfig(maxRetries int, baseDelay time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxRetries = maxRetries
	r.retryBaseDelay = baseDelay
	if r.pool != nil {
		r.pool.SetRetryConfig(maxRetries, baseDelay)
	}
}

// Update reconfigures the single-provider fields (called when runtime
// settings change).
func (r *Router) Update(baseURL, apiKey, fastModel, frontierModel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.baseURL = strings.TrimRight(baseURL, "/")
	r.apiKey = apiKey
	r.fastModel = fastModel
	r.frontierModel = frontierModel
}

// UpdateProvider reconfigures the provider type.
func (r *Router) UpdateProvider(providerType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerType = providerType
}

// Snapshot returns the current single-provider config. Exported for
// runtime config application in main.go.
func (r *Router) Snapshot() (baseURL, apiKey, fastModel, frontierModel string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.baseURL, r.apiKey, r.fastModel, r.frontierModel
}

// snapshot returns the full single-provider config under the read lock.
func (r *Router) snapshot() (providerType, baseURL, apiKey, fastModel, frontierModel string,
	maxRetries int, retryBaseDelay time.Duration, bedrockRegion string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerType, r.baseURL, r.apiKey, r.fastModel, r.frontierModel,
		r.maxRetries, r.retryBaseDelay, r.bedrockRegion
}

// FastJSON calls the fast model and parses the response as JSON.
func (r *Router) FastJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
	if r.IsNoop() {
		return r.noopCall(ctx, "fast_json", system, user)
	}
	_, _, _, fastModel, _, _, _, _ := r.snapshot()
	return r.completeJSON(ctx, fastModel, system, user, maxTokens)
}

// FrontierJSON calls the frontier model and parses the response as JSON.
func (r *Router) FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
	if r.IsNoop() {
		return r.noopCall(ctx, "frontier_json", system, user)
	}
	_, _, _, _, frontierModel, _, _, _ := r.snapshot()
	return r.completeJSON(ctx, frontierModel, system, user, maxTokens)
}

// FastText calls the fast model and returns the raw text.
func (r *Router) FastText(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if r.IsNoop() {
		return r.noopText(ctx, "fast_text", system, user)
	}
	_, _, _, fastModel, _, _, _, _ := r.snapshot()
	return r.complete(ctx, fastModel, system, user, maxTokens)
}

// FrontierText calls the frontier model and returns the raw text.
func (r *Router) FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if r.IsNoop() {
		return r.noopText(ctx, "frontier_text", system, user)
	}
	_, _, _, _, frontierModel, _, _, _ := r.snapshot()
	return r.complete(ctx, frontierModel, system, user, maxTokens)
}

// complete is the core LLM call. Checks the cache, then routes through
// the pool (if set) or the single-provider path. Wraps the call with two
// resilience loops (v1.7.4):
//
//   - too-large: the request itself exceeds the model/provider token
//     budget (e.g. Groq "Request too large ... (TPM): Limit 8000,
//     Requested 13271"). Retrying the same payload can NEVER succeed,
//     so the user prompt is progressively shrunk (middle-dropped) and
//     retried immediately, up to maxTooLargeShrinks times.
//   - rate-limit: the request fits but the provider window is exhausted
//     (429 / TPM per-minute). Waited retry with the same payload, up to
//     maxRateLimitRetries times, with window-aware delays.
//
// Everything else propagates immediately (auth errors, context
// cancellation, non-transient failures).
const (
	maxTooLargeShrinks  = 3
	maxRateLimitRetries = 2
)

func (r *Router) complete(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
	_, _, _, _, _, maxRetries, _, _ := r.snapshot()

	r.mu.RLock()
	cache := r.cache
	pool := r.pool
	r.mu.RUnlock()

	promptKey := system + "\n" + user + "\n" + model
	if cache != nil && cache.Enabled() {
		if resp, ok := cache.Get(ctx, promptKey); ok {
			return resp, nil
		}
	}

	if recordLLMMetrics != nil {
		LLMCallStart()
		defer LLMCallEnd()
	}
	callStart := time.Now()

	current := user
	tooLargeShrinks := 0
	rateLimitRetries := 0
	for {
		var result string
		var err error
		if pool != nil && pool.HasProviders() {
			result, err = r.completeWithPool(ctx, pool, model, system, current, maxTokens, maxRetries, callStart)
		} else {
			result, err = r.completeSingle(ctx, model, system, current, maxTokens, maxRetries, callStart, "")
		}
		if err == nil {
			if cache != nil && cache.Enabled() {
				cache.Put(ctx, promptKey, result)
			}
			return result, nil
		}

		// Too-large: shrink and retry immediately (waiting cannot help —
		// the payload exceeds the whole budget).
		if isRequestTooLargeError(err) {
			if tooLargeShrinks >= maxTooLargeShrinks {
				logging.Error("llm: request too large — shrink budget exhausted",
					"model", model, "prompt_bytes", len(current), "shrinks", tooLargeShrinks)
				return "", err
			}
			shrunk := shrinkUserPrompt(current)
			if shrunk == current {
				// Already at the shrink floor; nothing more to shed.
				return "", err
			}
			tooLargeShrinks++
			logging.Warn("llm: request too large — shrinking context and retrying",
				"model", model, "shrink", tooLargeShrinks,
				"was_bytes", len(current), "now_bytes", len(shrunk))
			current = shrunk
			continue
		}

		// Rate-limit: the request fits but the window is exhausted. Wait
		// for the window to reset and retry the SAME payload.
		if isRateLimitWindowError(err) && rateLimitRetries < maxRateLimitRetries {
			if ctx.Err() != nil {
				return "", err
			}
			rateLimitRetries++
			delay := rateLimitRetryDelay(err, rateLimitRetries)
			logging.Warn("llm: rate limit hit — waiting for window and retrying",
				"model", model, "retry", rateLimitRetries, "wait", delay)
			select {
			case <-ctx.Done():
				return "", err
			case <-time.After(delay):
			}
			continue
		}

		return "", err
	}
}

// poolModelForRouterModel maps the router-level model name (the primary
// provider's name for the fast/frontier tier) to cfg's own model name for
// the same tier. The tier methods (FastJSON/FrontierText/...) pass the
// ROUTER's model into the pool, but every other pool entry must serve the
// call with ITS OWN name — e.g. groq's frontier "openai/gpt-oss-120b" has
// to become "gemini-2.5-flash" on a gemini overflow entry, or Google
// rejects the request ("GenerateContentRequest.model: unexpected model
// name format"). Run #19: every failover died this way — the pool was
// "active" but the overflow was never callable; all 6 incidents failed,
// 0/9 fixes.
func poolModelForRouterModel(cfg *ProviderConfig, routerFast, routerFrontier, model string) string {
	// Explicit (non-tier) model names from direct callers: verbatim.
	if model != routerFast && model != routerFrontier {
		return model
	}
	// Entry configured without per-tier models: keep the router-level name.
	if cfg.FastModel == "" && cfg.FrontierModel == "" {
		return model
	}
	// Unambiguous tier: this entry's model for that tier when set.
	if routerFast != routerFrontier {
		if model == routerFast && cfg.FastModel != "" {
			return cfg.FastModel
		}
		if model == routerFrontier && cfg.FrontierModel != "" {
			return cfg.FrontierModel
		}
	}
	// Ambiguous tier, or the entry lacks this tier's model: prefer the
	// entry's fast model, then its frontier model.
	if cfg.FastModel != "" {
		return cfg.FastModel
	}
	return cfg.FrontierModel
}

// completeWithPool tries each provider in the pool in priority order.
//
// Race fix: each call constructs its own providerCfg from the acquired
// ProviderConfig and passes it directly to buildGoAIModel. The Router's
// single-provider fields (providerType/baseURL/apiKey) are NOT mutated.
// Two concurrent pool calls therefore cannot see each other's credentials.
func (r *Router) completeWithPool(ctx context.Context, pool *Pool, model, system, user string,
	maxTokens, maxRetries int, callStart time.Time) (string, error) {

	_, _, _, routerFast, routerFrontier, _, _, _ := r.snapshot()
	var resultText string
	var lastErr error
	err := pool.Call(ctx, func(cfg *ProviderConfig) error {
		pc := providerCfg{
			providerType:  cfg.ProviderType,
			baseURL:       cfg.BaseURL,
			apiKey:        cfg.APIKey,
			bedrockRegion: cfg.BedrockRegion,
		}
		callModel := poolModelForRouterModel(cfg, routerFast, routerFrontier, model)
		text, err := r.completeSingleWithCfg(ctx, pc, callModel, system, user, maxTokens, maxRetries, callStart, cfg.ID)
		if err != nil {
			lastErr = err
			return err
		}
		resultText = text
		return nil
	})
	if err != nil {
		if recordLLMMetrics != nil {
			recordLLMMetrics(model, "error", time.Since(callStart), 0, 0)
		}
		if lastErr != nil {
			return "", fmt.Errorf("LLM call failed (all providers exhausted): %w", lastErr)
		}
		return "", err
	}
	return resultText, nil
}

// completeSingle executes one LLM call against the Router's single-provider
// config. providerID is used for metrics labeling (empty = single-provider
// mode, no pool).
func (r *Router) completeSingle(ctx context.Context, model, system, user string,
	maxTokens, maxRetries int, callStart time.Time, providerID string) (string, error) {

	pt, baseURL, apiKey, _, _, _, _, bedrockRegion := r.snapshot()
	pc := providerCfg{
		providerType:  pt,
		baseURL:       baseURL,
		apiKey:        apiKey,
		bedrockRegion: bedrockRegion,
	}
	return r.completeSingleWithCfg(ctx, pc, model, system, user, maxTokens, maxRetries, callStart, providerID)
}

// completeSingleWithCfg executes one LLM call against the given provider
// config. This is the single entry point that actually invokes GoAI.
func (r *Router) completeSingleWithCfg(ctx context.Context, pc providerCfg, model, system, user string,
	maxTokens, maxRetries int, callStart time.Time, providerID string) (string, error) {

	goaiModel := buildGoAIModel(pc, model)
	result, err := goai.GenerateText(ctx, goaiModel,
		goai.WithSystem(system),
		goai.WithPrompt(user),
		goai.WithMaxOutputTokens(maxTokens),
		goai.WithTemperature(0.2),
		goai.WithMaxRetries(maxRetries),
		goai.WithTimeout(300*time.Second),
		goai.WithProviderOptions(map[string]any{"useResponsesAPI": false}),
	)
	if err != nil {
		if recordLLMMetrics != nil {
			recordLLMMetrics(model, "error", time.Since(callStart), 0, 0)
		}
		return "", fmt.Errorf("LLM call failed: %w", err)
	}
	if recordLLMMetrics != nil {
		recordLLMMetrics(model, "success", time.Since(callStart),
			result.TotalUsage.InputTokens, result.TotalUsage.OutputTokens)
	}
	return result.Text, nil
}

// recordLLMMetrics is set by main.go via SetMetricsRecorder. This avoids a
// circular import (llm -> metrics -> ... -> llm). If nil, metrics are skipped.
var recordLLMMetrics func(model, status string, duration time.Duration, inputTokens, outputTokens int)

// SetMetricsRecorder wires in the metrics recorder. Called from main.go.
func SetMetricsRecorder(fn func(model, status string, duration time.Duration, inputTokens, outputTokens int)) {
	recordLLMMetrics = fn
}

// LLMCallStart / LLMCallEnd hooks let main.go track the in-flight gauge
// without creating a dependency cycle.
var (
	llmCallStartHook func()
	llmCallEndHook   func()
)

// LLMCallStart invokes the in-flight start hook (if wired).
func LLMCallStart() {
	if llmCallStartHook != nil {
		llmCallStartHook()
	}
}

// LLMCallEnd invokes the in-flight end hook (if wired).
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

// completeJSON calls the LLM and parses the response as JSON. Appends a
// "Output ONLY a valid JSON object" instruction to suppress reasoning prose.
func (r *Router) completeJSON(ctx context.Context, model, system, user string, maxTokens int) (map[string]any, error) {
	prompt := user + "\n\nIMPORTANT: Output ONLY a valid JSON object. " +
		"Do NOT include reasoning, explanation, or prose. " +
		"Start your response with { and end with }. No text before or after the JSON."
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
// Doubles each retry, with ±25% jitter, capped at 30s.
func (r *Router) backoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := r.retryBaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	delay := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	jitterRange := delay / 4
	if jitterRange > 0 {
		jitter := time.Duration(time.Now().UnixNano() % int64(jitterRange*2))
		delay = delay - jitterRange + jitter
	}
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
	var secs int
	if _, err := fmt.Sscanf(value, "%d", &secs); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// isRequestTooLargeError reports whether err indicates the request itself
// exceeds the model's or provider's token budget. Retrying the SAME payload
// can never succeed in this case — the payload must shrink.
//
// Detection combines three signals, because providers phrase this
// differently (all real strings seen in production):
//   - goai's typed ContextOverflowError / IsOverflow patterns
//   - Groq's TPM phrasing: "Request too large for model ... on tokens per
//     minute (TPM): Limit 8000, Requested 13271, please reduce your
//     message size and try again" (413/400 — NOT caught by goai's patterns,
//     which expect "reduce the length of the messages")
//   - generic context-window phrasings (OpenAI, Anthropic, Bedrock, ...)
func isRequestTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	var overflow *goai.ContextOverflowError
	if errors.As(err, &overflow) {
		return true
	}
	return isRequestTooLargeMessage(err.Error())
}

func isRequestTooLargeMessage(msg string) bool {
	if msg == "" {
		return false
	}
	if goai.IsOverflow(msg) {
		return true
	}
	m := strings.ToLower(msg)
	patterns := []string{
		"request too large",        // Groq TPM (run #11: killed shipping-api)
		"too large for model",      // Groq TPM variant
		"reduce your message size", // Groq TPM variant
		"message size too large",
		"context length", // OpenAI context_length_exceeded
		"context window", // generic
		"token limit",
		"exceeds the model",
		"exceeds the maximum",
		"prompt is too long", // Anthropic
	}
	for _, p := range patterns {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// isRateLimitWindowError reports whether err looks like a rate limit where
// the request FITS the budget but the provider's time window is exhausted
// (429 / "try again in Xs"). Distinct from too-large: waiting helps here.
func isRateLimitWindowError(err error) bool {
	if err == nil {
		return false
	}
	if isRequestTooLargeError(err) {
		return false // too-large first: waiting never helps there
	}
	var apiErr *goai.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 429 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "try again in")
}

// rateLimitRetryDelay picks the wait before a rate-limit retry.
// Per-minute token windows (TPM/RPM) reset on a ~60s cadence, so the waits
// must be tens of seconds, not the sub-second backoff used for transient
// capacity errors. "try again in Xs" hints are honored when parseable.
func rateLimitRetryDelay(err error, attempt int) time.Duration {
	// Honor explicit "try again in 34s"-style hints when present.
	if m := regexp.MustCompile(`try again in (\d+(?:\.\d+)?)s`).FindStringSubmatch(err.Error()); len(m) == 2 {
		var secs float64
		if _, perr := fmt.Sscanf(m[1], "%f", &secs); perr == nil && secs > 0 {
			d := time.Duration(secs * float64(time.Second))
			if d > 90*time.Second {
				d = 90 * time.Second
			}
			if d < time.Second {
				d = time.Second
			}
			return d + 2*time.Second // small buffer past the window edge
		}
	}
	// TPM/RPM windows: attempt 1 waits ~25s, attempt 2 waits ~55s.
	switch attempt {
	case 1:
		return 25 * time.Second
	default:
		return 55 * time.Second
	}
}

// shrinkUserPrompt reduces a too-large prompt while preserving the head
// (service identity, anomaly summary, detected language, crash location)
// and the tail (task instructions and output-format requirements). The
// bulk — pod logs, file trees, search results — lives in the middle and is
// what gets dropped. Each call keeps roughly 60% of the previous length,
// so three iterations bring a 13k-token request under an 8k-token budget.
// Returns the input unchanged once it is too small to shed further.
func shrinkUserPrompt(s string) string {
	const minShrinkable = 2048 // below this there is nothing meaningful to shed
	if len(s) <= minShrinkable {
		return s
	}
	headLen := len(s) * 20 / 100
	tailLen := len(s) * 40 / 100

	head := s[:headLen]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i] // snap to a line boundary — never cut mid-line
	}
	tail := s[len(s)-tailLen:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:] // snap to a line boundary
	}

	const marker = "\n[... Sentinel: middle context truncated to fit the model token limit ...]\n"
	out := head + marker + tail
	if len(out) >= len(s) {
		return s
	}
	return out
}

// isTransientCapacityError reports whether msg looks like a transient
// capacity/rate-limit issue worth retrying. Provider-agnostic.
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
		"worker",
		"temporarily", "temporary",
		"retry", "try again",
		"service unavailable", "unavailable",
		"timeout", "timed out",
		"503", "502", "504",
		"pool",
		"concurrent", "concurrency",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// ExtractJSON defensively parses JSON from LLM output. Exported so other
// packages (agents) can use it for parsing metadata JSON from
// SEARCH/REPLACE-formatted LLM output.
func ExtractJSON(text string) map[string]any {
	return extractJSON(text)
}

var (
	jsonBlockRe = regexp.MustCompile("(?s)" + "```(?:[a-zA-Z]+)?\\s*(\\{.*?\\}|\\[.*?\\])```")
	bareJSONRe  = regexp.MustCompile(`(?s)(\{.*\}|\[.*\])`)
)

// extractJSON defensively parses JSON from LLM output. Handles markdown
// fences, prose around JSON, and truncated JSON (max_tokens cutoff).
func extractJSON(text string) map[string]any {
	if m := jsonBlockRe.FindStringSubmatch(text); len(m) >= 2 {
		if v, ok := tryParse(m[1]); ok {
			return v
		}
	}
	if m := bareJSONRe.FindStringSubmatch(text); len(m) >= 1 {
		if v, ok := tryParse(m[0]); ok {
			return v
		}
	}
	if v, ok := tryParse(text); ok {
		return v
	}

	lines := strings.Split(text, "\n")
	var jsonStarts []int
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			jsonStarts = append(jsonStarts, i)
		}
	}
	for i := len(jsonStarts) - 1; i >= 0; i-- {
		candidate := strings.Join(lines[jsonStarts[i]:], "\n")
		if v, ok := tryParse(candidate); ok {
			return v
		}
		if repaired := repairTruncated(candidate); repaired != "" {
			if v, ok := tryParse(repaired); ok {
				logging.Info("extractJSON recovered via prose-strip + repair", "start_line", jsonStarts[i])
				return v
			}
		}
	}

	if repaired := repairTruncated(text); repaired != "" {
		if v, ok := tryParse(repaired); ok {
			return v
		}
	}

	preview := text
	if len(preview) > 500 {
		preview = preview[:500] + "...(truncated)"
	}
	logging.Error("extractJSON FAILED — raw response", "len", len(text), "preview", preview)
	return map[string]any{}
}

func tryParse(s string) (map[string]any, bool) {
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v, true
	}
	return nil, false
}

// repairTruncated closes unclosed strings/braces/brackets in JSON that
// was cut off by max_tokens. Tracks opens as a stack and closes in reverse.
func repairTruncated(text string) string {
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}
	s := text[start:]

	inString := false
	escapeNext := false
	var stack []rune
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
	s = strings.TrimRight(strings.TrimSpace(s), ",")
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			s += "}"
		case '[':
			s += "]"
		}
	}
	return s
}

// noopCall logs the prompt and returns a stub JSON response. Used when the
// router is in noop mode (no LLM credentials configured).
func (r *Router) noopCall(ctx context.Context, callType, system, user string) (map[string]any, error) {
	logging.Info("noop LLM call (no API key configured)",
		"call_type", callType,
		"system_prompt_len", len(system),
		"user_prompt_len", len(user),
		"system_preview", truncatePrompt(system, 200),
		"user_preview", truncatePrompt(user, 200),
	)
	return map[string]any{
		"status":  "noop",
		"message": "LLM not configured — set SENTINEL_LLM_API_KEY to enable autonomous diagnosis",
	}, nil
}

// noopText logs the prompt and returns a stub text response.
func (r *Router) noopText(ctx context.Context, callType, system, user string) (string, error) {
	logging.Info("noop LLM call (no API key configured)",
		"call_type", callType,
		"system_prompt_len", len(system),
		"user_prompt_len", len(user),
		"system_preview", truncatePrompt(system, 200),
		"user_preview", truncatePrompt(user, 200),
	)
	return "", nil
}

func truncatePrompt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

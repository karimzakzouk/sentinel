#!/usr/bin/env python3
"""Add noop mode to LLM router for auto-degrade/observe-only mode."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
router_path = REPO / "internal/llm/router.go"
r = router_path.read_text()

# 1. Add noop field to Router struct
old_struct = """type Router struct {
\tmu             sync.RWMutex
\tproviderType   string
\tbaseURL        string
\tapiKey         string
\tfastModel      string
\tfrontierModel  string
\tbedrockRegion  string
\tmaxRetries     int
\tretryBaseDelay time.Duration

\tpool  *Pool
\tcache *SemanticCache
}"""

new_struct = """type Router struct {
\tmu             sync.RWMutex
\tproviderType   string
\tbaseURL        string
\tapiKey         string
\tfastModel      string
\tfrontierModel  string
\tbedrockRegion  string
\tmaxRetries     int
\tretryBaseDelay time.Duration

\tpool  *Pool
\tcache *SemanticCache

\t// noop, when true, makes every call return a stub response and logs the
\t// prompt instead of making an API call. Used when SENTINEL_LLM_API_KEY is
\t// empty (auto-degrade) or SENTINEL_OBSERVE_ONLY=true.
\tnoop bool
}"""

assert old_struct in r, "router.go: Router struct not found"
r = r.replace(old_struct, new_struct, 1)

# 2. Add NewNoop constructor after NewWithProvider
old_new = """// NewWithProvider returns a Router configured for a specific LLM provider.
// See goai_provider.go for the full list of supported providerType values.
func NewWithProvider(providerType, baseURL, apiKey, fastModel, frontierModel string) *Router {
\treturn &Router{
\t\tproviderType:   providerType,
\t\tbaseURL:        strings.TrimRight(baseURL, "/"),
\t\tapiKey:         apiKey,
\t\tfastModel:      fastModel,
\t\tfrontierModel:  frontierModel,
\t\tmaxRetries:     3,
\t\tretryBaseDelay: 500 * time.Millisecond,
\t}
}"""

new_new = """// NewWithProvider returns a Router configured for a specific LLM provider.
// See goai_provider.go for the full list of supported providerType values.
func NewWithProvider(providerType, baseURL, apiKey, fastModel, frontierModel string) *Router {
\treturn &Router{
\t\tproviderType:   providerType,
\t\tbaseURL:        strings.TrimRight(baseURL, "/"),
\t\tapiKey:         apiKey,
\t\tfastModel:      fastModel,
\t\tfrontierModel:  frontierModel,
\t\tmaxRetries:     3,
\t\tretryBaseDelay: 500 * time.Millisecond,
\t}
}

// NewNoop returns a Router that never calls an LLM. Every call logs the
// prompt and returns a stub response. Used when LLM credentials are not
// configured (auto-degrade) or in observe-only mode.
func NewNoop() *Router {
\treturn &Router{
\t\tnoop:           true,
\t\tproviderType:   "noop",
\t\tfastModel:      "noop",
\t\tfrontierModel:  "noop",
\t\tmaxRetries:     0,
\t\tretryBaseDelay: 0,
\t}
}

// IsNoop reports whether the router is in noop mode (no real LLM calls).
func (r *Router) IsNoop() bool {
\tr.mu.RLock()
\tdefer r.mu.RUnlock()
\treturn r.noop
}"""

assert old_new in r, "router.go: NewWithProvider not found"
r = r.replace(old_new, new_new, 1)

# 3. Add noop short-circuit to the 4 call methods
old_fast_json = """// FastJSON calls the fast model and parses the response as JSON.
func (r *Router) FastJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
\t_, _, _, fastModel, _, _, _, _ := r.snapshot()
\treturn r.completeJSON(ctx, fastModel, system, user, maxTokens)
}"""

new_fast_json = """// FastJSON calls the fast model and parses the response as JSON.
func (r *Router) FastJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
\tif r.IsNoop() {
\t\treturn r.noopCall(ctx, "fast_json", system, user)
\t}
\t_, _, _, fastModel, _, _, _, _ := r.snapshot()
\treturn r.completeJSON(ctx, fastModel, system, user, maxTokens)
}"""

assert old_fast_json in r, "router.go: FastJSON not found"
r = r.replace(old_fast_json, new_fast_json, 1)

old_frontier_json = """// FrontierJSON calls the frontier model and parses the response as JSON.
func (r *Router) FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
\t_, _, _, _, frontierModel, _, _, _ := r.snapshot()
\treturn r.completeJSON(ctx, frontierModel, system, user, maxTokens)
}"""

new_frontier_json = """// FrontierJSON calls the frontier model and parses the response as JSON.
func (r *Router) FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
\tif r.IsNoop() {
\t\treturn r.noopCall(ctx, "frontier_json", system, user)
\t}
\t_, _, _, _, frontierModel, _, _, _ := r.snapshot()
\treturn r.completeJSON(ctx, frontierModel, system, user, maxTokens)
}"""

assert old_frontier_json in r, "router.go: FrontierJSON not found"
r = r.replace(old_frontier_json, new_frontier_json, 1)

old_fast_text = """// FastText calls the fast model and returns the raw text.
func (r *Router) FastText(ctx context.Context, system, user string, maxTokens int) (string, error) {
\t_, _, _, fastModel, _, _, _, _ := r.snapshot()
\treturn r.complete(ctx, fastModel, system, user, maxTokens)
}"""

new_fast_text = """// FastText calls the fast model and returns the raw text.
func (r *Router) FastText(ctx context.Context, system, user string, maxTokens int) (string, error) {
\tif r.IsNoop() {
\t\treturn r.noopText(ctx, "fast_text", system, user)
\t}
\t_, _, _, fastModel, _, _, _, _ := r.snapshot()
\treturn r.complete(ctx, fastModel, system, user, maxTokens)
}"""

assert old_fast_text in r, "router.go: FastText not found"
r = r.replace(old_fast_text, new_fast_text, 1)

old_frontier_text = """// FrontierText calls the frontier model and returns the raw text.
func (r *Router) FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error) {
\t_, _, _, _, frontierModel, _, _, _ := r.snapshot()
\treturn r.complete(ctx, frontierModel, system, user, maxTokens)
}"""

new_frontier_text = """// FrontierText calls the frontier model and returns the raw text.
func (r *Router) FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error) {
\tif r.IsNoop() {
\t\treturn r.noopText(ctx, "frontier_text", system, user)
\t}
\t_, _, _, _, frontierModel, _, _, _ := r.snapshot()
\treturn r.complete(ctx, frontierModel, system, user, maxTokens)
}"""

assert old_frontier_text in r, "router.go: FrontierText not found"
r = r.replace(old_frontier_text, new_frontier_text, 1)

# 4. Add the noopCall + noopText helper methods at the end of the file
helpers = """

// noopCall logs the prompt and returns a stub JSON response. Used when the
// router is in noop mode (no LLM credentials configured).
func (r *Router) noopCall(ctx context.Context, callType, system, user string) (map[string]any, error) {
\tlogging.Info("noop LLM call (no API key configured)",
\t\t"call_type", callType,
\t\t"system_prompt_len", len(system),
\t\t"user_prompt_len", len(user),
\t\t"system_preview", truncatePrompt(system, 200),
\t\t"user_preview", truncatePrompt(user, 200),
\t)
\treturn map[string]any{
\t\t"status":  "noop",
\t\t"message": "LLM not configured — set SENTINEL_LLM_API_KEY to enable autonomous diagnosis",
\t}, nil
}

// noopText logs the prompt and returns a stub text response.
func (r *Router) noopText(ctx context.Context, callType, system, user string) (string, error) {
\tlogging.Info("noop LLM call (no API key configured)",
\t\t"call_type", callType,
\t\t"system_prompt_len", len(system),
\t\t"user_prompt_len", len(user),
\t\t"system_preview", truncatePrompt(system, 200),
\t\t"user_preview", truncatePrompt(user, 200),
\t)
\treturn "", nil
}

func truncatePrompt(s string, max int) string {
\tif len(s) <= max {
\t\treturn s
\t}
\treturn s[:max] + "..."
}
"""

r = r.rstrip() + helpers
router_path.write_text(r)
print("OK: router.go — noop mode added")

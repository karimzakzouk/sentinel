package llmcost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Complexity is the task-complexity classification returned by
// Router.Classify.
type Complexity int

const (
	// ComplexityTrivial is a pattern-match / known-error query: short
	// prompt, no multi-step reasoning required.
	ComplexityTrivial Complexity = iota
	// ComplexityMedium is a summarize / classify / extract query.
	ComplexityMedium
	// ComplexityHard is a novel fix proposal or root-cause analysis:
	// long prompt, multi-step reasoning, code generation.
	ComplexityHard
)

// String returns a human-readable label for c.
func (c Complexity) String() string {
	switch c {
	case ComplexityTrivial:
		return "trivial"
	case ComplexityMedium:
		return "medium"
	case ComplexityHard:
		return "hard"
	}
	return "unknown"
}

// Completer is the LLM-call surface Router uses. *llm.Router satisfies
// this interface; tests provide a fake.
type Completer interface {
	// FastText calls the fast/cheap model.
	FastText(ctx context.Context, system, user string, maxTokens int) (string, error)
	// FrontierText calls the frontier/expensive model.
	FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error)
}

// modelGetter exposes the fast/frontier model names (for cost
// estimation). *llm.Router satisfies this via Snapshot().
type modelGetter interface {
	// Snapshot returns baseURL, apiKey, fastModel, frontierModel.
	Snapshot() (string, string, string, string)
}

// Response is what Complete returns.
type Response struct {
	Text         string
	Model        string
	FromCache    bool
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Complexity   Complexity
}

// Stats tracks cumulative Router metrics for observability + dashboards.
type Stats struct {
	Total          int64
	CacheHits      int64
	CacheMisses    int64
	TrivialRouted  int64
	MediumRouted   int64
	HardRouted     int64
	TotalCostUSD   float64
	TotalTokensIn  int64
	TotalTokensOut int64
}

// stats is the internal stats container with its own mutex.
type stats struct {
	mu sync.Mutex
	s  Stats
}

func (s *stats) inc(f func(*Stats)) {
	s.mu.Lock()
	f(&s.s)
	s.mu.Unlock()
}

// snapshot returns a point-in-time copy of the stats.
func (s *stats) snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

// internalEntry is one row in the internal fallback cache (used when
// *llm.SemanticCache is nil but an embedder is configured).
type internalEntry struct {
	promptHash string
	embedding  []float32
	response   string
	createdAt  time.Time
}

// Router is the cost-aware front door. It wraps a Completer (usually
// *llm.Router), classifies each prompt by complexity, routes to the
// fast vs frontier model, and serves from a semantic cache when one is
// available.
type Router struct {
	completer     Completer
	fastModel     string
	frontierModel string

	// cache is the primary semantic cache (usually *llm.SemanticCache).
	// May be nil — in that case caching falls back to the internal map.
	cache *llm.SemanticCache

	// embedder + threshold drive the internal fallback cache when
	// cache is nil. If both cache and embedder are nil, no caching.
	embedder  emb.Embedder
	threshold float64

	mu       sync.Mutex
	internal map[string]*internalEntry

	stats stats
}

// New returns a Router that wraps router. cache may be nil — in that
// case the Router uses embedder + threshold for an internal fallback
// cache. If both cache and embedder are nil, no caching is performed.
//
// threshold is the cosine similarity above which two prompts are
// considered the same (0.95 = near-identical, conservative).
func New(router *llm.Router, cache *llm.SemanticCache, embedder emb.Embedder, threshold float64) *Router {
	r := &Router{
		cache:     cache,
		embedder:  embedder,
		threshold: threshold,
		internal:  make(map[string]*internalEntry),
	}
	if mg, ok := any(router).(modelGetter); ok {
		_, _, fast, frontier := mg.Snapshot()
		r.fastModel = fast
		r.frontierModel = frontier
	}
	if cr, ok := any(router).(Completer); ok {
		r.completer = cr
	}
	if threshold <= 0 {
		r.threshold = 0.95
	}
	return r
}

// newForTest is the test-only constructor that takes any Completer and
// explicit model names. Not used by production callers.
func newForTest(completer Completer, fastModel, frontierModel string, cache *llm.SemanticCache, embedder emb.Embedder, threshold float64) *Router {
	r := &Router{
		completer:     completer,
		fastModel:     fastModel,
		frontierModel: frontierModel,
		cache:         cache,
		embedder:      embedder,
		threshold:     threshold,
		internal:      make(map[string]*internalEntry),
	}
	if threshold <= 0 {
		r.threshold = 0.95
	}
	return r
}

// Classify returns the task complexity for prompt. Classification is a
// pure heuristic on prompt length + keyword hints — it does NOT call
// the LLM. This is intentional: classification itself must be free.
func (r *Router) Classify(prompt string) Complexity {
	lower := strings.ToLower(prompt)

	// Keyword signals for hard tasks (multi-step reasoning required).
	hardSignals := []string{
		"root cause", "root-cause", "investigate", "diagnose",
		"fix proposal", "fix_proposal", "propose a fix",
		"debug", "traceback", "stack trace",
		"code review", "diff", "patch",
		"multi-step", "step by step",
	}
	for _, s := range hardSignals {
		if strings.Contains(lower, s) {
			return ComplexityHard
		}
	}

	// Keyword signals for medium tasks (summarize / classify / extract).
	mediumSignals := []string{
		"summarize", "summary", "classify", "label", "tag",
		"extract", "parse", "translate", "embed",
		"category", "categorize",
	}
	for _, s := range mediumSignals {
		if strings.Contains(lower, s) {
			return ComplexityMedium
		}
	}

	// Keyword signals for trivial tasks (pattern match / known error).
	trivialSignals := []string{
		"is known error", "known pattern", "pattern match",
		"have we seen", "seen before",
	}
	for _, s := range trivialSignals {
		if strings.Contains(lower, s) {
			return ComplexityTrivial
		}
	}

	// Prompt-length fallback: very short prompts are usually trivial,
	// medium prompts are medium, long prompts are hard.
	switch {
	case len(prompt) < 200:
		return ComplexityTrivial
	case len(prompt) < 1500:
		return ComplexityMedium
	}
	return ComplexityHard
}

// Complete routes a prompt through cache → classifier → model. The
// returned Response includes the model used, token estimates, cost
// estimate, and whether the response came from cache.
func (r *Router) Complete(ctx context.Context, prompt string, maxTokens int) (Response, error) {
	r.stats.inc(func(s *Stats) { s.Total++ })

	// 1. Primary cache (*llm.SemanticCache).
	if r.cache != nil && r.cache.Enabled() {
		if cached, ok := r.cache.Get(ctx, prompt); ok {
			r.stats.inc(func(s *Stats) { s.CacheHits++ })
			logging.Debug("llmcost: cache hit (primary)",
				"model", "", "prompt_hash_short", shortHash(prompt))
			return Response{
				Text:         cached,
				FromCache:    true,
				InputTokens:  0,
				OutputTokens: estimateTokens(cached),
			}, nil
		}
	}

	// 2. Internal fallback cache (exact match first, then semantic).
	if r.cache == nil && r.embedder != nil {
		if cached, ok := r.internalLookup(ctx, prompt); ok {
			r.stats.inc(func(s *Stats) { s.CacheHits++ })
			logging.Debug("llmcost: cache hit (internal)", "prompt_hash_short", shortHash(prompt))
			return Response{
				Text:         cached,
				FromCache:    true,
				OutputTokens: estimateTokens(cached),
			}, nil
		}
	}

	r.stats.inc(func(s *Stats) { s.CacheMisses++ })

	// 3. Classify + route.
	complexity := r.Classify(prompt)
	var model string
	var text string
	var err error
	switch complexity {
	case ComplexityTrivial, ComplexityMedium:
		model = r.fastModel
		text, err = r.completer.FastText(ctx, "", prompt, maxTokens)
	case ComplexityHard:
		model = r.frontierModel
		text, err = r.completer.FrontierText(ctx, "", prompt, maxTokens)
	}
	if err != nil {
		return Response{}, fmt.Errorf("llmcost: LLM call (complexity=%s, model=%s): %w", complexity, model, err)
	}

	// 4. Estimate tokens + cost.
	inputTokens := estimateTokens(prompt)
	outputTokens := estimateTokens(text)
	cost := r.Cost(model, inputTokens, outputTokens)

	r.stats.inc(func(s *Stats) {
		switch complexity {
		case ComplexityTrivial:
			s.TrivialRouted++
		case ComplexityMedium:
			s.MediumRouted++
		case ComplexityHard:
			s.HardRouted++
		}
		s.TotalCostUSD += cost
		s.TotalTokensIn += int64(inputTokens)
		s.TotalTokensOut += int64(outputTokens)
	})

	// 5. Store in cache.
	if len(text) > 20 {
		if r.cache != nil && r.cache.Enabled() {
			r.cache.Put(ctx, prompt, text)
		} else if r.embedder != nil {
			r.internalStore(ctx, prompt, text)
		}
	}

	return Response{
		Text:         text,
		Model:        model,
		FromCache:    false,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostUSD:      cost,
		Complexity:   complexity,
	}, nil
}

// Cost returns the estimated USD cost for inputTokens + outputTokens on
// model. Unknown models fall back to default pricing.
func (r *Router) Cost(model string, inputTokens, outputTokens int) float64 {
	return costPerTokens(model, inputTokens, outputTokens)
}

// Stats returns a point-in-time copy of cumulative Router metrics.
func (r *Router) Stats() Stats {
	return r.stats.snapshot()
}

// --- internal fallback cache ---

func (r *Router) internalLookup(ctx context.Context, prompt string) (string, bool) {
	hash := hashPrompt(prompt)
	r.mu.Lock()
	defer r.mu.Unlock()

	// Exact match.
	if e, ok := r.internal[hash]; ok && r.fresh(e) {
		return e.response, true
	}

	// Semantic match.
	if r.embedder == nil {
		return "", false
	}
	embed, err := r.embedder.Embed(ctx, prompt)
	if err != nil {
		return "", false
	}
	var best *internalEntry
	var bestSim float64
	for _, e := range r.internal {
		if !r.fresh(e) {
			continue
		}
		sim := cosineSim(embed, e.embedding)
		if sim >= r.threshold && sim > bestSim {
			best = e
			bestSim = sim
		}
	}
	if best != nil {
		return best.response, true
	}
	return "", false
}

func (r *Router) internalStore(ctx context.Context, prompt, response string) {
	if r.embedder == nil {
		return
	}
	embed, err := r.embedder.Embed(ctx, prompt)
	if err != nil {
		return
	}
	hash := hashPrompt(prompt)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.internal[hash] = &internalEntry{
		promptHash: hash,
		embedding:  embed,
		response:   response,
		createdAt:  time.Now(),
	}
}

// fresh reports whether the entry is still within the 24h TTL.
func (r *Router) fresh(e *internalEntry) bool {
	return time.Since(e.createdAt) < 24*time.Hour
}

// --- helpers ---

// hashPrompt returns the SHA-256 hex of s.
func hashPrompt(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// shortHash returns the first 12 hex chars of hashPrompt(s), for logging.
func shortHash(s string) string {
	h := hashPrompt(s)
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// estimateTokens returns a rough token estimate for s. LLM tokenizers
// average ~4 chars/token for English text; this is precise enough for
// cost estimation (the actual tokenizer would require a network call).
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

// cosineSim computes cosine similarity between two equal-length float32
// vectors. Returns 0 if lengths differ or either vector is zero.
func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		af := float64(a[i])
		bf := float64(b[i])
		dot += af * bf
		na += af * af
		nb += bf * bf
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

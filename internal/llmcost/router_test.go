package llmcost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/llm"
)

// fakeCompleter records every call and returns a canned response.
type fakeCompleter struct {
	mu         sync.Mutex
	fastCalls  int
	frontCalls int
}

func (f *fakeCompleter) FastText(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fastCalls++
	return "fast-response: this is a long enough canned response to exceed the 20-char cache threshold", nil
}

func (f *fakeCompleter) FrontierText(_ context.Context, _, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frontCalls++
	return "frontier-response: this is a long enough canned response to exceed the 20-char cache threshold", nil
}

func (f *fakeCompleter) fastCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fastCalls
}

func (f *fakeCompleter) frontCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frontCalls
}

// failingCompleter always returns an error.
type failingCompleter struct{}

func (failingCompleter) FastText(_ context.Context, _, _ string, _ int) (string, error) {
	return "", errors.New("upstream unavailable")
}

func (failingCompleter) FrontierText(_ context.Context, _, _ string, _ int) (string, error) {
	return "", errors.New("upstream unavailable")
}

// fakeSemanticEmbedder returns deterministic, semantic-ish vectors.
// Same text → same vector. Similar texts → high cosine similarity.
type fakeSemanticEmbedder struct{ dim int }

func (e *fakeSemanticEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, e.dim)
	if len(text) == 0 {
		return v, nil
	}
	for i, c := range text {
		idx := (i + int(c)) % e.dim
		v[idx] += float32(c) / 255.0
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		inv := 1.0 / sum
		for i := range v {
			v[i] = float32(float64(v[i]) * inv)
		}
	}
	return v, nil
}

func (e *fakeSemanticEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, _ := e.Embed(ctx, t)
		out[i] = v
	}
	return out, nil
}

func (e *fakeSemanticEmbedder) Dim() int         { return e.dim }
func (e *fakeSemanticEmbedder) Name() string     { return "fake-semantic" }
func (e *fakeSemanticEmbedder) IsSemantic() bool { return true }

// TestLLMCost_ClassifiesTrivial verifies short prompts and trivial
// keyword hints classify as ComplexityTrivial.
func TestLLMCost_ClassifiesTrivial(t *testing.T) {
	r := newForTest(&fakeCompleter{}, "fast", "frontier", nil, nil, 0)
	cases := []string{
		"is known error: CrashLoopBackOff?",
		"have we seen this before?",
		"known pattern: OOMKilled",
		"status?", // very short
	}
	for _, p := range cases {
		if got := r.Classify(p); got != ComplexityTrivial {
			t.Errorf("Classify(%q) = %s, want trivial", p, got)
		}
	}
}

// TestLLMCost_ClassifiesMedium verifies summarize/classify prompts and
// medium-length prompts classify as ComplexityMedium.
func TestLLMCost_ClassifiesMedium(t *testing.T) {
	r := newForTest(&fakeCompleter{}, "fast", "frontier", nil, nil, 0)
	cases := []string{
		"summarize this alert",
		"classify this incident",
		"extract the error code from this log",
		strings.Repeat("this is a medium-length prompt. ", 30), // ~960 chars, no hard signals
	}
	for _, p := range cases {
		if got := r.Classify(p); got != ComplexityMedium {
			t.Errorf("Classify(%q) = %s, want medium", p, got)
		}
	}
}

// TestLLMCost_ClassifiesHard verifies fix-proposal / root-cause / long
// prompts classify as ComplexityHard.
func TestLLMCost_ClassifiesHard(t *testing.T) {
	r := newForTest(&fakeCompleter{}, "fast", "frontier", nil, nil, 0)
	cases := []string{
		"investigate the root cause of this incident",
		"propose a fix for this traceback",
		"debug this stack trace and suggest a patch",
		"review this code diff",
		strings.Repeat("this is a very long prompt that requires multi-step reasoning. ", 50), // ~3200 chars
	}
	for _, p := range cases {
		if got := r.Classify(p); got != ComplexityHard {
			t.Errorf("Classify(%q) = %s, want hard", p, got)
		}
	}
}

// TestLLMCost_RoutesToCorrectModel verifies that Complete() calls
// FastText for trivial/medium prompts and FrontierText for hard prompts.
func TestLLMCost_RoutesToCorrectModel(t *testing.T) {
	fc := &fakeCompleter{}
	r := newForTest(fc, "fast-model", "frontier-model", nil, nil, 0)

	// Trivial → fast model.
	resp, err := r.Complete(context.Background(), "is known error: OOM?", 100)
	if err != nil {
		t.Fatalf("Complete(trivial): %v", err)
	}
	if resp.Model != "fast-model" {
		t.Errorf("trivial: Model = %q, want fast-model", resp.Model)
	}
	if resp.Complexity != ComplexityTrivial {
		t.Errorf("trivial: Complexity = %s, want trivial", resp.Complexity)
	}
	if fc.fastCount() != 1 || fc.frontCount() != 0 {
		t.Errorf("trivial: fastCalls=%d frontCalls=%d, want 1/0", fc.fastCount(), fc.frontCount())
	}
	if resp.Text != "fast-response: this is a long enough canned response to exceed the 20-char cache threshold" {
		t.Errorf("trivial: Text = %q, want canned fast-response", resp.Text)
	}

	// Hard → frontier model.
	resp, err = r.Complete(context.Background(), "investigate the root cause of this 5-alarm incident and propose a fix", 1000)
	if err != nil {
		t.Fatalf("Complete(hard): %v", err)
	}
	if resp.Model != "frontier-model" {
		t.Errorf("hard: Model = %q, want frontier-model", resp.Model)
	}
	if resp.Complexity != ComplexityHard {
		t.Errorf("hard: Complexity = %s, want hard", resp.Complexity)
	}
	if fc.frontCount() != 1 {
		t.Errorf("hard: frontCalls=%d, want 1", fc.frontCount())
	}
}

// TestLLMCost_SemanticCacheHit verifies that two identical prompts
// result in exactly one LLM call (the second is served from cache).
// Uses *llm.SemanticCache with a fake semantic embedder.
func TestLLMCost_SemanticCacheHit(t *testing.T) {
	fc := &fakeCompleter{}
	embedder := &fakeSemanticEmbedder{dim: 32}
	cache := llm.NewSemanticCache(embedder, 0.95, time.Hour, 100)
	r := newForTest(fc, "fast", "frontier", cache, embedder, 0.95)

	prompt := "What is the root cause of the OOMKilled error in the api pod?"
	resp1, err := r.Complete(context.Background(), prompt, 200)
	if err != nil {
		t.Fatalf("Complete #1: %v", err)
	}
	if resp1.FromCache {
		t.Error("first call should not be from cache")
	}
	if fc.fastCount()+fc.frontCount() != 1 {
		t.Errorf("after first call: LLM calls = %d, want 1", fc.fastCount()+fc.frontCount())
	}

	// Second identical call — should hit cache.
	resp2, err := r.Complete(context.Background(), prompt, 200)
	if err != nil {
		t.Fatalf("Complete #2: %v", err)
	}
	if !resp2.FromCache {
		t.Error("second call should be from cache")
	}
	if resp2.Text != resp1.Text {
		t.Errorf("cached text %q != original %q", resp2.Text, resp1.Text)
	}
	if fc.fastCount()+fc.frontCount() != 1 {
		t.Errorf("after second call: LLM calls = %d, want 1 (cache hit)", fc.fastCount()+fc.frontCount())
	}

	s := r.Stats()
	if s.CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", s.CacheHits)
	}
	if s.CacheMisses != 1 {
		t.Errorf("CacheMisses = %d, want 1", s.CacheMisses)
	}
}

// TestLLMCost_SemanticCacheMiss verifies that two unrelated prompts
// both miss the cache and trigger LLM calls.
func TestLLMCost_SemanticCacheMiss(t *testing.T) {
	fc := &fakeCompleter{}
	embedder := &fakeSemanticEmbedder{dim: 32}
	cache := llm.NewSemanticCache(embedder, 0.95, time.Hour, 100)
	r := newForTest(fc, "fast", "frontier", cache, embedder, 0.95)

	prompt1 := "What is the root cause of the OOMKilled error in the api pod?"
	prompt2 := "Completely different query about Kubernetes pod scheduling and resource quotas in the cluster."

	if _, err := r.Complete(context.Background(), prompt1, 200); err != nil {
		t.Fatalf("Complete #1: %v", err)
	}
	if _, err := r.Complete(context.Background(), prompt2, 200); err != nil {
		t.Fatalf("Complete #2: %v", err)
	}

	if fc.fastCount()+fc.frontCount() != 2 {
		t.Errorf("LLM calls = %d, want 2 (both misses)", fc.fastCount()+fc.frontCount())
	}

	s := r.Stats()
	if s.CacheHits != 0 {
		t.Errorf("CacheHits = %d, want 0", s.CacheHits)
	}
	if s.CacheMisses != 2 {
		t.Errorf("CacheMisses = %d, want 2", s.CacheMisses)
	}
}

// TestLLMCost_InternalCacheHit verifies the internal fallback cache
// (used when *llm.SemanticCache is nil but embedder is set).
func TestLLMCost_InternalCacheHit(t *testing.T) {
	fc := &fakeCompleter{}
	embedder := &fakeSemanticEmbedder{dim: 32}
	r := newForTest(fc, "fast", "frontier", nil, embedder, 0.95)

	prompt := "is known error: CrashLoopBackOff on api pod?"

	resp1, err := r.Complete(context.Background(), prompt, 100)
	if err != nil {
		t.Fatalf("Complete #1: %v", err)
	}
	if resp1.FromCache {
		t.Error("first call should not be from cache")
	}

	resp2, err := r.Complete(context.Background(), prompt, 100)
	if err != nil {
		t.Fatalf("Complete #2: %v", err)
	}
	if !resp2.FromCache {
		t.Error("second identical call should hit internal cache")
	}
	if resp2.Text != resp1.Text {
		t.Errorf("cached text %q != original %q", resp2.Text, resp1.Text)
	}
	if fc.fastCount()+fc.frontCount() != 1 {
		t.Errorf("LLM calls = %d, want 1", fc.fastCount()+fc.frontCount())
	}
}

// TestLLMCost_CostEstimator verifies the per-model cost table.
func TestLLMCost_CostEstimator(t *testing.T) {
	r := newForTest(&fakeCompleter{}, "fast", "frontier", nil, nil, 0)

	cases := []struct {
		name         string
		model        string
		inputTokens  int
		outputTokens int
		want         float64
		tolerance    float64
	}{
		{"gpt-4o", "gpt-4o", 1000, 500, 0.0025 + 0.005, 0.0001},
		{"gpt-4o-mini", "gpt-4o-mini", 1000, 500, 0.00015 + 0.0003, 0.00001},
		{"glm-4.5", "glm-4.5", 2000, 1000, 0.01 + 0.015, 0.0001},
		{"claude-3-haiku", "claude-3-haiku", 1000, 1000, 0.00025 + 0.00125, 0.0001},
		{"unknown-model", "unknown-model", 1000, 1000, 0.001 + 0.002, 0.0001},
		{"zero-tokens", "gpt-4o", 0, 0, 0, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Cost(c.model, c.inputTokens, c.outputTokens)
			if abs(got-c.want) > c.tolerance {
				t.Errorf("Cost(%q, %d, %d) = %v, want %v (±%v)",
					c.model, c.inputTokens, c.outputTokens, got, c.want, c.tolerance)
			}
		})
	}
}

// TestLLMCost_CostEstimatorLocalModel verifies that local models (llama3,
// mistral, qwen2) report zero cost.
func TestLLMCost_CostEstimatorLocalModel(t *testing.T) {
	r := newForTest(&fakeCompleter{}, "llama3", "frontier", nil, nil, 0)
	for _, m := range []string{"llama3", "mistral", "qwen2"} {
		if got := r.Cost(m, 1000, 1000); got != 0 {
			t.Errorf("Cost(%q) = %v, want 0 (local model)", m, got)
		}
	}
}

// TestLLMCost_CompleteErrorPropagates verifies that LLM errors are
// wrapped with the complexity + model name and returned to the caller.
func TestLLMCost_CompleteErrorPropagates(t *testing.T) {
	r := newForTest(failingCompleter{}, "fast", "frontier", nil, nil, 0)
	_, err := r.Complete(context.Background(), "is known error?", 100)
	if err == nil {
		t.Fatal("expected error from failing completer, got nil")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Errorf("error should wrap upstream message, got: %v", err)
	}
}

// TestLLMCost_StatsTracked verifies that the Stats struct tracks
// routing counts and cost across multiple calls.
func TestLLMCost_StatsTracked(t *testing.T) {
	fc := &fakeCompleter{}
	r := newForTest(fc, "fast", "frontier", nil, nil, 0)

	// 2 trivial calls + 1 hard call.
	_, _ = r.Complete(context.Background(), "is known error: OOM?", 100)
	_, _ = r.Complete(context.Background(), "status?", 100)
	_, _ = r.Complete(context.Background(), "investigate the root cause and propose a fix", 1000)

	s := r.Stats()
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.TrivialRouted != 2 {
		t.Errorf("TrivialRouted = %d, want 2", s.TrivialRouted)
	}
	if s.HardRouted != 1 {
		t.Errorf("HardRouted = %d, want 1", s.HardRouted)
	}
	if s.TotalCostUSD <= 0 {
		t.Errorf("TotalCostUSD = %v, want > 0", s.TotalCostUSD)
	}
	if s.TotalTokensIn <= 0 {
		t.Errorf("TotalTokensIn = %d, want > 0", s.TotalTokensIn)
	}
}

// TestLLMCost_EmbedderInterface verifies that emb.Embedder satisfies the
// embedder surface Router uses (via the *llm.SemanticCache constructor).
func TestLLMCost_EmbedderInterface(t *testing.T) {
	var _ emb.Embedder = (*fakeSemanticEmbedder)(nil)
}

// TestLLMCost_CompleterInterface verifies that *llm.Router satisfies the
// Completer interface (compile-time check).
func TestLLMCost_CompleterInterface(t *testing.T) {
	var _ Completer = (*llm.Router)(nil)
}

// --- helpers ---

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

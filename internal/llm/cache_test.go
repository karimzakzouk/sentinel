package llm

import (
	"context"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/emb"
)

// fakeSemanticEmbedder is a minimal embedder that returns deterministic
// vectors and reports IsSemantic=true so the cache enables itself.
// Different inputs produce different vectors (so similarity < threshold
// for unrelated prompts).
type fakeSemanticEmbedder struct{ dim int }

func (f *fakeSemanticEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, f.dim)
	if len(text) == 0 {
		return v, nil
	}
	// Hash each character into a different dim bucket so different texts
	// produce different vectors. Same text -> same vector (deterministic).
	for i, c := range text {
		idx := (i + int(c)) % f.dim
		v[idx] += float32(c) / 255.0
	}
	// L2 normalize
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

func (f *fakeSemanticEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, _ := f.Embed(ctx, t)
		out[i] = v
	}
	return out, nil
}

func (f *fakeSemanticEmbedder) Dim() int         { return f.dim }
func (f *fakeSemanticEmbedder) Name() string     { return "fake" }
func (f *fakeSemanticEmbedder) IsSemantic() bool { return true }

// TestSemanticCache_DisabledWhenEmbedderIsNonSemantic verifies the cache
// is disabled when the embedder reports IsSemantic=false (e.g. tfidf).
func TestSemanticCache_DisabledWhenEmbedderIsNonSemantic(t *testing.T) {
	e := emb.NewTfIDF(64)
	c := NewSemanticCache(e, 0.97, time.Hour, 100)
	if c.Enabled() {
		t.Error("cache should be disabled for non-semantic embedder")
	}
}

func TestSemanticCache_DisabledWhenEmbedderIsNil(t *testing.T) {
	c := NewSemanticCache(nil, 0.97, time.Hour, 100)
	if c.Enabled() {
		t.Error("cache should be disabled when embedder is nil")
	}
}

func TestSemanticCache_ExactMatch(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, time.Hour, 100)

	prompt := "what is the root cause?"
	c.Put(context.Background(), prompt, "the answer")

	got, ok := c.Get(context.Background(), prompt)
	if !ok {
		t.Fatal("expected exact-match hit")
	}
	if got != "the answer" {
		t.Errorf("got %q, want %q", got, "the answer")
	}
}

func TestSemanticCache_Miss(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, time.Hour, 100)

	c.Put(context.Background(), "what is the root cause?", "answer1")

	_, ok := c.Get(context.Background(), "completely different prompt about weather")
	if ok {
		t.Error("expected miss for unrelated prompt")
	}
}

func TestSemanticCache_Stats(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, time.Hour, 100)

	c.Put(context.Background(), "p1", "r1")
	c.Put(context.Background(), "p2", "r2")

	entries, exact := c.Stats()
	if entries != 2 {
		t.Errorf("entries = %d, want 2", entries)
	}
	if exact != 2 {
		t.Errorf("exact = %d, want 2", exact)
	}
}

// TestSemanticCache_EvictionOnOverflow verifies the cache evicts entries
// when it exceeds maxEntries. This is the v1 fix for the v5 unbounded-growth
// memory leak.
func TestSemanticCache_EvictionOnOverflow(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, time.Hour, 5)

	for i := 0; i < 10; i++ {
		c.Put(context.Background(), "prompt-"+string(rune('a'+i)), "resp")
	}

	entries, _ := c.Stats()
	if entries > 5 {
		t.Errorf("entries = %d, expected <= 5 after eviction", entries)
	}
}

// TestSemanticCache_TTLExpiry verifies that entries past TTL are not served.
func TestSemanticCache_TTLExpiry(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, 50*time.Millisecond, 100)

	c.Put(context.Background(), "prompt", "answer")

	// Immediate hit
	if _, ok := c.Get(context.Background(), "prompt"); !ok {
		t.Error("expected hit before TTL expiry")
	}

	time.Sleep(60 * time.Millisecond)

	// After TTL: miss on the slow path (exact cache is not TTL-evicted
	// automatically; EvictExpired must be called). The slow path checks
	// entry.createdAt.
	if _, ok := c.Get(context.Background(), "prompt"); ok {
		t.Error("expected miss after TTL expiry")
	}
}

func TestSemanticCache_EvictExpired(t *testing.T) {
	e := &fakeSemanticEmbedder{dim: 16}
	c := NewSemanticCache(e, 0.97, 50*time.Millisecond, 100)

	c.Put(context.Background(), "p1", "r1")
	c.Put(context.Background(), "p2", "r2")

	time.Sleep(60 * time.Millisecond)
	c.EvictExpired()

	entries, _ := c.Stats()
	if entries != 0 {
		t.Errorf("after EvictExpired, entries = %d, want 0", entries)
	}
}

func TestSemanticCache_NilSafe(t *testing.T) {
	var c *SemanticCache
	if c.Enabled() {
		t.Error("nil cache should not be enabled")
	}
	if _, ok := c.Get(context.Background(), "x"); ok {
		t.Error("nil cache Get should return false")
	}
	c.Put(context.Background(), "x", "y") // must not panic
	if entries, _ := c.Stats(); entries != 0 {
		t.Error("nil cache Stats should return 0")
	}
	c.EvictExpired() // must not panic
}

func TestHashPrompt(t *testing.T) {
	h1 := hashPrompt("hello")
	h2 := hashPrompt("hello")
	h3 := hashPrompt("world")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("SHA256 hex length = %d, want 64", len(h1))
	}
}

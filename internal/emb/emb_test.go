package emb

import (
	"context"
	"testing"
)

func TestTfIDFBasic(t *testing.T) {
	e := NewTfIDF(64)
	if e.Name() != "tfidf" {
		t.Errorf("Name = %q", e.Name())
	}
	if e.IsSemantic() {
		t.Error("tfidf should not be semantic")
	}
	if e.Dim() != 64 {
		t.Errorf("Dim = %d, want 64", e.Dim())
	}

	v, err := e.Embed(context.Background(), "the quick brown fox")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 64 {
		t.Fatalf("len = %d, want 64", len(v))
	}

	// L2-normalized => sum of squares ~= 1
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq < 0.99 || sumSq > 1.01 {
		t.Errorf("vector not normalized: sum of squares = %f", sumSq)
	}
}

func TestTfIDFEmpty(t *testing.T) {
	e := NewTfIDF(32)
	v, err := e.Embed(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 32 {
		t.Fatalf("len = %d, want 32", len(v))
	}
	for i, x := range v {
		if x != 0 {
			t.Errorf("empty text vector[%d] = %v, want 0", i, x)
		}
	}
}

func TestTfIDFSimilarity(t *testing.T) {
	e := NewTfIDF(256)
	a, _ := e.Embed(context.Background(), "database connection error")
	b, _ := e.Embed(context.Background(), "database connection error")
	c, _ := e.Embed(context.Background(), "totally different topic about weather")

	simSame := CosineSim(a, b)
	if simSame < 0.99 {
		t.Errorf("identical text similarity = %f, want ~1.0", simSame)
	}
	simDiff := CosineSim(a, c)
	if simDiff > 0.5 {
		t.Errorf("different text similarity = %f, want < 0.5", simDiff)
	}
}

func TestTfIDFBatch(t *testing.T) {
	e := NewTfIDF(64)
	vecs, err := e.EmbedBatch(context.Background(), []string{"foo bar", "baz qux"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len = %d, want 2", len(vecs))
	}
}

func TestNewAutoDetect(t *testing.T) {
	// No API key + no model => tfidf
	e, err := New(Config{Dimension: 64})
	if err != nil {
		t.Fatal(err)
	}
	if e.Name() != "tfidf" {
		t.Errorf("auto-detect without API key = %q, want tfidf", e.Name())
	}

	// API key + model => remote
	e2, err := New(Config{APIKey: "k", Model: "text-embedding-3-small"})
	if err != nil {
		t.Fatal(err)
	}
	if e2.Name() != "remote" {
		t.Errorf("auto-detect with API key = %q, want remote", e2.Name())
	}
}

func TestNewRemoteRequiresAPIKey(t *testing.T) {
	_, err := New(Config{Provider: "remote"})
	if err == nil {
		t.Error("expected error for remote without API key")
	}
}

func TestNewUnknownProvider(t *testing.T) {
	_, err := New(Config{Provider: "acme"})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestNewDefaultsDimension(t *testing.T) {
	e, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Dim() != 1536 {
		t.Errorf("default Dim = %d, want 1536", e.Dim())
	}
}

func TestCosineSimDifferentLens(t *testing.T) {
	if got := CosineSim([]float32{1, 2, 3}, []float32{1, 2}); got != 0 {
		t.Errorf("CosineSim different lens = %v, want 0", got)
	}
}

func TestTokenize(t *testing.T) {
	cases := map[string][]string{
		"":                {},
		"hello":           {"hello"},
		"Hello World":     {"hello", "world"},
		"foo_bar baz":     {"foo_bar", "baz"},
		"path/to/file.go": {"path/to/file.go"},
		"123 abc":         {"123", "abc"},
		// punctuation alone is included as a token because '.' and '/' are
		// token characters (file paths). Whitespace alone produces no token.
		"...": {"..."},
		"   ": {},
	}
	for in, want := range cases {
		got := tokenize(in)
		if len(got) != len(want) {
			t.Errorf("tokenize(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

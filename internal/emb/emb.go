// Package emb provides pluggable text embeddings for incident memory and
// the LLM semantic cache.
//
// Two backends:
//
//   - remote: calls an OpenAI-compatible /embeddings endpoint. Requires
//     an API key + model name. IsSemantic() returns true.
//   - tfidf: a local TF-IDF hash embedding. No network. IsSemantic()
//     returns false (cosine similarity is meaningful but not as accurate
//     as a real model).
//
// New picks the backend automatically based on Config: if APIKey and
// Model are both set, remote; otherwise tfidf.
package emb

import (
	"context"
	"fmt"
)

// Embedder turns text into fixed-dim float32 vectors.
type Embedder interface {
	// Embed returns the embedding for one text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch returns embeddings for multiple texts in one call.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	// Dim returns the dimensionality of vectors produced by this embedder.
	Dim() int
	// Name returns the backend identifier ("remote" or "tfidf").
	Name() string
	// IsSemantic reports whether the embeddings are produced by a model
	// (true) or by a local hash function (false). Used by the semantic
	// cache to decide whether to enable similarity search.
	IsSemantic() bool
}

// Config configures an Embedder.
type Config struct {
	Provider  string // "remote", "tfidf", or "" (auto-detect)
	Model     string // e.g. "text-embedding-3-small"
	APIKey    string
	BaseURL   string // e.g. "https://api.openai.com/v1"
	Dimension int    // vector dimension (default 1536)
}

// New returns an Embedder for cfg. If Provider is empty, picks "remote"
// when both APIKey and Model are set, otherwise "tfidf".
func New(cfg Config) (Embedder, error) {
	if cfg.Dimension == 0 {
		cfg.Dimension = 1536
	}
	provider := cfg.Provider
	if provider == "" {
		if cfg.APIKey != "" && cfg.Model != "" {
			provider = "remote"
		} else {
			provider = "tfidf"
		}
	}
	switch provider {
	case "remote":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("embedding provider=remote requires API key")
		}
		if cfg.Model == "" {
			cfg.Model = "text-embedding-3-small"
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.openai.com/v1"
		}
		return newRemote(cfg), nil
	case "tfidf":
		return NewTfIDF(cfg.Dimension), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", provider)
	}
}

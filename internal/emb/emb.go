// Package emb provides pluggable text embeddings for incident memory + LLM semantic cache.
package emb

import (
        "context"
        "fmt"
)

type Embedder interface {
        Embed(ctx context.Context, text string) ([]float32, error)
        EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
        Dim() int
        Name() string
        IsSemantic() bool
}

type Config struct {
        Provider string // "remote", "tfidf", "" (auto-detect)
        Model    string // e.g. "text-embedding-3-small"
        APIKey   string
        BaseURL  string // e.g. "https://api.openai.com/v1"
        Dimension int   // embedding dimension (default 1536)
}

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

// Package emb — TF-IDF embedding backend.
package emb

import (
        "context"
        "math"
        "strings"
        "unicode"
)

type tfidf struct {
        dim int
}

func NewTfIDF(dim int) Embedder {
        return &tfidf{dim: dim}
}

func (t *tfidf) Name() string     { return "tfidf" }
func (t *tfidf) IsSemantic() bool { return false }
func (t *tfidf) Dim() int         { return t.dim }

func (t *tfidf) Embed(ctx context.Context, text string) ([]float32, error) {
        return t.embedOne(text), nil
}

func (t *tfidf) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
        out := make([][]float32, len(texts))
        for i, text := range texts {
                out[i] = t.embedOne(text)
        }
        return out, nil
}

func (t *tfidf) embedOne(text string) []float32 {
        tokens := tokenize(text)
        if len(tokens) == 0 {
                return make([]float32, t.dim)
        }

        // Term frequency
        tf := make(map[string]float64)
        for _, tok := range tokens {
                tf[tok]++
        }
        for k := range tf {
                tf[k] /= float64(len(tokens))
        }

        // Hash each token into the vector with TF weight
        vec := make([]float32, t.dim)
        for tok, weight := range tf {
                h := hash(tok)
                idx := int(h % uint32(t.dim))
                sign := float32(1)
                if h%2 == 1 {
                        sign = -1
                }
                vec[idx] += sign * float32(weight)
        }

        // L2 normalize
        normalize(vec)
        return vec
}

func tokenize(text string) []string {
        var tokens []string
        var sb strings.Builder
        for _, r := range text {
                if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '/' {
                        sb.WriteRune(unicode.ToLower(r))
                } else {
                        if sb.Len() > 0 {
                                tokens = append(tokens, sb.String())
                                sb.Reset()
                        }
                }
        }
        if sb.Len() > 0 {
                tokens = append(tokens, sb.String())
        }
        return tokens
}

func hash(s string) uint32 {
        h := uint32(2166136261)
        for _, c := range s {
                h ^= uint32(c)
                h *= 16777619
        }
        return h
}

func normalize(vec []float32) {
        var sum float64
        for _, v := range vec {
                sum += float64(v) * float64(v)
        }
        norm := math.Sqrt(sum)
        if norm == 0 {
                return
        }
        for i := range vec {
                vec[i] = float32(float64(vec[i]) / norm)
        }
}

func CosineSim(a, b []float32) float32 {
        if len(a) != len(b) {
                return 0
        }
        var dot float64
        for i := range a {
                dot += float64(a[i]) * float64(b[i])
        }
        return float32(dot) // vectors are already normalized
}

// Remote embedding backend — calls OpenAI-compatible /embeddings API.
package emb

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "time"
)

type remote struct {
        apiKey  string
        baseURL string
        model   string
        dim     int
        http    *http.Client
}

func newRemote(cfg Config) *remote {
        return &remote{
                apiKey:  cfg.APIKey,
                baseURL: cfg.BaseURL,
                model:   cfg.Model,
                dim:     cfg.Dimension,
                http: &http.Client{
                        Timeout: 30 * time.Second,
                },
        }
}

func (r *remote) Name() string     { return "remote" }
func (r *remote) IsSemantic() bool { return true }
func (r *remote) Dim() int         { return r.dim }

type embedRequest struct {
        Model string   `json:"model"`
        Input []string `json:"input"`
}

type embedResponse struct {
        Data []struct {
                Embedding []float32 `json:"embedding"`
        } `json:"data"`
}

func (r *remote) Embed(ctx context.Context, text string) ([]float32, error) {
        vecs, err := r.EmbedBatch(ctx, []string{text})
        if err != nil {
                return nil, err
        }
        if len(vecs) == 0 {
                return nil, fmt.Errorf("empty embedding response")
        }
        return vecs[0], nil
}

func (r *remote) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
        if len(texts) == 0 {
                return nil, nil
        }

        payload, _ := json.Marshal(embedRequest{Model: r.model, Input: texts})
        req, err := http.NewRequestWithContext(ctx, "POST", r.baseURL+"/embeddings", bytes.NewReader(payload))
        if err != nil {
                return nil, err
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Authorization", "Bearer "+r.apiKey)

        resp, err := r.http.Do(req)
        if err != nil {
                return nil, fmt.Errorf("embedding API call: %w", err)
        }
        defer resp.Body.Close()

        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return nil, fmt.Errorf("embedding API %d: %s", resp.StatusCode, string(body))
        }

        var result embedResponse
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
                return nil, fmt.Errorf("decode embedding response: %w", err)
        }

        out := make([][]float32, len(result.Data))
        for i, d := range result.Data {
                out[i] = d.Embedding
        }
        return out, nil
}

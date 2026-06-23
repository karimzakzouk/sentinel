// Package feedback — Vector store using HNSW index + LLM embeddings API.
package feedback

import (
        "bytes"
        "context"
        "encoding/json"
        "log"
        "net/http"
        "sync"
        "time"

        "github.com/coder/hnsw"
)

// VectorStore manages incident embeddings + similarity search.
type VectorStore struct {
        index      *hnsw.Graph[uint64]
        mu         sync.RWMutex
        baseURL    string
        apiKey     string
        model      string
        httpClient *http.Client
        metadata   map[uint64]*VectorMetadata
        nextID     uint64
}

// VectorMetadata is the metadata stored alongside each vector.
type VectorMetadata struct {
        IncidentID string `json:"incident_id"`
        Service    string `json:"service"`
        RootCause  string `json:"root_cause"`
        FixType    string `json:"fix_type"`
        FixPattern string `json:"fix_pattern"`
        Outcome    string `json:"outcome"`
}

// NewVectorStore creates a new vector store.
func NewVectorStore(baseURL, apiKey, model string) *VectorStore {
        return &VectorStore{
                index:      hnsw.NewGraph[uint64](),
                baseURL:    baseURL,
                apiKey:     apiKey,
                model:      model,
                httpClient: &http.Client{Timeout: 30 * time.Second},
                metadata:   make(map[uint64]*VectorMetadata),
        }
}

// GenerateEmbedding calls the LLM provider's embeddings API.
func (v *VectorStore) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
        if v.apiKey == "" || v.baseURL == "" {
                return hashEmbedding(text, 1536), nil
        }

        reqBody := map[string]any{"model": v.model, "input": text}
        bodyBytes, _ := json.Marshal(reqBody)

        req, err := http.NewRequestWithContext(ctx, "POST", v.baseURL+"/embeddings", bytes.NewReader(bodyBytes))
        if err != nil {
                return hashEmbedding(text, 1536), nil
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Authorization", "Bearer "+v.apiKey)

        resp, err := v.httpClient.Do(req)
        if err != nil {
                log.Printf("[vector] embeddings API failed, using fallback: %v", err)
                return hashEmbedding(text, 1536), nil
        }
        defer resp.Body.Close()

        if resp.StatusCode != 200 {
                return hashEmbedding(text, 1536), nil
        }

        var result struct {
                Data []struct {
                        Embedding []float32 `json:"embedding"`
                } `json:"data"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data) == 0 {
                return hashEmbedding(text, 1536), nil
        }
        return result.Data[0].Embedding, nil
}

// Add stores an incident vector in the HNSW index.
func (v *VectorStore) Add(ctx context.Context, text string, meta *VectorMetadata) error {
        embedding, err := v.GenerateEmbedding(ctx, text)
        if err != nil {
                return err
        }

        v.mu.Lock()
        defer v.mu.Unlock()

        id := v.nextID
        v.nextID++
        v.index.Add(hnsw.MakeNode(id, embedding))
        v.metadata[id] = meta
        return nil
}

// Search returns the top-K most similar incidents plus their cosine similarity scores
// (range [0, 1], where 1 = identical). HNSW returns results sorted by ascending
// distance, so scores are already in descending order.
//
// v3.3.1: Returns REAL cosine similarities computed via hnsw.CosineDistance
// (1 - distance). Previously this method returned no scores, which forced the
// caller in main.go to synthesize fake scores — that broke the memory loop's
// 0.85 threshold (it fired on every incident after the first success).
func (v *VectorStore) Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error) {
        queryVec, err := v.GenerateEmbedding(ctx, text)
        if err != nil {
                return nil, nil, err
        }

        v.mu.RLock()
        defer v.mu.RUnlock()

        results := v.index.Search(queryVec, k)
        if len(results) == 0 {
                return nil, nil, nil
        }

        metas := make([]*VectorMetadata, 0, len(results))
        scores := make([]float32, 0, len(results))
        for _, node := range results {
                meta, ok := v.metadata[node.Key]
                if !ok {
                        continue
                }
                // Real cosine similarity = 1 - cosine distance.
                // Both queryVec and node.Value are L2-normalized (hashEmbedding normalizes;
                // text-embedding-3-small returns normalized vectors), so cosine similarity
                // is in [0, 1] for non-negative embeddings.
                sim := float32(1.0) - hnsw.CosineDistance(queryVec, node.Value)
                // Clamp to [0, 1] — small negative values can occur due to float rounding
                // when vectors are nearly orthogonal.
                if sim < 0 {
                        sim = 0
                }
                if sim > 1 {
                        sim = 1
                }
                metas = append(metas, meta)
                scores = append(scores, sim)
        }
        return metas, scores, nil
}

// LoadFromDB rebuilds the HNSW index from stored vectors in SQLite.
func (v *VectorStore) LoadFromDB(ctx context.Context, store *Store) error {
        rows, err := store.db.QueryContext(ctx,
                `SELECT incident_id, service, root_cause_category, fix_type, fix_pattern, outcome_success, embedding_json FROM incident_vectors`)
        if err != nil {
                return err
        }
        defer rows.Close()

        var loaded int
        for rows.Next() {
                var incidentID, service, rootCause, fixType, fixPattern, embeddingJSON string
                var outcomeSuccess int
                if err := rows.Scan(&incidentID, &service, &rootCause, &fixType, &fixPattern, &outcomeSuccess, &embeddingJSON); err != nil {
                        continue
                }

                var embedding []float32
                if err := json.Unmarshal([]byte(embeddingJSON), &embedding); err != nil {
                        continue
                }

                v.mu.Lock()
                id := v.nextID
                v.nextID++
                v.index.Add(hnsw.MakeNode(id, embedding))
                v.metadata[id] = &VectorMetadata{
                        IncidentID: incidentID,
                        Service:    service,
                        RootCause:  rootCause,
                        FixType:    fixType,
                        FixPattern: fixPattern,
                        Outcome:    map[bool]string{true: "success", false: "failure"}[outcomeSuccess == 1],
                }
                v.mu.Unlock()
                loaded++
        }

        if loaded > 0 {
                log.Printf("[vector] loaded %d vectors into HNSW index", loaded)
        }
        return nil
}

func hashEmbedding(text string, dim int) []float32 {
        vec := make([]float32, dim)
        words := splitWords(text)
        for _, w := range words {
                h := simpleHash32(w)
                vec[h%uint32(dim)] += 1.0
        }
        var sum float32
        for _, v := range vec {
                sum += v * v
        }
        if sum > 0 {
                norm := sum
                for i := 0; i < 20; i++ {
                        norm = (norm + sum/norm) * 0.5
                }
                for i := range vec {
                        vec[i] /= norm
                }
        }
        return vec
}

// simpleHash32 is defined in store.go (shared between both files)

func splitWords(s string) []string {
        var words []string
        current := ""
        for _, c := range s {
                if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
                        current += string(c)
                } else {
                        if current != "" {
                                words = append(words, current)
                                current = ""
                        }
                }
        }
        if current != "" {
                words = append(words, current)
        }
        return words
}

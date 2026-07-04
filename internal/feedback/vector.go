package feedback

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/hnsw"
	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

type VectorStore struct {
	index    *hnsw.Graph[uint64]
	mu       sync.RWMutex
	embedder emb.Embedder
	metadata map[uint64]*VectorMetadata
	nextID   uint64
}

type VectorMetadata struct {
	IncidentID string `json:"incident_id"`
	Service    string `json:"service"`
	RootCause  string `json:"root_cause"`
	FixType    string `json:"fix_type"`
	FixPattern string `json:"fix_pattern"`
	Outcome    string `json:"outcome"`
}

func NewVectorStore(embedder emb.Embedder) *VectorStore {
	return &VectorStore{
		index:    hnsw.NewGraph[uint64](),
		embedder: embedder,
		metadata: make(map[uint64]*VectorMetadata),
	}
}

func (v *VectorStore) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return v.embedder.Embed(ctx, text)
}

func (v *VectorStore) Add(ctx context.Context, text string, meta *VectorMetadata) error {
	embedding, err := v.embedder.Embed(ctx, text)
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

func (v *VectorStore) Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error) {
	queryVec, err := v.embedder.Embed(ctx, text)
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
		sim := float32(1.0) - hnsw.CosineDistance(queryVec, node.Value)
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

func (v *VectorStore) UsingHashFallback() bool {
	return !v.embedder.IsSemantic()
}

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
		logging.Info("loaded vectors into HNSW index", "loaded", loaded)
	}
	return nil
}

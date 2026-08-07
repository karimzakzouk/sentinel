package feedback

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/hnsw"
	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// defaultMaxVectors caps the in-memory HNSW graph + metadata map. Without
// a cap the index grows without bound as incidents accumulate; at scale
// (100k+ incidents) the resident memory becomes a problem. The SQLite
// table is the source of truth and is NOT evicted — only the in-memory
// search index. Callers that need to retrieve an evicted incident can
// fall back to a SQL LIKE query on summary / root_cause.
const defaultMaxVectors = 10000

// VectorStore overlays an in-memory HNSW graph over the incident_vectors
// SQLite table for fast cosine-similarity search. The graph is bounded by
// maxEntries with FIFO eviction; the SQLite table is the source of truth.
type VectorStore struct {
	index    *hnsw.Graph[uint64]
	mu       sync.RWMutex
	embedder emb.Embedder
	metadata map[uint64]*VectorMetadata

	// nextID is the monotonic HNSW node key. Never reused.
	nextID uint64

	// ids is the FIFO order of inserted node keys, used for eviction.
	ids        []uint64
	maxEntries int
}

// VectorMetadata is the per-vector payload returned by Search.
type VectorMetadata struct {
	IncidentID string `json:"incident_id"`
	Service    string `json:"service"`
	RootCause  string `json:"root_cause"`
	FixType    string `json:"fix_type"`
	FixPattern string `json:"fix_pattern"`
	Outcome    string `json:"outcome"`
}

// NewVectorStore returns a VectorStore with the default 10k-entry cap.
func NewVectorStore(embedder emb.Embedder) *VectorStore {
	return &VectorStore{
		index:      hnsw.NewGraph[uint64](),
		embedder:   embedder,
		metadata:   make(map[uint64]*VectorMetadata),
		maxEntries: defaultMaxVectors,
	}
}

// SetMaxEntries overrides the in-memory entry cap. Call before LoadFromDB
// / Add. A value <= 0 keeps the default.
func (v *VectorStore) SetMaxEntries(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if n > 0 {
		v.maxEntries = n
	}
}

// GenerateEmbedding is a convenience wrapper for callers that need to
// embed text outside of an Add/Search flow.
func (v *VectorStore) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return v.embedder.Embed(ctx, text)
}

// Add embeds text, inserts the resulting vector into the HNSW index, and
// attaches meta to the node. If the index is at capacity, the oldest
// inserted entry is evicted (FIFO).
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
	v.ids = append(v.ids, id)
	v.evictLocked()
	return nil
}

// Search returns the k nearest vectors to text (by cosine similarity) and
// their similarity scores in descending order.
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
			// Evicted entry — HNSW still returns the node but we have no
			// metadata to attach. Skip.
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

// UsingHashFallback reports whether the embedder is the non-semantic
// (TF-IDF) fallback. Used by callers to decide whether to trust
// similarity scores for auto-apply of known fixes.
func (v *VectorStore) UsingHashFallback() bool {
	return !v.embedder.IsSemantic()
}

// evictLocked removes the oldest entries until len(metadata) <= maxEntries.
// Caller must hold v.mu. The HNSW graph itself does not support per-node
// removal without a rebuild; the orphaned node remains in the graph but
// Search skips it via the metadata-map miss above.
func (v *VectorStore) evictLocked() {
	for len(v.metadata) > v.maxEntries && len(v.ids) > 0 {
		oldest := v.ids[0]
		v.ids = v.ids[1:]
		delete(v.metadata, oldest)
	}
}

// LoadFromDB hydrates the HNSW index from the incident_vectors table. The
// in-memory cap (maxEntries) still applies: if the table has more rows
// than maxEntries, only the most-recently-inserted maxEntries rows are
// loaded.
func (v *VectorStore) LoadFromDB(ctx context.Context, store *Store) error {
	rows, err := store.db.QueryContext(ctx,
		`SELECT incident_id, service, root_cause_category, fix_type, fix_pattern, outcome_success, embedding_json FROM incident_vectors ORDER BY created_at DESC`)
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
		// Stop once we're at capacity — the ORDER BY created_at DESC
		// means we've already loaded the most recent incidents.
		if len(v.metadata) >= v.maxEntries {
			v.mu.Unlock()
			break
		}
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
		v.ids = append(v.ids, id)
		v.mu.Unlock()
		loaded++
	}

	if loaded > 0 {
		logging.Info("feedback: loaded vectors into HNSW index", "loaded", loaded)
	}
	return nil
}

// Len returns the number of in-memory vectors. Exposed for tests / metrics.
func (v *VectorStore) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.metadata)
}

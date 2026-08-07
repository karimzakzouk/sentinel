package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/coder/hnsw"
	"github.com/karimzakzouk/sentinel/internal/emb"
)

// cacheEntry is the stored metadata for one cached LLM response.
type cacheEntry struct {
	response   string
	promptHash string
	createdAt  time.Time
}

// SemanticCache caches LLM responses by prompt similarity. It has two
// layers:
//
//   - exactCache: SHA256(prompt) -> cacheEntry. O(1) hit path.
//   - index: an HNSW graph over prompt embeddings. Slow path, used when
//     no exact match exists. Cosine similarity >= threshold => hit.
//
// Both paths check TTL: an entry is treated as a miss if
// time.Since(createdAt) >= ttl.
//
// The cache is bounded: when the number of entries exceeds maxEntries,
// the oldest entries are evicted. Default maxEntries is 1000.
type SemanticCache struct {
	enabled    bool
	threshold  float32
	ttl        time.Duration
	maxEntries int

	embedder emb.Embedder
	index    *hnsw.Graph[uint64]

	mu         sync.RWMutex
	metadata   map[uint64]*cacheEntry
	nextID     uint64
	exactCache map[string]*cacheEntry

	// ids ordered by insertion; used for FIFO eviction.
	ids []uint64
}

// NewSemanticCache returns a cache. If embedder is nil or non-semantic
// (e.g. tfidf), the cache is disabled (Enabled() returns false).
// threshold defaults to 0.97, ttl to 24h, maxEntries to 1000.
func NewSemanticCache(embedder emb.Embedder, threshold float32, ttl time.Duration, maxEntries int) *SemanticCache {
	if threshold <= 0 {
		threshold = 0.97
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &SemanticCache{
		enabled:    embedder != nil && embedder.IsSemantic(),
		threshold:  threshold,
		ttl:        ttl,
		maxEntries: maxEntries,
		embedder:   embedder,
		index:      hnsw.NewGraph[uint64](),
		metadata:   make(map[uint64]*cacheEntry),
		exactCache: make(map[string]*cacheEntry),
	}
}

// Enabled reports whether the cache is active.
func (c *SemanticCache) Enabled() bool {
	return c != nil && c.enabled
}

func hashPrompt(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(h[:])
}

// Get returns the cached response for prompt, or ("", false) on miss.
// The fast path checks the exact-match hash map; the slow path embeds the
// prompt and searches the HNSW index. Both paths check TTL.
func (c *SemanticCache) Get(ctx context.Context, prompt string) (string, bool) {
	if !c.Enabled() {
		return "", false
	}

	hash := hashPrompt(prompt)
	c.mu.RLock()
	if entry, ok := c.exactCache[hash]; ok {
		if time.Since(entry.createdAt) < c.ttl {
			c.mu.RUnlock()
			return entry.response, true
		}
	}
	c.mu.RUnlock()

	vec, err := c.embedder.Embed(ctx, prompt)
	if err != nil {
		return "", false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	results := c.index.Search(vec, 1)
	if len(results) == 0 {
		return "", false
	}
	entry, ok := c.metadata[results[0].Key]
	if !ok || time.Since(entry.createdAt) >= c.ttl {
		return "", false
	}
	sim := float32(1.0) - hnsw.CosineDistance(vec, results[0].Value)
	if sim < c.threshold {
		return "", false
	}
	return entry.response, true
}

// Put stores response for prompt. After Put, the cache may evict the
// oldest entries to stay under maxEntries.
func (c *SemanticCache) Put(ctx context.Context, prompt, response string) {
	if !c.Enabled() {
		return
	}

	hash := hashPrompt(prompt)
	entry := &cacheEntry{
		response:   response,
		promptHash: hash,
		createdAt:  time.Now(),
	}

	c.mu.Lock()
	c.exactCache[hash] = entry
	c.mu.Unlock()

	vec, err := c.embedder.Embed(ctx, prompt)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	c.index.Add(hnsw.MakeNode(id, vec))
	c.metadata[id] = entry
	c.ids = append(c.ids, id)
	c.evictLocked()
}

// evictLocked removes the oldest entries until len(metadata) <= maxEntries.
// Caller must hold c.mu.
func (c *SemanticCache) evictLocked() {
	for len(c.metadata) > c.maxEntries && len(c.ids) > 0 {
		oldest := c.ids[0]
		c.ids = c.ids[1:]
		if entry, ok := c.metadata[oldest]; ok {
			delete(c.exactCache, entry.promptHash)
		}
		delete(c.metadata, oldest)
		// HNSW graphs don't support per-node removal without rebuild; the
		// orphaned node in the index will simply return no metadata on
		// search (the metadata map lookup above guards this).
	}
}

// Stats returns (semantic entries, exact-match entries). For observability.
func (c *SemanticCache) Stats() (entries int, exactHits int) {
	if c == nil {
		return 0, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.metadata), len(c.exactCache)
}

// EvictExpired removes entries whose createdAt is older than ttl. Called
// periodically by the caller (e.g. main.go's housekeeping loop).
func (c *SemanticCache) EvictExpired() {
	if c == nil || !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	kept := c.ids[:0]
	for _, id := range c.ids {
		entry, ok := c.metadata[id]
		if !ok {
			continue
		}
		if now.Sub(entry.createdAt) >= c.ttl {
			delete(c.metadata, id)
			delete(c.exactCache, entry.promptHash)
			continue
		}
		kept = append(kept, id)
	}
	c.ids = kept
}

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/coder/hnsw"
	"github.com/karimzakzouk/sentinel/internal/emb"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

type cacheEntry struct {
	response  string
	promptHash string
	createdAt time.Time
}

type SemanticCache struct {
	enabled    bool
	threshold  float32
	ttl        time.Duration
	embedder   emb.Embedder
	index      *hnsw.Graph[uint64]
	mu         sync.RWMutex
	metadata   map[uint64]*cacheEntry
	nextID     uint64
	exactCache map[string]string // exact-match fast path (SHA256 → response)
}

func NewSemanticCache(embedder emb.Embedder, threshold float32, ttl time.Duration) *SemanticCache {
	if threshold <= 0 {
		threshold = 0.97
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &SemanticCache{
		enabled:    embedder != nil && embedder.IsSemantic(),
		threshold:  threshold,
		ttl:        ttl,
		embedder:   embedder,
		index:      hnsw.NewGraph[uint64](),
		metadata:   make(map[uint64]*cacheEntry),
		exactCache: make(map[string]string),
	}
}

func (c *SemanticCache) Enabled() bool {
	return c != nil && c.enabled
}

func hashPrompt(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(h[:])
}

func (c *SemanticCache) Get(ctx context.Context, prompt string) (string, bool) {
	if !c.Enabled() {
		return "", false
	}

	// Fast path: exact match via hash
	hash := hashPrompt(prompt)
	c.mu.RLock()
	if resp, ok := c.exactCache[hash]; ok {
		c.mu.RUnlock()
		logging.Info("LLM cache HIT (exact)", "hash", hash[:8])
		return resp, true
	}
	c.mu.RUnlock()

	// Slow path: semantic similarity search
	vec, err := c.embedder.Embed(ctx, prompt)
	if err != nil {
		return "", false
	}

	c.mu.RLock()
	results := c.index.Search(vec, 1)
	if len(results) > 0 {
		entry, ok := c.metadata[results[0].Key]
		if ok && time.Since(entry.createdAt) < c.ttl {
			sim := float32(1.0) - hnsw.CosineDistance(vec, results[0].Value)
			if sim >= c.threshold {
				c.mu.RUnlock()
				logging.Info("LLM cache HIT (semantic)", "similarity", sim)
				return entry.response, true
			}
		}
	}
	c.mu.RUnlock()
	return "", false
}

func (c *SemanticCache) Put(ctx context.Context, prompt, response string) {
	if !c.Enabled() {
		return
	}

	hash := hashPrompt(prompt)
	c.mu.Lock()
	c.exactCache[hash] = response
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
	c.metadata[id] = &cacheEntry{
		response:   response,
		promptHash: hash,
		createdAt:  time.Now(),
	}
}

func (c *SemanticCache) Stats() (entries int, exactHits int) {
	if c == nil {
		return 0, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.metadata), len(c.exactCache)
}

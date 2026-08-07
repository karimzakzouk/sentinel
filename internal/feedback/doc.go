// Package feedback is Sentinel's incident memory.
//
// One SQLite file holds four tables:
//
//   - incidents: the end-to-end incident record (cluster + agent state +
//     Beta prior + post-resolution PR metadata).
//   - events: the per-incident audit log (every agent step appends a row).
//   - beta_priors: per (service, fix_type) Beta(α, β) success counts,
//     updated by OutcomeWatcher after every fix verification.
//   - incident_vectors: embeddings for RAG retrieval of similar past
//     incidents.
//   - command_events: chatops audit log (every /sentinel command).
//
// The VectorStore overlays an in-memory HNSW graph over incident_vectors
// for fast cosine-similarity search. The graph is bounded by maxEntries
// (default 10k) with FIFO eviction; the SQLite table remains the source of
// truth and is not evicted.
//
// OutcomeWatcher runs a two-phase post-fix watch:
//
//   - Phase 1 (10 min, polls every 30s): did the service recover? Record
//     success or failure.
//   - Phase 2 (24h, polls every 5 min): did the original error fingerprint
//     reappear in pod logs? If so, override the Phase 1 success with a
//     failure.
//
// Phase 2 is the regression watcher: it corrects Beta priors that would
// otherwise be inflated by sandbox-only successes.
package feedback

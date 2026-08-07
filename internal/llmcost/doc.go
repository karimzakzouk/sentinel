// Package llmcost implements the cost-aware LLM router that sits in front
// of internal/llm.Router.
//
// internal/llm.Router already does multi-provider failover and serves
// from a semantic cache, but it sends every request to whichever model
// the caller picks (FastText vs FrontierText). That is expensive: most
// Sentinel LLM calls are short prompts ("is this error string a known
// pattern?", "classify this alert", "summarize this trace") that a
// fast/cheap model can answer.
//
// This package adds two cost-reduction layers:
//
//  1. Task-complexity classifier. Classify(prompt) inspects the prompt
//     length + keyword hints and returns ComplexityTrivial / Medium /
//     Hard. Complete() routes trivial + medium to the fast model and
//     hard to the frontier model. Classification itself is free (no LLM
//     call) — it is pure pattern matching.
//  2. Semantic cache. If a *llm.SemanticCache is wired, Complete()
//     checks it before calling the LLM. Near-identical prompts (cosine
//     similarity ≥ threshold) get the cached response. Target: ~70%
//     cost reduction per incident on workloads where the same error
//     patterns recur (which is most of them).
//
// The router does NOT replace internal/llm.Router — it wraps it. The
// failover pool, the per-provider circuit breakers, and the underlying
// semantic cache all keep working unchanged.
package llmcost

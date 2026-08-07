// Package automerge implements Sentinel's auto-merge loop: the only actor
// that merges a Sentinel-tracked PR.
//
// Auto-merge is opt-in and three layers deep:
//
//  1. Global config flag (config.AutoMergeEnabled).
//  2. Namespace allowlist (config.AutoMergeNamespaces).
//  3. Per-service annotation (sentinel.io/auto-merge: "true").
//
// Policy (policy.go) runs 11 gates before approving a merge: enabled,
// no-merge/hold labels, namespace allowlist, annotation, sandbox verified,
// confidence threshold, safety verdict, schema migration, rollback type,
// high-risk approval, CI green, rate limit, and cooldown.
//
// Merger (merger.go) is the 30-second poll loop that tracks pending PRs and
// merges any that pass all gates. EvaluateNow is the immediate-evaluation
// entry point used by the chatops webhook handler when a check_run event
// arrives for a tracked PR. Per-PR mutexes (sync.Map of *sync.Mutex keyed by
// "repo#pr") prevent the poll loop and EvaluateNow from racing on the same
// PR, which would cause duplicate merges + duplicate comments.
//
// Safety valves:
//
//   - Rate limiter: max N merges per hour (default 3). Prevents a cascade
//     of bad merges if Sentinel's confidence model is wrong.
//   - Cooldown: pause auto-merge for a configurable duration after any
//     regression is detected post-merge.
//   - Escape hatch: /sentinel no-merge (chatops).
package automerge

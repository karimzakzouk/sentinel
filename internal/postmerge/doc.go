// Package postmerge implements the post-merge regression watcher.
//
// Sentinel verifies a fix BEFORE merge (sandbox). But "passed sandbox" is not
// "didn't break prod". The autonomous loop is not closed until we either (a)
// confirm the deploy is healthy, or (b) auto-revert it.
//
// This package closes the loop. For every PR Sentinel auto-merges:
//
//  1. Phase 1 (10 min): poll the deploying service's error-rate, p99 latency,
//     and SLO burn rate. If any metric regresses past a threshold relative
//     to the pre-merge baseline, fire a RollbackEvent.
//  2. Phase 2 (24h): poll less frequently for slow-burn regressions
//     (memory leaks, log-volume growth, dependency-graph anomalies).
//  3. On RollbackEvent: revert the merge commit on the source repo,
//     open a "postmortem draft" issue, post a comment on the original PR,
//     and notify the OnRollback callback.
//
// "Fix it at 3am" is only an honest claim if you can also "un-fix it at 3:05am
// when you're wrong." This package is the un-fix half. No other AI-SRE tool
// does post-merge regression detection with auto-rollback — they all stop at
// "PR opened" or "PR merged" and assume the human will watch the dashboards.
//
// # Integration
//
// The automerge package calls Watch (or the automerge-compatible
// WatchPostMerge adapter) for every merged PR. The metrics source is the same
// Prometheus instance that observability.PrometheusQuerier wraps — the
// MetricsSource interface here is satisfied by that querier.
package postmerge

// Package detection implements Sentinel's autonomous trigger layer.
//
// Two live watchers run continuously:
//
//  1. Detector — streams logs from every pod carrying the
//     sentinel.io/repo annotation and watches pod status changes. Errors
//     matched by the shared errorparse patterns, plus pod-status transitions
//     (CrashLoopBackOff, OOMKilled, ImagePullBackOff), produce Signals.
//  2. Clusterer — collapses Signals for the same service+namespace within
//     a short window (default 500ms) into a single trigger so the
//     investigator is not launched N times for the same incident.
//
// The ActiveTracker suppresses re-triggers for a service that already has an
// in-flight incident, with a 30-minute post-resolution cooldown on
// successful fixes (no cooldown on failures — allow retry).
package detection

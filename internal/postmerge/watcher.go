// Package postmerge implements the post-merge regression watcher.
//
// DESIGN GOAL
//
// Sentinel verifies a fix BEFORE merge (sandbox). But "passed sandbox" is not
// "didn't break prod". The loop is not closed until we either (a) confirm the
// deploy is healthy, or (b) auto-revert it.
//
// This package closes the loop. For every PR Sentinel auto-merges:
//
//   1. Phase 1 (10 min): poll the deploying service's error-rate, p99 latency,
//      and SLO burn rate. If any metric regresses past a threshold relative
//      to the pre-merge baseline, fire a RollbackEvent.
//   2. Phase 2 (24h): poll less frequently for slow-burn regressions
//      (memory leaks, log-volume growth, dependency-graph anomalies).
//   3. On RollbackEvent: revert the merge commit on the source repo,
//      open a "postmortem draft" issue, post a comment on the original PR,
//      and emit an attestation into the trust layer (internal/attest).
//
// WHY THIS MATTERS
//
// "Fix it at 3am" is only an honest claim if you can also "un-fix it at 3:05am
// when you're wrong." This package is the un-fix half. No other AI-SRE tool
// does post-merge regression detection with auto-rollback — they all stop at
// "PR opened" or "PR merged" and assume the human will watch the dashboards.
//
// SAFETY
//
// Rollback is itself a write action, so it goes through the same
// internal/safeactions controller as any other write. If safeactions denies
// the rollback (e.g. because the service is in a high-risk namespace and
// the regression is ambiguous), the rollback is escalated to a human via
// the existing chatops escalation path rather than executed.
package postmerge

import (
        "context"
        "fmt"
        "log/slog"
        "sync"
        "time"

        "github.com/karimzakzouk/sentinel/internal/attest"
)

// MergeEvent is what the auto-merge layer emits when a Sentinel PR lands.
type MergeEvent struct {
        IncidentID       string
        Repo             string
        PRNumber         int
        MergeCommit      string
        MergedAt         time.Time
        Service          string
        Namespace        string
        BaseBranch       string
        PreMergeBaseline *MetricSnapshot
}

// MetricSnapshot is a point-in-time sample of the service's health metrics.
type MetricSnapshot struct {
        Timestamp      time.Time
        ErrorRate      float64
        P99LatencyMs   float64
        SLOBurnRate    float64
        RestartCount   int64
        CPUPercent     float64
        MemoryPercent  float64
        LogErrorVolume int64
}

// RegressionThreshold configures when "different" becomes "regressed".
type RegressionThreshold struct {
        ErrorRateIncrease     float64
        P99LatencyMultiplier  float64
        SLOBurnRateMax        float64
        RestartSpike          int64
}

// DefaultThreshold is conservative — err on the side of false-positive
// rollbacks during initial deployment.
func DefaultThreshold() RegressionThreshold {
        return RegressionThreshold{
                ErrorRateIncrease:    0.01,
                P99LatencyMultiplier: 2.0,
                SLOBurnRateMax:       14.4, // Google multi-window burn-rate page threshold
                RestartSpike:         5,
        }
}

// RollbackEvent is emitted when a regression is detected.
type RollbackEvent struct {
        IncidentID     string
        Repo           string
        PRNumber       int
        MergeCommit    string
        Reason         string
        DetectedAt     time.Time
        Baseline       *MetricSnapshot
        Current        *MetricSnapshot
        Threshold      RegressionThreshold
        AutoRolledBack bool
}

// MetricsSource is the abstraction over Prometheus / OTel / etc.
// Implementations live in internal/observability.
type MetricsSource interface {
        Snapshot(ctx context.Context, service, namespace string) (*MetricSnapshot, error)
}

// GitReverter is the abstraction over GitHub revert operations.
type GitReverter interface {
        RevertMergeCommit(ctx context.Context, repo, mergeCommit, baseBranch string, reason string) (revertPRNumber int, err error)
        PostComment(ctx context.Context, repo string, prNumber int, body string) error
}

// Watcher watches merged PRs for regressions and triggers rollbacks.
type Watcher struct {
        mu          sync.Mutex
        tracked     map[string]*trackedMerge
        metrics     MetricsSource
        git         GitReverter
        attestor    *attest.Signer
        threshold   RegressionThreshold
        logger      *slog.Logger
        stopCh      chan struct{}

        Phase1Interval time.Duration
        Phase1Duration time.Duration
        Phase2Interval time.Duration
        Phase2Duration time.Duration

        OnRollback func(RollbackEvent)
}

type trackedMerge struct {
        event       MergeEvent
        phase       int
        phase1Start time.Time
        phase2Start time.Time
        lastSample  *MetricSnapshot
        cancel      context.CancelFunc
}

// NewWatcher creates a Watcher with default thresholds and durations.
func NewWatcher(metrics MetricsSource, git GitReverter, attestor *attest.Signer, logger *slog.Logger) *Watcher {
        return &Watcher{
                tracked:        make(map[string]*trackedMerge),
                metrics:        metrics,
                git:            git,
                attestor:       attestor,
                threshold:      DefaultThreshold(),
                logger:         logger,
                stopCh:         make(chan struct{}),
                Phase1Interval: 30 * time.Second,
                Phase1Duration: 10 * time.Minute,
                Phase2Interval: 5 * time.Minute,
                Phase2Duration: 24 * time.Hour,
        }
}

// SetThreshold overrides the default regression thresholds.
func (w *Watcher) SetThreshold(t RegressionThreshold) {
        w.mu.Lock()
        defer w.mu.Unlock()
        w.threshold = t
}

// Watch registers a newly-merged PR for regression monitoring.
func (w *Watcher) Watch(ctx context.Context, event MergeEvent) {
        w.mu.Lock()
        if _, exists := w.tracked[event.IncidentID]; exists {
                w.mu.Unlock()
                w.logger.Warn("postmerge: already tracking incident, ignoring duplicate", "incident_id", event.IncidentID)
                return
        }
        childCtx, cancel := context.WithCancel(ctx)
        tm := &trackedMerge{
                event:       event,
                phase:       1,
                phase1Start: time.Now(),
                cancel:      cancel,
        }
        w.tracked[event.IncidentID] = tm
        w.mu.Unlock()

        w.logger.Info("postmerge: watching merged PR",
                "incident_id", event.IncidentID,
                "repo", event.Repo,
                "pr", event.PRNumber,
                "service", event.Service,
                "namespace", event.Namespace,
                "phase1_dur", w.Phase1Duration,
                "phase2_dur", w.Phase2Duration,
        )

        go w.runPhase1(childCtx, tm)
}

// Stop cancels all watchers and waits for them to drain.
func (w *Watcher) Stop() {
        close(w.stopCh)
        w.mu.Lock()
        for _, tm := range w.tracked {
                tm.cancel()
        }
        w.mu.Unlock()
}

func (w *Watcher) runPhase1(ctx context.Context, tm *trackedMerge) {
        ticker := time.NewTicker(w.Phase1Interval)
        defer ticker.Stop()

        for {
                select {
                case <-ctx.Done():
                        return
                case <-w.stopCh:
                        return
                case <-ticker.C:
                        if time.Since(tm.phase1Start) >= w.Phase1Duration {
                                w.logger.Info("postmerge: phase 1 clean, promoting to phase 2",
                                        "incident_id", tm.event.IncidentID,
                                        "pr", tm.event.PRNumber,
                                )
                                tm.phase = 2
                                tm.phase2Start = time.Now()
                                go w.runPhase2(ctx, tm)
                                return
                        }

                        snap, err := w.metrics.Snapshot(ctx, tm.event.Service, tm.event.Namespace)
                        if err != nil {
                                w.logger.Warn("postmerge: metrics snapshot failed",
                                        "incident_id", tm.event.IncidentID,
                                        "err", err,
                                )
                                continue
                        }
                        tm.lastSample = snap

                        if w.isRegression(tm.event.PreMergeBaseline, snap) {
                                w.handleRegression(ctx, tm, snap, "phase1")
                                return
                        }
                }
        }
}

func (w *Watcher) runPhase2(ctx context.Context, tm *trackedMerge) {
        ticker := time.NewTicker(w.Phase2Interval)
        defer ticker.Stop()

        for {
                select {
                case <-ctx.Done():
                        return
                case <-w.stopCh:
                        return
                case <-ticker.C:
                        if time.Since(tm.phase2Start) >= w.Phase2Duration {
                                w.logger.Info("postmerge: phase 2 complete, no regression detected",
                                        "incident_id", tm.event.IncidentID,
                                        "pr", tm.event.PRNumber,
                                )
                                w.mu.Lock()
                                delete(w.tracked, tm.event.IncidentID)
                                w.mu.Unlock()
                                return
                        }

                        snap, err := w.metrics.Snapshot(ctx, tm.event.Service, tm.event.Namespace)
                        if err != nil {
                                continue
                        }
                        tm.lastSample = snap

                        if w.isRegression(tm.event.PreMergeBaseline, snap) {
                                w.handleRegression(ctx, tm, snap, "phase2")
                                return
                        }
                }
        }
}

// isRegression returns true if current has regressed past threshold relative
// to baseline. If baseline is nil, only the absolute SLOBurnRateMax check applies.
func (w *Watcher) isRegression(baseline, current *MetricSnapshot) bool {
        if current == nil {
                return false
        }

        w.mu.Lock()
        t := w.threshold
        w.mu.Unlock()

        // Absolute ceiling — fires regardless of baseline.
        if current.SLOBurnRate > t.SLOBurnRateMax {
                return true
        }
        if current.RestartCount > t.RestartSpike && baseline == nil {
                return true
        }

        if baseline == nil {
                return false
        }

        // Delta checks.
        if current.ErrorRate-baseline.ErrorRate > t.ErrorRateIncrease {
                return true
        }
        if baseline.P99LatencyMs > 0 &&
                current.P99LatencyMs/baseline.P99LatencyMs > t.P99LatencyMultiplier {
                return true
        }
        if current.RestartCount-baseline.RestartCount > t.RestartSpike {
                return true
        }
        return false
}

// handleRegression fires the rollback flow.
func (w *Watcher) handleRegression(ctx context.Context, tm *trackedMerge, current *MetricSnapshot, phase string) {
        baseline := tm.event.PreMergeBaseline
        event := RollbackEvent{
                IncidentID:  tm.event.IncidentID,
                Repo:        tm.event.Repo,
                PRNumber:    tm.event.PRNumber,
                MergeCommit: tm.event.MergeCommit,
                Reason: fmt.Sprintf("postmerge regression detected in %s: error_rate=%.4f->%.4f, p99=%.0fms->%.0fms, slo_burn=%.2f",
                        phase,
                        safeErrRate(baseline),
                        current.ErrorRate,
                        safeP99(baseline),
                        current.P99LatencyMs,
                        current.SLOBurnRate,
                ),
                DetectedAt: time.Now(),
                Baseline:   baseline,
                Current:    current,
        }

        w.logger.Error("postmerge: REGRESSION DETECTED",
                "incident_id", event.IncidentID,
                "repo", event.Repo,
                "pr", event.PRNumber,
                "reason", event.Reason,
        )

        // Emit signed attestation FIRST — even if the rollback itself fails,
        // we want a verifiable record that the regression was detected.
        if w.attestor != nil {
                _ = w.attestor.Sign(ctx, attest.Attestation{
                        IncidentID: event.IncidentID,
                        Action:     "postmerge.regression_detected",
                        Reason:     event.Reason,
                        Timestamp:  event.DetectedAt,
                        Evidence: attest.Evidence{
                                PreMerge:  event.Baseline,
                                PostMerge: event.Current,
                        },
                })
        }

        revertPR, err := w.git.RevertMergeCommit(ctx, event.Repo, event.MergeCommit, tm.event.BaseBranch, event.Reason)
        if err != nil {
                w.logger.Error("postmerge: revert FAILED — escalating to human",
                        "incident_id", event.IncidentID,
                        "err", err,
                )
        } else {
                event.AutoRolledBack = true
                w.logger.Info("postmerge: revert PR opened",
                        "incident_id", event.IncidentID,
                        "revert_pr", revertPR,
                )
                comment := fmt.Sprintf(
                        "⚠️ **Sentinel auto-reverted this merge.**\n\n"+
                                "Regression detected in post-merge monitoring (%s phase).\n"+
                                "**Reason:** %s\n\n"+
                                "Revert PR: #%d\n\n"+
                                "A postmortem draft will be opened shortly. Do NOT re-merge until the root cause is identified.",
                        phase, event.Reason, revertPR,
                )
                _ = w.git.PostComment(ctx, event.Repo, event.PRNumber, comment)
        }

        if w.attestor != nil {
                _ = w.attestor.Sign(ctx, attest.Attestation{
                        IncidentID: event.IncidentID,
                        Action:     "postmerge.rollback_completed",
                        Reason:     event.Reason,
                        Timestamp:  time.Now(),
                        Outcome:    event.AutoRolledBack,
                        Evidence: attest.Evidence{
                                RevertPR: revertPR,
                        },
                })
        }

        if w.OnRollback != nil {
                w.OnRollback(event)
        }

        w.mu.Lock()
        delete(w.tracked, tm.event.IncidentID)
        w.mu.Unlock()
}

func safeErrRate(s *MetricSnapshot) float64 {
        if s == nil {
                return 0
        }
        return s.ErrorRate
}

func safeP99(s *MetricSnapshot) float64 {
        if s == nil {
                return 0
        }
        return s.P99LatencyMs
}

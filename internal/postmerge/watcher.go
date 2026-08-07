package postmerge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
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

// MetricSnapshot is a point-in-time sample of a service's health metrics.
// The same struct is consumed by observability.PrometheusQuerier.Snapshot —
// the MetricsSource interface owns this type so the dependency direction is
// postmerge → (interface) ← observability, with no import cycle.
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
	ErrorRateIncrease    float64
	P99LatencyMultiplier float64
	SLOBurnRateMax       float64
	RestartSpike         int64
}

// DefaultThreshold is conservative — err on the side of false-positive
// rollbacks during initial deployment.
func DefaultThreshold() RegressionThreshold {
	return RegressionThreshold{
		ErrorRateIncrease:    0.01,
		P99LatencyMultiplier: 2.0,
		SLOBurnRateMax:       14.4, // Google multi-window burn-rate page threshold.
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

// MetricsSource is the abstraction over Prometheus / OTel / etc. The real
// implementation lives in internal/observability; this interface is owned by
// the consumer (postmerge) so observability can depend on postmerge without
// an import cycle.
type MetricsSource interface {
	Snapshot(ctx context.Context, service, namespace string) (*MetricSnapshot, error)
}

// GitReverter is the abstraction over GitHub revert operations. The
// github.BaseClient satisfies this interface.
type GitReverter interface {
	RevertMergeCommit(ctx context.Context, repo, mergeCommit, baseBranch, reason string) (revertPRNumber int, err error)
	PostComment(ctx context.Context, repo string, prNumber int, body string) error
}

// Watcher watches merged PRs for regressions and triggers rollbacks. It is
// safe for concurrent use.
type Watcher struct {
	mu        sync.Mutex
	tracked   map[string]*trackedMerge
	metrics   MetricsSource
	git       GitReverter
	threshold RegressionThreshold
	stopCh    chan struct{}

	Phase1Interval time.Duration
	Phase1Duration time.Duration
	Phase2Interval time.Duration
	Phase2Duration time.Duration

	// OnRollback is invoked synchronously after the rollback flow completes
	// (whether or not the revert PR succeeded). Wire this to record the event
	// in the audit log or escalate to a human.
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
func NewWatcher(metrics MetricsSource, git GitReverter) *Watcher {
	return &Watcher{
		tracked:        make(map[string]*trackedMerge),
		metrics:        metrics,
		git:            git,
		threshold:      DefaultThreshold(),
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

// Watch registers a newly-merged PR for regression monitoring. Duplicate
// IncidentIDs are silently ignored.
func (w *Watcher) Watch(ctx context.Context, event MergeEvent) {
	w.mu.Lock()
	if _, exists := w.tracked[event.IncidentID]; exists {
		w.mu.Unlock()
		logging.Warn("postmerge: already tracking incident, ignoring duplicate",
			"incident_id", event.IncidentID)
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

	logging.Info("postmerge: watching merged PR",
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

// WatchPostMerge is the automerge.OutcomeWatcher-compatible adapter. It
// constructs a partial MergeEvent — sufficient for metrics monitoring but
// missing the Git-revert fields (Repo, PRNumber, MergeCommit, BaseBranch).
// Production deployments should prefer Watch with a full MergeEvent so the
// watcher can open a revert PR when a regression is detected.
func (w *Watcher) WatchPostMerge(ctx context.Context, incidentID, service, namespace string) {
	w.Watch(ctx, MergeEvent{
		IncidentID: incidentID,
		Service:    service,
		Namespace:  namespace,
		MergedAt:   time.Now(),
	})
}

// Stop cancels all watchers and waits for them to drain. Safe to call once.
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
				logging.Info("postmerge: phase 1 clean, promoting to phase 2",
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
				logging.Warn("postmerge: metrics snapshot failed",
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
				logging.Info("postmerge: phase 2 complete, no regression detected",
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

// IsRegression is exported for test instrumentation and operators who want
// to validate a snapshot against the configured threshold without running
// the watcher loop. It returns true if current has regressed past threshold
// relative to baseline. If baseline is nil, only the absolute SLOBurnRateMax
// check applies.
func (w *Watcher) IsRegression(baseline, current *MetricSnapshot) bool {
	return w.isRegression(baseline, current)
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

	logging.Error("postmerge: REGRESSION DETECTED",
		"incident_id", event.IncidentID,
		"repo", event.Repo,
		"pr", event.PRNumber,
		"reason", event.Reason,
	)

	if w.git == nil || tm.event.MergeCommit == "" {
		// No reverter wired or no merge commit recorded — record the event
		// and let OnRollback escalate to a human.
		logging.Warn("postmerge: no reverter available, escalating",
			"incident_id", event.IncidentID,
		)
	} else {
		revertPR, err := w.git.RevertMergeCommit(ctx, event.Repo, event.MergeCommit, tm.event.BaseBranch, event.Reason)
		if err != nil {
			logging.Error("postmerge: revert FAILED — escalating to human",
				"incident_id", event.IncidentID,
				"err", err,
			)
		} else {
			event.AutoRolledBack = true
			logging.Info("postmerge: revert PR opened",
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

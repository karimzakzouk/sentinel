package postmerge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeMetricsSource returns a healthy snapshot for the first `regressAfter`
// calls, then returns the regression snapshot forever. This makes phase
// transitions deterministic — the test asserts that the watcher consumed
// exactly `regressAfter` healthy samples before flipping to the regression.
type fakeMetricsSource struct {
	mu           sync.Mutex
	healthy      *MetricSnapshot
	regression   *MetricSnapshot
	regressAfter int
	err          error
	calls        int
}

func (f *fakeMetricsSource) Snapshot(_ context.Context, _, _ string) (*MetricSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.calls > f.regressAfter {
		return f.regression, nil
	}
	return f.healthy, nil
}

func (f *fakeMetricsSource) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeGitReverter records revert + comment calls.
type fakeGitReverter struct {
	mu          sync.Mutex
	reverts     []revertCall
	comments    []commentCall
	revertErr   error
	revertPRNum int
}

type revertCall struct {
	Repo, MergeCommit, BaseBranch, Reason string
}

type commentCall struct {
	Repo  string
	PRNum int
	Body  string
}

func (f *fakeGitReverter) RevertMergeCommit(_ context.Context, repo, mergeCommit, baseBranch, reason string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reverts = append(f.reverts, revertCall{repo, mergeCommit, baseBranch, reason})
	if f.revertErr != nil {
		return 0, f.revertErr
	}
	if f.revertPRNum == 0 {
		f.revertPRNum = 9001
	}
	return f.revertPRNum, nil
}

func (f *fakeGitReverter) PostComment(_ context.Context, repo string, prNumber int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, commentCall{repo, prNumber, body})
	return nil
}

func (f *fakeGitReverter) revertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reverts)
}

func (f *fakeGitReverter) commentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.comments)
}

// newTestWatcher returns a Watcher with millisecond-scale durations so the
// tests run in tens of milliseconds instead of minutes.
func newTestWatcher(metrics MetricsSource, git GitReverter) *Watcher {
	w := NewWatcher(metrics, git)
	w.Phase1Interval = 5 * time.Millisecond
	w.Phase1Duration = 20 * time.Millisecond
	w.Phase2Interval = 5 * time.Millisecond
	w.Phase2Duration = 200 * time.Millisecond
	return w
}

// startWatch is a test-only helper that calls Watch with a fixed merge event.
func (w *Watcher) startWatch(ctx context.Context) {
	w.Watch(ctx, MergeEvent{
		IncidentID:  "inc-1",
		Repo:        "acme/api",
		PRNumber:    42,
		MergeCommit: "deadbeefdeadbeef",
		BaseBranch:  "main",
		Service:     "api",
		Namespace:   "prod",
		MergedAt:    time.Now(),
	})
}

// waitForRevert polls up to 2s for the git reverter to record a revert.
func waitForRevert(t *testing.T, git *fakeGitReverter, metrics *fakeMetricsSource) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if git.revertCount() > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("revert not called within timeout; metrics calls=%d", metrics.callsCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestPostMerge_Phase1RegressionTriggersRevert verifies that a snapshot
// exceeding the SLO burn-rate threshold during phase 1 triggers a revert +
// comment on the original PR.
func TestPostMerge_Phase1RegressionTriggersRevert(t *testing.T) {
	metrics := &fakeMetricsSource{
		healthy: &MetricSnapshot{
			Timestamp: time.Now(), ErrorRate: 0.001, P99LatencyMs: 50, SLOBurnRate: 0.5,
		},
		regression: &MetricSnapshot{
			Timestamp: time.Now(), ErrorRate: 0.10, P99LatencyMs: 50, SLOBurnRate: 20.0,
		},
		regressAfter: 1, // first snapshot healthy, second snapshot regresses.
	}
	git := &fakeGitReverter{}
	w := newTestWatcher(metrics, git)

	var event RollbackEvent
	w.OnRollback = func(e RollbackEvent) { event = e }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.startWatch(ctx)

	waitForRevert(t, git, metrics)
	cancel()
	w.Stop()

	if got := git.revertCount(); got != 1 {
		t.Errorf("expected 1 revert, got %d", got)
	}
	if got := git.commentCount(); got != 1 {
		t.Errorf("expected 1 comment on original PR, got %d", got)
	}
	if !event.AutoRolledBack {
		t.Error("expected AutoRolledBack=true on rollback event")
	}
	if event.Repo != "acme/api" || event.PRNumber != 42 {
		t.Errorf("event has wrong repo/pr: %+v", event)
	}
}

// TestPostMerge_Phase2Monitoring verifies that a clean phase 1 promotes the
// merge to phase 2, and that a regression during phase 2 still triggers a
// revert. The regression is delayed past the phase 1 window so it must be
// caught by phase 2.
func TestPostMerge_Phase2Monitoring(t *testing.T) {
	metrics := &fakeMetricsSource{
		healthy: &MetricSnapshot{
			Timestamp: time.Now(), ErrorRate: 0.001, P99LatencyMs: 50, SLOBurnRate: 0.5,
		},
		regression: &MetricSnapshot{
			Timestamp: time.Now(), ErrorRate: 0.10, P99LatencyMs: 50, SLOBurnRate: 20.0,
		},
		regressAfter: 5, // phase 1 consumes 3 healthy; phase 2 consumes 2 healthy then 1 regression.
	}
	git := &fakeGitReverter{}
	w := newTestWatcher(metrics, git)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.startWatch(ctx)

	waitForRevert(t, git, metrics)
	cancel()
	w.Stop()

	if got := git.revertCount(); got != 1 {
		t.Errorf("expected 1 revert from phase 2, got %d", got)
	}
	// The regression should only fire after at least 5 healthy samples —
	// i.e. phase 1 (3 samples) + at least 2 phase 2 samples.
	if got := metrics.callsCount(); got < 5 {
		t.Errorf("regression fired too early: only %d metrics calls", got)
	}
}

// TestPostMerge_NoRegressionNoRevert verifies that a merge with clean
// metrics throughout phase 1 AND phase 2 does NOT trigger a revert.
func TestPostMerge_NoRegressionNoRevert(t *testing.T) {
	metrics := &fakeMetricsSource{
		healthy: &MetricSnapshot{
			Timestamp: time.Now(), ErrorRate: 0.001, P99LatencyMs: 50, SLOBurnRate: 0.5,
		},
		regressAfter: 1 << 30, // effectively never regress.
	}
	git := &fakeGitReverter{}
	w := newTestWatcher(metrics, git)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.startWatch(ctx)

	// Wait long enough for both phases to complete and the merge to be
	// evicted from the tracked map. Phase1Duration=20ms + Phase2Duration=200ms
	// = 220ms; sleep 300ms to be safe.
	time.Sleep(300 * time.Millisecond)
	cancel()
	w.Stop()

	if got := git.revertCount(); got != 0 {
		t.Errorf("expected 0 reverts for clean deploy, got %d", got)
	}
}

// TestPostMerge_DuplicateWatchIsIgnored verifies that calling Watch twice
// with the same IncidentID does not start a second tracker.
func TestPostMerge_DuplicateWatchIsIgnored(t *testing.T) {
	metrics := &fakeMetricsSource{err: errors.New("boom"), regressAfter: 1 << 30}
	git := &fakeGitReverter{}
	w := newTestWatcher(metrics, git)

	event := MergeEvent{
		IncidentID: "inc-1",
		Repo:       "acme/api",
		PRNumber:   42,
		Service:    "api",
		Namespace:  "prod",
	}
	w.Watch(context.Background(), event)
	w.Watch(context.Background(), event)

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tracked) != 1 {
		t.Errorf("expected 1 tracked merge, got %d", len(w.tracked))
	}
}

// TestPostMerge_IsRegression verifies the threshold logic without running
// the watcher loop.
func TestPostMerge_IsRegression(t *testing.T) {
	w := NewWatcher(&fakeMetricsSource{}, &fakeGitReverter{})
	w.SetThreshold(RegressionThreshold{
		ErrorRateIncrease:    0.01,
		P99LatencyMultiplier: 2.0,
		SLOBurnRateMax:       14.4,
		RestartSpike:         5,
	})

	baseline := &MetricSnapshot{ErrorRate: 0.001, P99LatencyMs: 50, RestartCount: 1}

	tests := []struct {
		name     string
		baseline *MetricSnapshot
		current  *MetricSnapshot
		want     bool
	}{
		{"nil current", baseline, nil, false},
		{"absolute slo burn", baseline, &MetricSnapshot{SLOBurnRate: 20}, true},
		{"restart spike without baseline", nil, &MetricSnapshot{RestartCount: 100}, true},
		{"error rate delta", baseline, &MetricSnapshot{ErrorRate: 0.05}, true},
		{"p99 latency multiplier", baseline, &MetricSnapshot{P99LatencyMs: 200}, true},
		{"restart delta", baseline, &MetricSnapshot{RestartCount: 10}, true},
		{"healthy snapshot", baseline, &MetricSnapshot{ErrorRate: 0.001, P99LatencyMs: 50, RestartCount: 1, SLOBurnRate: 0.5}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := w.IsRegression(tc.baseline, tc.current); got != tc.want {
				t.Errorf("IsRegression = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPostMerge_WatchPostMergeAdapter verifies the automerge-compatible
// adapter constructs a partial MergeEvent and starts the watcher.
func TestPostMerge_WatchPostMergeAdapter(t *testing.T) {
	metrics := &fakeMetricsSource{err: errors.New("stop"), regressAfter: 1 << 30}
	git := &fakeGitReverter{}
	w := newTestWatcher(metrics, git)

	w.WatchPostMerge(context.Background(), "inc-99", "billing-api", "prod")

	w.mu.Lock()
	defer w.mu.Unlock()
	tm, ok := w.tracked["inc-99"]
	if !ok {
		t.Fatal("expected tracked merge for inc-99")
	}
	if tm.event.Service != "billing-api" || tm.event.Namespace != "prod" {
		t.Errorf("wrong service/namespace: %+v", tm.event)
	}
	// Repo / PRNumber / MergeCommit are NOT populated by the adapter —
	// production wiring should use Watch with a full MergeEvent when a
	// revert is desired.
	if tm.event.Repo != "" || tm.event.MergeCommit != "" {
		t.Errorf("adapter should not populate revert fields: %+v", tm.event)
	}
}

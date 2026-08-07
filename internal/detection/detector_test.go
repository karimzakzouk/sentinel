package detection

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestShouldTriggerCooldown verifies the cooldown logic — the same error
// type for the same service+namespace should not re-trigger within the
// cooldown window.
func TestShouldTriggerCooldown(t *testing.T) {
	d := New(nil, nil, "")
	d.cooldown = 100 * time.Millisecond

	if !d.shouldTrigger("sim", "api", "KeyError") {
		t.Error("first trigger should succeed")
	}
	if d.shouldTrigger("sim", "api", "KeyError") {
		t.Error("second trigger within cooldown should be blocked")
	}
	time.Sleep(150 * time.Millisecond)
	if !d.shouldTrigger("sim", "api", "KeyError") {
		t.Error("trigger after cooldown should succeed")
	}
}

// TestShouldTriggerDifferentServices verifies that different services are
// tracked independently.
func TestShouldTriggerDifferentServices(t *testing.T) {
	d := New(nil, nil, "")
	if !d.shouldTrigger("sim", "api", "KeyError") {
		t.Error("trigger for api should succeed")
	}
	if !d.shouldTrigger("sim", "worker", "KeyError") {
		t.Error("trigger for worker should succeed (different service)")
	}
}

// TestShouldTriggerDifferentNamespaces verifies that different namespaces
// are tracked independently.
func TestShouldTriggerDifferentNamespaces(t *testing.T) {
	d := New(nil, nil, "")
	if !d.shouldTrigger("sim", "api", "KeyError") {
		t.Error("trigger for sim/api should succeed")
	}
	if !d.shouldTrigger("prod", "api", "KeyError") {
		t.Error("trigger for prod/api should succeed (different namespace)")
	}
}

// TestExtractErrorType verifies error-type extraction from log lines.
func TestExtractErrorType(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"KeyError: 'database_url'", "KeyError"},
		{"ValueError: invalid input", "ValueError"},
		{"TypeError: unsupported operand", "TypeError"},
		{"panic: runtime error: nil pointer dereference", "panic"},
		{"  File \"app.py\", line 42", "unknown_error"},
		{"", "unknown_error"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := dummyDetector.extractErrorType(tt.line)
			if got != tt.want {
				t.Errorf("extractErrorType(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// dummyDetector is a Detector used by the table-driven extractErrorType
// test above — extractErrorType only reads the pre-compiled patterns, so a
// nil clientset is fine.
var dummyDetector = New(nil, nil, "")

// TestIsErrorLine verifies the fast-path error-line scan.
func TestIsErrorLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"Traceback (most recent call last):", true},
		{"KeyError: 'DATABASE_URL'", true},
		{"panic: runtime error", true},
		{"java.lang.NullPointerException", true},
		{"INFO: application started", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			if got := dummyDetector.isErrorLine(c.line); got != c.want {
				t.Errorf("isErrorLine(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

// TestNewWithConfigDefaults verifies that zero values keep the defaults.
func TestNewWithConfigDefaults(t *testing.T) {
	det := NewWithConfig(nil, nil, "", 0, 0, nil)
	if det.cooldown != 5*time.Minute {
		t.Errorf("default cooldown = %v, want 5m", det.cooldown)
	}
	if det.tailLines != 100 {
		t.Errorf("default tailLines = %d, want 100", det.tailLines)
	}
	if !det.skipNamespaces["kube-system"] {
		t.Error("default skip list should include kube-system")
	}
}

// TestNewWithConfigOverrides verifies that explicit overrides take effect.
func TestNewWithConfigOverrides(t *testing.T) {
	det := NewWithConfig(nil, nil, "", time.Minute, 50, []string{"custom-ns"})
	if det.cooldown != time.Minute {
		t.Errorf("cooldown = %v, want 1m", det.cooldown)
	}
	if det.tailLines != 50 {
		t.Errorf("tailLines = %d, want 50", det.tailLines)
	}
	if !det.skipNamespaces["custom-ns"] || det.skipNamespaces["kube-system"] {
		t.Errorf("skipNamespaces = %v, want only custom-ns", det.skipNamespaces)
	}
}

// TestGetServiceFromPod verifies the label-fallback chain.
func TestGetServiceFromPod(t *testing.T) {
	det := New(nil, nil, "")
	cases := []struct {
		name    string
		labels  map[string]string
		podName string
		want    string
	}{
		{"app.kubernetes.io/name wins", map[string]string{"app.kubernetes.io/name": "api", "app": "old"}, "api-abc", "api"},
		{"app label fallback", map[string]string{"app": "worker"}, "worker-xyz", "worker"},
		{"pod name fallback (strip replica)", map[string]string{}, "scheduler-1234", "scheduler"},
		{"pod name no dash", map[string]string{}, "standalone", "standalone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: c.podName, Labels: c.labels},
			}
			if got := det.getServiceFromPod(pod); got != c.want {
				t.Errorf("getServiceFromPod = %q, want %q", got, c.want)
			}
		})
	}
}

// TestClusterer_EmitDedupesWithinWindow verifies that multiple signals for
// the same service+namespace within the grouping window collapse to a
// single handler invocation. The handler is invoked once with the most
// descriptive reason.
func TestClusterer_EmitDedupesWithinWindow(t *testing.T) {
	var calls int32
	var lastReason string
	var mu sync.Mutex
	handler := func(svc, ns, reason, logs string) {
		atomic.AddInt32(&calls, 1)
		mu.Lock()
		lastReason = reason
		mu.Unlock()
	}

	tracker := NewActiveTracker()
	c := NewClusterer(handler, tracker)
	c.SetWindow(50 * time.Millisecond)

	// Fire 5 signals within the window — only one handler call expected.
	// The last signal has the longest Reason; the merge logic keeps the
	// most descriptive reason.
	for i := 0; i < 5; i++ {
		c.Send(context.Background(), Signal{
			Type:      SignalLogError,
			Service:   "api",
			Namespace: "prod",
			Reason:    "KeyError",
		})
	}

	// Wait past the window + a slack for the timer goroutine.
	time.Sleep(200 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler called %d times, want 1 (signals should dedupe)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastReason != "KeyError" {
		t.Errorf("lastReason = %q, want KeyError", lastReason)
	}
}

// TestClusterer_DedupesAcrossWindows verifies that signals arriving after
// the window closes produce a second trigger. Uses a nil tracker so the
// second emit is not suppressed by the active-incident gate (which is
// exercised separately in TestClusterer_ActiveTrackerSuppressesEmit).
func TestClusterer_DedupesAcrossWindows(t *testing.T) {
	var calls int32
	handler := func(svc, ns, reason, logs string) {
		atomic.AddInt32(&calls, 1)
	}
	c := NewClusterer(handler, nil)
	c.SetWindow(50 * time.Millisecond)

	c.Send(context.Background(), Signal{Type: SignalLogError, Service: "api", Namespace: "prod", Reason: "err1"})
	time.Sleep(150 * time.Millisecond)
	c.Send(context.Background(), Signal{Type: SignalLogError, Service: "api", Namespace: "prod", Reason: "err2"})
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("handler called %d times, want 2 (one per window)", got)
	}
}

// TestClusterer_PauseCheckerNoRace is the v1 regression test for the v5
// data race at clusterer.go:157. SetPauseChecker writes c.pauseChecker
// under c.mu; emit() reads it under c.mu (copied to a local before being
// invoked). Run under -race to verify no race is detected.
func TestClusterer_PauseCheckerNoRace(t *testing.T) {
	tracker := NewActiveTracker()
	c := NewClusterer(func(svc, ns, reason, logs string) {}, tracker)
	c.SetWindow(5 * time.Millisecond)

	pc := &atomicPauseChecker{}
	c.SetPauseChecker(pc)

	// Hammer SetPauseChecker / Send / emit from multiple goroutines for
	// 100ms. emit() is reached via the time.AfterFunc callback inside
	// Send, so spamming Send at high frequency exercises the race.
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: flip the pause checker between implementations.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.SetPauseChecker(&atomicPauseChecker{})
				c.SetPauseChecker(pc)
			}
		}
	}()

	// Sender: pump signals so emit() runs frequently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			c.Send(context.Background(), Signal{
				Type:      SignalLogError,
				Service:   "svc",
				Namespace: "ns",
				Reason:    "err",
			})
		}
	}()

	// Let the goroutines run for 100ms, then stop and wait.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	c.Stop()
}

// TestClusterer_PausedSuppressesEmit verifies that when the pause checker
// returns true, the handler is NOT invoked.
func TestClusterer_PausedSuppressesEmit(t *testing.T) {
	var calls int32
	handler := func(svc, ns, reason, logs string) {
		atomic.AddInt32(&calls, 1)
	}
	tracker := NewActiveTracker()
	c := NewClusterer(handler, tracker)
	c.SetWindow(20 * time.Millisecond)
	c.SetPauseChecker(alwaysPaused{})

	c.Send(context.Background(), Signal{Type: SignalLogError, Service: "api", Namespace: "prod", Reason: "err"})
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("paused clusterer should not emit, got %d calls", got)
	}
}

// TestClusterer_ActiveTrackerSuppressesEmit verifies that when the
// ActiveTracker reports an in-flight incident, the handler is NOT invoked.
func TestClusterer_ActiveTrackerSuppressesEmit(t *testing.T) {
	var calls int32
	handler := func(svc, ns, reason, logs string) {
		atomic.AddInt32(&calls, 1)
	}
	tracker := NewActiveTracker()
	tracker.MarkActive("api", "prod")

	c := NewClusterer(handler, tracker)
	c.SetWindow(20 * time.Millisecond)
	c.Send(context.Background(), Signal{Type: SignalLogError, Service: "api", Namespace: "prod", Reason: "err"})
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("active incident should suppress emit, got %d calls", got)
	}
}

// TestClusterer_Stop verifies Stop cancels pending timers so no handler
// fires after shutdown.
func TestClusterer_Stop(t *testing.T) {
	var calls int32
	handler := func(svc, ns, reason, logs string) {
		atomic.AddInt32(&calls, 1)
	}
	tracker := NewActiveTracker()
	c := NewClusterer(handler, tracker)
	c.SetWindow(50 * time.Millisecond)

	c.Send(context.Background(), Signal{Type: SignalLogError, Service: "api", Namespace: "prod", Reason: "err"})
	c.Stop()
	time.Sleep(200 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("Stop should cancel pending emit, got %d calls", got)
	}
}

// TestActiveTracker verifies the resolved-success cooldown path.
func TestActiveTracker(t *testing.T) {
	tr := NewActiveTracker()
	tr.SetCooldown(50 * time.Millisecond)

	if tr.IsActive("api", "prod") {
		t.Error("should not be active before MarkActive")
	}
	tr.MarkActive("api", "prod")
	if !tr.IsActive("api", "prod") {
		t.Error("should be active after MarkActive")
	}

	// Resolve as success — should remain "active" for the cooldown.
	tr.MarkResolved("api", "prod", true)
	if !tr.IsActive("api", "prod") {
		t.Error("should be active during success cooldown")
	}
	time.Sleep(80 * time.Millisecond)
	if tr.IsActive("api", "prod") {
		t.Error("should clear after success cooldown")
	}

	// Failure path: clears immediately.
	tr.MarkActive("api", "prod")
	tr.MarkResolved("api", "prod", false)
	if tr.IsActive("api", "prod") {
		t.Error("failed incident should clear immediately")
	}
}

// alwaysPaused is a PauseChecker that always reports paused.
type alwaysPaused struct{}

func (alwaysPaused) IsPaused() bool { return true }

// atomicPauseChecker is a PauseChecker whose IsPaused reads an atomic flag
// (so it is goroutine-safe under the race detector).
type atomicPauseChecker struct {
	flag int32
}

func (a *atomicPauseChecker) IsPaused() bool {
	return atomic.LoadInt32(&a.flag) == 1
}

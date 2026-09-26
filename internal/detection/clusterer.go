package detection

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// SignalType is the kind of detector that produced a Signal.
type SignalType string

const (
	SignalLogError  SignalType = "log_error"
	SignalPodStatus SignalType = "pod_status"
	SignalK8sEvent  SignalType = "k8s_event"
)

// Signal is the alarm bell sent to the clusterer. The clusterer does not
// fetch logs — that's the investigator's job; the Signal carries the
// dedup-relevant fields plus whatever crash logs the detector already
// captured (--previous container output at trigger time).
type Signal struct {
	Type      SignalType
	Service   string
	Namespace string
	Reason    string
	Repo      string
	// PodLogs is the detection-time crash evidence (previous container).
	// Often the only copy of the crash signature — the investigator merges
	// it with a fresh fetch.
	PodLogs   string
	Timestamp time.Time
}

// IncidentHandler is invoked when a cluster fires. The podLogs argument
// carries the detection-time crash logs (may be empty when the detector had
// none — e.g. ImagePullBackOff).
type IncidentHandler func(service, namespace, reason, podLogs string)

// PauseChecker reports whether autonomous mode is paused (e.g. via a Slack
// bot command). When paused, the clusterer silently drops signals instead
// of buffering them.
type PauseChecker interface {
	IsPaused() bool
}

// Clusterer groups signals by service+namespace within a window and emits
// one trigger per service per window. It does not fetch logs.
type Clusterer struct {
	handler IncidentHandler
	tracker *ActiveTracker

	mu      sync.Mutex
	pending map[string]*pendingCluster
	window  time.Duration

	// pauseChecker is read in emit() and written by SetPauseChecker. Both
	// paths take c.mu so the interface assignment is serialized — Go
	// interface writes are NOT atomic, so reading without the lock is a
	// data race.
	pauseChecker PauseChecker
}

// pendingCluster is the in-flight cluster for one service+namespace key.
type pendingCluster struct {
	signals   []Signal
	firstSeen time.Time
	timer     *time.Timer
}

// NewClusterer returns a Clusterer with the default 500ms grouping window.
func NewClusterer(handler IncidentHandler, tracker *ActiveTracker) *Clusterer {
	return &Clusterer{
		handler: handler,
		tracker: tracker,
		pending: make(map[string]*pendingCluster),
		window:  500 * time.Millisecond,
	}
}

// SetWindow overrides the grouping window. Useful for tests that need a
// shorter window than the default 500ms.
func (c *Clusterer) SetWindow(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.window = d
	}
}

// SetPauseChecker wires in a pause checker. After this call, any cluster
// about to fire first checks IsPaused; if true, the trigger is suppressed
// (signals are not buffered for later).
func (c *Clusterer) SetPauseChecker(pc PauseChecker) {
	c.mu.Lock()
	c.pauseChecker = pc
	c.mu.Unlock()
}

// Send delivers a signal to the clusterer. If a pending cluster exists for
// the signal's service+namespace, the signal is merged into it (the most
// descriptive Reason wins). Otherwise a new cluster is opened with a timer
// that fires after the grouping window.
func (c *Clusterer) Send(ctx context.Context, sig Signal) {
	key := sig.Service + "/" + sig.Namespace

	c.mu.Lock()
	defer c.mu.Unlock()

	if pending, exists := c.pending[key]; exists {
		pending.signals = append(pending.signals, sig)
		if len(sig.Reason) > len(pending.signals[0].Reason) {
			pending.signals[0].Reason = sig.Reason
		}
		// Keep the longest crash-log capture (most complete evidence).
		if len(sig.PodLogs) > len(pending.signals[0].PodLogs) {
			pending.signals[0].PodLogs = sig.PodLogs
		}
		return
	}

	pending := &pendingCluster{
		signals:   []Signal{sig},
		firstSeen: time.Now(),
	}
	c.pending[key] = pending

	pending.timer = time.AfterFunc(c.window, func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Info("clusterer.emit recovered", "r", r)
			}
		}()
		c.emit(key)
	})
}

// Stop cancels every pending cluster timer. Call on shutdown so pending
// timers do not fire against a cancelled parent context.
func (c *Clusterer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, p := range c.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(c.pending, key)
	}
}

// emit fires the cluster after the grouping window closes. The pause
// checker is read under c.mu (copied to a local) and invoked outside the
// lock so a slow IsPaused call cannot block Send or Stop.
func (c *Clusterer) emit(key string) {
	c.mu.Lock()
	pending, exists := c.pending[key]
	if !exists {
		c.mu.Unlock()
		return
	}
	delete(c.pending, key)
	pauseChecker := c.pauseChecker
	c.mu.Unlock()

	if len(pending.signals) == 0 {
		return
	}
	sig := pending.signals[0]

	if c.tracker != nil && c.tracker.IsActive(sig.Service, sig.Namespace) {
		logging.Info("clusterer: skipping emit — active incident in progress", "key", key)
		return
	}

	if pauseChecker != nil && pauseChecker.IsPaused() {
		logging.Info("clusterer: skipping emit — autonomous mode paused", "key", key)
		return
	}

	var signalTypes []string
	for _, s := range pending.signals {
		signalTypes = append(signalTypes, string(s.Type))
	}

	if c.tracker != nil {
		c.tracker.MarkActive(sig.Service, sig.Namespace)
	}

	logging.Info("clusterer: emitting trigger",
		"key", key, "signals", strings.Join(signalTypes, "+"))

	if c.handler != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Info("clusterer.handler recovered", "r", r)
				}
			}()
			c.handler(sig.Service, sig.Namespace, sig.Reason, sig.PodLogs)
		}()
	}
}

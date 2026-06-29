package detection

import (
        "context"
        "log"
        "strings"
        "sync"
        "time"
)

// SignalType is what kind of detector produced this signal.

type SignalType string

const (
        SignalLogError  SignalType = "log_error"
        SignalPodStatus SignalType = "pod_status"
        SignalK8sEvent  SignalType = "k8s_event"
)

// Signal is what detectors send to the clusterer. The signal is just the
// alarm bell — the investigator fetches full logs via K8s API.

type Signal struct {
        Type      SignalType
        Service   string
        Namespace string
        Reason    string
        Repo      string
        Timestamp time.Time
}

// Clusterer groups signals by service within a time window and emits
// ONE trigger per service per window. Does NOT fetch logs — that's the
// investigator's job.

type Clusterer struct {
        handler IncidentHandler
        tracker *ActiveTracker

        mu      sync.Mutex
        pending map[string]*pendingCluster
        window  time.Duration

        // skips emitting triggers (autonomous mode paused via Slack bot).

        pauseChecker PauseChecker
}

// PauseChecker returns true if autonomous mode is paused. Used to skip
// emitting triggers without restarting Sentinel.

type PauseChecker interface {
        IsPaused() bool
}

type pendingCluster struct {
        signals   []Signal
        firstSeen time.Time
        timer     *time.Timer
}

// NewClusterer creates a Clusterer (no K8s client needed — no log fetching).

func NewClusterer(handler IncidentHandler, tracker *ActiveTracker) *Clusterer {
        return &Clusterer{
                handler: handler,
                tracker: tracker,
                pending: make(map[string]*pendingCluster),
                window:  500 * time.Millisecond,
        }
}

// SetPauseChecker wires in a pause checker (e.g. the Slack bot).
// If paused, signals are silently dropped (not buffered).
// goroutine's read at emit() (interface assignment is not atomic).

func (c *Clusterer) SetPauseChecker(pc PauseChecker) {
        c.mu.Lock()
        c.pauseChecker = pc
        c.mu.Unlock()
}

// Send sends a signal to the clusterer.

func (c *Clusterer) Send(ctx context.Context, sig Signal) {
        key := sig.Service + "/" + sig.Namespace

        c.mu.Lock()
        defer c.mu.Unlock()

        if pending, exists := c.pending[key]; exists {
                // Merge into existing cluster — keep the most descriptive reason

                pending.signals = append(pending.signals, sig)
                if len(sig.Reason) > len(pending.signals[0].Reason) {
                        pending.signals[0].Reason = sig.Reason
                }
                return
        }

        pending := &pendingCluster{
                signals:   []Signal{sig},
                firstSeen: time.Now(),
        }
        c.pending[key] = pending

        pending.timer = time.AfterFunc(c.window, func() {

                // Without this, a panic in the handler chain would kill the
                // detection layer's timer thread and silently stop clustering.

                defer func() {
                        if r := recover(); r != nil {
                                log.Printf("[panic] clusterer.emit recovered: %v", r)
                        }
                }()
                c.emit(key)
        })
}

// Stop cancels all pending cluster timers. Call on shutdown so pending timers
// don't fire after the parent context is cancelled.
// post-shutdown, spawning handler goroutines against a cancelled ctx.

func (c *Clusterer) Stop() {
        c.mu.Lock()
        defer c.mu.Unlock()
        for key, p := range c.pending {
                if p.timer != nil {
                        p.timer.Stop()
                }
                delete(c.pending, key)
        }
        log.Printf("[clusterer] stopped — cancelled %d pending clusters", len(c.pending))
}

// emit fires the cluster after the grouping window closes.
// The investigator fetches full logs via K8s API.

func (c *Clusterer) emit(key string) {
        c.mu.Lock()
        pending, exists := c.pending[key]
        if !exists {
                c.mu.Unlock()
                return
        }
        delete(c.pending, key)
        c.mu.Unlock()

        if c.tracker.IsActive(pending.signals[0].Service, pending.signals[0].Namespace) {
                log.Printf("[clusterer] skipping %s — active incident already in progress", key)
                return
        }

        if c.pauseChecker != nil && c.pauseChecker.IsPaused() {
                log.Printf("[clusterer] skipping %s — autonomous mode paused", key)
                return
        }

        sig := pending.signals[0]

        var signalTypes []string
        for _, s := range pending.signals {
                signalTypes = append(signalTypes, string(s.Type))
        }

        c.tracker.MarkActive(sig.Service, sig.Namespace)

        log.Printf("[clusterer] emitting trigger for %s (signals: %s)",
                key, strings.Join(signalTypes, "+"))

        // This is the industry pattern: detection triggers, investigation fetches context.
        // chain killed the process).

        if c.handler != nil {
                go func() {
                        defer func() {
                                if r := recover(); r != nil {
                                        log.Printf("[panic] clusterer.handler recovered: %v", r)
                                }
                        }()
                        c.handler(sig.Service, sig.Namespace, sig.Reason, "")
                }()
        }
}

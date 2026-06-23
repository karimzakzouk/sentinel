// Package detection — Signal type + Anomaly Clusterer.
//
// The clusterer is the central piece that fixes the triple-trigger bug.
// Instead of each detector firing independently, all detectors send signals
// to the clusterer. The clusterer groups them by service within a 60-second
// window and emits ONE anomaly cluster to the pipeline.
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
        SignalLogError    SignalType = "log_error"
        SignalPodStatus   SignalType = "pod_status"
        SignalK8sEvent    SignalType = "k8s_event"
)

// Signal is what detectors send to the clusterer.
type Signal struct {
        Type      SignalType
        Service   string
        Namespace string
        Reason    string
        PodLogs   string
        Repo      string
        Timestamp time.Time
}

// Clusterer groups signals by service within a time window and emits
// ONE anomaly cluster per service per window. This prevents the triple-trigger
// bug where log scanner + pod watcher + K8s events all fire for the same incident.
type Clusterer struct {
        handler    IncidentHandler
        tracker    *ActiveTracker
        preprocess *LogPreprocessor

        mu       sync.Mutex
        pending  map[string]*pendingCluster // service+namespace → pending signals
        window   time.Duration
}

type pendingCluster struct {
        signals   []Signal
        firstSeen time.Time
        timer     *time.Timer
}

// NewClusterer creates a Clusterer.
func NewClusterer(handler IncidentHandler, tracker *ActiveTracker) *Clusterer {
        return &Clusterer{
                handler:   handler,
                tracker:   tracker,
                preprocess: NewLogPreprocessor(),
                pending:   make(map[string]*pendingCluster),
                window:    5 * time.Second, // group signals within 5s
        }
}

// Send sends a signal to the clusterer. The clusterer groups it with other
// signals for the same service and emits one cluster after the window closes.
func (c *Clusterer) Send(ctx context.Context, sig Signal) {
        key := sig.Service + "/" + sig.Namespace

        c.mu.Lock()
        defer c.mu.Unlock()

        // Check if we already have a pending cluster for this service
        if pending, exists := c.pending[key]; exists {
                // Merge into existing cluster
                pending.signals = append(pending.signals, sig)
                // Use the longest pod logs
                if len(sig.PodLogs) > len(pending.signals[0].PodLogs) {
                        pending.signals[0].PodLogs = sig.PodLogs
                }
                // Use the most descriptive reason
                if len(sig.Reason) > len(pending.signals[0].Reason) {
                        pending.signals[0].Reason = sig.Reason
                }
                return
        }

        // New cluster — create pending entry with a timer
        pending := &pendingCluster{
                signals:   []Signal{sig},
                firstSeen: time.Now(),
        }
        c.pending[key] = pending

        // After the window closes, emit the cluster
        pending.timer = time.AfterFunc(c.window, func() {
                c.emit(key)
        })
}

// emit fires the cluster after the grouping window closes.
func (c *Clusterer) emit(key string) {
        c.mu.Lock()
        pending, exists := c.pending[key]
        if !exists {
                c.mu.Unlock()
                return
        }
        delete(c.pending, key)
        c.mu.Unlock()

        // Check active incident tracker — skip if this service already has an active incident
        if c.tracker.IsActive(pending.signals[0].Service, pending.signals[0].Namespace) {
                log.Printf("[clusterer] skipping %s — active incident already in progress", key)
                return
        }

        // Build the anomaly cluster from the first (primary) signal
        sig := pending.signals[0]

        // Preprocess the pod logs
        processedLogs := c.preprocess.Process(sig.PodLogs)

        // Build the explanation with all signals
        var signalTypes []string
        for _, s := range pending.signals {
                signalTypes = append(signalTypes, string(s.Type))
        }
        explanation := sig.Reason
        if processedLogs != "" {
                explanation += "\n\n## Pod Logs (preprocessed)\n" + processedLogs
        }

        // Mark as active
        c.tracker.MarkActive(sig.Service, sig.Namespace)

        log.Printf("[clusterer] emitting cluster for %s (signals: %s)", key, strings.Join(signalTypes, "+"))

        // Fire the handler with the processed logs
        if c.handler != nil {
                go c.handler(sig.Service, sig.Namespace, sig.Reason, processedLogs)
        }
}

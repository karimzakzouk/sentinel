// Package correlation detects cascading failures across services.
// If service B deploys at T=0 and services A, C, D start failing at
// T+2min, that's ONE rollback trigger, not three separate investigations.
// The correlator maintains a sliding window of active incidents. When the
// number of simultaneous incidents across DIFFERENT services exceeds a
// threshold (default 3) within a time window (default 60s), it emits a
// CorrelationEvent. The caller can then:
//   - Roll back the most-recently-deployed service (likely cause)
//   - Skip individual investigations (they're symptoms, not root causes)
//   - Open a single incident for the correlated group
// Algorithm:
//   1. Every incident is registered with RegisterIncident(service, ns, time)
//   2. The correlator keeps a sliding window of the last `window` duration
//   3. When a new incident arrives, it counts how many DISTINCT services
//      have incidents in the current window
//   4. If count >= threshold AND the new service is different from the
//      most-recent-deployed service, emit a CorrelationEvent
// The "most-recently-deployed" heuristic: the correlator accepts deployment
// events via RegisterDeployment(service, ns, time). The service that deployed
// most recently before the cascade is the likely cause.
package correlation

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "log"
        "sync"
        "time"
)

// Correlator detects cascading failures across services.

type Correlator struct {
        mu sync.Mutex

        // incidents is a sliding window of recent incidents.
        // Key: service/namespace, Value: timestamp of the incident.

        incidents map[string]time.Time

        // deployments tracks recent deployments for root-cause attribution.
        // Key: service/namespace, Value: timestamp of the deployment.

        deployments map[string]time.Time

        // Config

        window           time.Duration // default 60s
        threshold        int           // default 3 distinct services
        cooldownDuration time.Duration // default 5 min — don't re-emit for the same cascade

        // State

        lastEmission time.Time // tracks cooldown
        handler      CorrelationHandler
}

// CorrelationEvent is emitted when a cascade is detected.

type CorrelationEvent struct {
        Timestamp       time.Time
        Services        []string // all affected services
        Namespaces      []string
        LikelyCause     string // the most-recently-deployed service (if any)
        LikelyCauseTime time.Time
        IncidentCount   int
}

// CorrelationHandler is called when a cascade is detected.

type CorrelationHandler func(event CorrelationEvent)

// New creates a Correlator with default settings (60s window, 3-service threshold).

func New(handler CorrelationHandler) *Correlator {
        return &Correlator{
                incidents:        make(map[string]time.Time),
                deployments:      make(map[string]time.Time),
                window:           60 * time.Second,
                threshold:        3,
                cooldownDuration: 5 * time.Minute,
                handler:          handler,
        }
}

// SetConfig configures the correlation parameters.
// Allows tuning for different cluster sizes.

func (c *Correlator) SetConfig(window time.Duration, threshold int, cooldown time.Duration) {
        c.mu.Lock()
        defer c.mu.Unlock()
        if window > 0 {
                c.window = window
        }
        if threshold > 1 {
                c.threshold = threshold
        }
        if cooldown > 0 {
                c.cooldownDuration = cooldown
        }
}

// RegisterDeployment records that a service was deployed at the given time.
// Called by the detection layer when it observes a deployment event
// (pod created with a new image tag). The most-recently-deployed service
// before a cascade is the likely cause.

func (c *Correlator) RegisterDeployment(service, namespace string, ts time.Time) {
        c.mu.Lock()
        defer c.mu.Unlock()
        key := service + "/" + namespace
        c.deployments[key] = ts
        logging.Info("deployment registered:  at", "value", key, ts)
        // Clean old deployments

        c.cleanupDeployments(ts)
}

// RegisterIncident records a new incident and checks for correlation.
// Called by the detection layer when a new incident is triggered.
// If the number of distinct services with incidents in the window exceeds
// the threshold, a CorrelationEvent is emitted (subject to cooldown).
// Returns true if a correlation event was emitted (caller should suppress
// the individual investigation), false otherwise (caller should proceed
// with the normal investigation).
// Handler is called SYNCHRONOUSLY after releasing the lock,
// not in a goroutine. This avoids timing flakes in tests and potential
// deadlocks if the handler calls back into the correlator. The detection
// layer should not block on this — if the handler is slow, run it in a
// goroutine at the call site.

func (c *Correlator) RegisterIncident(service, namespace string, ts time.Time) bool {
        // Build the event under the lock, then release before calling handler.

        event, emitted := c.checkAndBuildEvent(service, namespace, ts)
        if !emitted {
                return false
        }

        // Call handler outside the lock to avoid deadlocks.

        if c.handler != nil {
                c.handler(event)
        }
        return true
}

// checkAndBuildEvent does the actual correlation logic under the lock.
// Returns the event and true if a cascade was detected, or zero value + false.

func (c *Correlator) checkAndBuildEvent(service, namespace string, ts time.Time) (CorrelationEvent, bool) {
        c.mu.Lock()
        defer c.mu.Unlock()

        key := service + "/" + namespace
        c.incidents[key] = ts

        // Clean expired incidents

        c.cleanupIncidents(ts)

        // Count distinct services with active incidents

        distinctServices := make(map[string]bool)
        for k := range c.incidents {
                distinctServices[k] = true
        }

        if len(distinctServices) < c.threshold {
                return CorrelationEvent{}, false
        }

        // Check cooldown — don't re-emit for the same cascade

        if time.Since(c.lastEmission) < c.cooldownDuration {
                logging.Info("cascade detected ( services) but in cooldown — suppressing", "value", len(distinctServices))
                return CorrelationEvent{}, false
        }

        // Find the most-recently-deployed service (likely cause)

        var likelyCause string
        var likelyCauseTime time.Time

        for depKey, depTime := range c.deployments {
                if ts.Sub(depTime) <= c.window*2 { // deployed within 2x window
                        if depTime.After(likelyCauseTime) {
                                likelyCause = depKey
                                likelyCauseTime = depTime
                        }
                }
        }

        // Build the event

        event := CorrelationEvent{
                Timestamp:       ts,
                LikelyCause:     likelyCause,
                LikelyCauseTime: likelyCauseTime,
                IncidentCount:   len(distinctServices),
        }
        for k := range c.incidents {
                event.Services = append(event.Services, k)
        }

        c.lastEmission = ts
        log.Printf("[correlation] CASCADE DETECTED: %d services failing in %v window. Likely cause: %s (deployed at %v)",
                len(distinctServices), c.window, likelyCause, likelyCauseTime)

        return event, true
}

// cleanupIncidents removes incidents older than the window.

func (c *Correlator) cleanupIncidents(now time.Time) {
        for key, ts := range c.incidents {
                if now.Sub(ts) > c.window {
                        delete(c.incidents, key)
                }
        }
}

// cleanupDeployments removes deployments older than 10x the window.

func (c *Correlator) cleanupDeployments(now time.Time) {
        for key, ts := range c.deployments {
                if now.Sub(ts) > c.window*10 {
                        delete(c.deployments, key)
                }
        }
}

// ActiveIncidentCount returns the number of distinct services with active
// incidents in the current window. v3.5.0: Used for metrics/debugging.

func (c *Correlator) ActiveIncidentCount() int {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.cleanupIncidents(time.Now())
        return len(c.incidents)
}

// IsLikelyCascade returns true if the given service's incident is likely
// part of an ongoing cascade (i.e., other services are also failing).
// The detection layer can use this to skip individual investigation
// and wait for the correlation handler to trigger a rollback instead.

func (c *Correlator) IsLikelyCascade(service, namespace string) bool {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.cleanupIncidents(time.Now())
        return len(c.incidents) >= c.threshold
}

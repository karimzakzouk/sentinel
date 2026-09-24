package correlation

import (
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Correlator detects cascading failures across services. It is safe for
// concurrent use.
type Correlator struct {
	mu sync.Mutex

	// incidents is the sliding window of recent incidents keyed by
	// "service/namespace".
	incidents map[string]time.Time

	// deployments tracks recent deployments for root-cause attribution,
	// keyed by "service/namespace".
	deployments map[string]time.Time

	// Configurable knobs; see New and SetConfig for defaults.
	window           time.Duration
	threshold        int
	cooldownDuration time.Duration

	// State.
	lastEmission time.Time
	handler      CorrelationHandler
}

// CorrelationEvent is emitted when a cascade is detected.
type CorrelationEvent struct {
	Timestamp       time.Time
	Services        []string
	Namespaces      []string
	LikelyCause     string
	LikelyCauseTime time.Time
	IncidentCount   int
}

// CorrelationHandler is invoked synchronously by RegisterIncident when a
// cascade is detected. It is called with the correlator's lock released.
type CorrelationHandler func(event CorrelationEvent)

// New returns a Correlator with the default 60s window, 3-service threshold,
// and 5-minute cooldown. handler may be nil.
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

// SetConfig overrides window, threshold, and cooldownDuration. Zero values
// leave the existing setting unchanged.
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

// RegisterDeployment records that a service was deployed at ts. The
// correlator keeps deployments for 10*window; the most-recent one before a
// cascade is reported as the likely cause.
func (c *Correlator) RegisterDeployment(service, namespace string, ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := service + "/" + namespace
	c.deployments[key] = ts
	logging.Info("correlation: deployment registered", "key", key, "ts", ts)
	c.cleanupDeploymentsLocked(ts)
}

// RegisterIncident records a new incident and checks for a cascade.
// Returns true when the caller should suppress the individual
// investigation in favor of cascade handling — which is only the case
// when the emitted event carries a non-empty LikelyCause (a recent
// deployment the cascade is attributed to). A cascade with no
// attributable cause is indistinguishable from "several services error
// in the same minute under shared load": suppressing investigations
// then silently drops real incidents (observed in the marketly eval run
// #10, where an empty-cause cascade swallowed a fully-detected Rust
// panic). The handler, if set, is invoked synchronously after the lock
// is released — slow handlers should be run in a goroutine by the
// caller.
func (c *Correlator) RegisterIncident(service, namespace string, ts time.Time) bool {
	event, emitted := c.checkAndBuildEvent(service, namespace, ts)
	if !emitted {
		return false
	}
	if c.handler != nil {
		c.handler(event)
	}
	if event.LikelyCause == "" {
		logging.Info("correlation: cascade emitted without attributable cause — individual investigations continue",
			"services", len(event.Services))
		return false
	}
	return true
}

// checkAndBuildEvent does the actual correlation logic under the lock and
// returns the event plus an emitted flag.
func (c *Correlator) checkAndBuildEvent(service, namespace string, ts time.Time) (CorrelationEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := service + "/" + namespace
	c.incidents[key] = ts
	c.cleanupIncidentsLocked(ts)

	if len(c.incidents) < c.threshold {
		return CorrelationEvent{}, false
	}

	if time.Since(c.lastEmission) < c.cooldownDuration {
		logging.Info("correlation: cascade detected but in cooldown, suppressing",
			"distinct_services", len(c.incidents))
		return CorrelationEvent{}, false
	}

	var likelyCause string
	var likelyCauseTime time.Time
	for depKey, depTime := range c.deployments {
		if ts.Sub(depTime) <= c.window*2 {
			if depTime.After(likelyCauseTime) {
				likelyCause = depKey
				likelyCauseTime = depTime
			}
		}
	}

	event := CorrelationEvent{
		Timestamp:       ts,
		LikelyCause:     likelyCause,
		LikelyCauseTime: likelyCauseTime,
		IncidentCount:   len(c.incidents),
	}
	seenSvc := map[string]bool{}
	seenNs := map[string]bool{}
	for k := range c.incidents {
		svc, ns := splitKey(k)
		if !seenSvc[svc] {
			seenSvc[svc] = true
			event.Services = append(event.Services, svc)
		}
		if !seenNs[ns] {
			seenNs[ns] = true
			event.Namespaces = append(event.Namespaces, ns)
		}
	}

	c.lastEmission = ts
	logging.Info("correlation: CASCADE DETECTED",
		"services", len(event.Services),
		"window", c.window,
		"likely_cause", likelyCause,
		"likely_cause_ts", likelyCauseTime)

	return event, true
}

// cleanupIncidentsLocked drops incidents older than the window. Caller must
// hold c.mu.
func (c *Correlator) cleanupIncidentsLocked(now time.Time) {
	for key, ts := range c.incidents {
		if now.Sub(ts) > c.window {
			delete(c.incidents, key)
		}
	}
}

// cleanupDeploymentsLocked drops deployments older than 10*window. Caller
// must hold c.mu.
func (c *Correlator) cleanupDeploymentsLocked(now time.Time) {
	for key, ts := range c.deployments {
		if now.Sub(ts) > c.window*10 {
			delete(c.deployments, key)
		}
	}
}

// ActiveIncidentCount returns the number of distinct service+namespace keys
// with active incidents in the current window. Used for metrics/debugging.
func (c *Correlator) ActiveIncidentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupIncidentsLocked(time.Now())
	return len(c.incidents)
}

// IsLikelyCascade reports whether the current incident count is at or above
// the cascade threshold. The detection layer uses this to skip individual
// investigation when a cascade is already in progress.
func (c *Correlator) IsLikelyCascade(service, namespace string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupIncidentsLocked(time.Now())
	return len(c.incidents) >= c.threshold
}

// splitKey splits a "service/namespace" key. Service names cannot contain
// "/" so a single split is unambiguous.
func splitKey(key string) (service, namespace string) {
	idx := strings.LastIndex(key, "/")
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}

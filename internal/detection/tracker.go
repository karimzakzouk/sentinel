package detection

import (
	"log"
	"sync"
	"time"
)

// ActiveTracker prevents duplicate incidents for the same service.
// Once an incident is active for a service, all new signals are skipped
// until the incident is resolved.
//
// After resolution:
//   - If PR opened: 30-minute cooldown (don't re-trigger for the same bug)
//   - If failed: clear immediately (allow retry)
type ActiveTracker struct {
	mu      sync.Mutex
	active  map[string]*incidentState // service+namespace → state
	cooldown time.Duration
}

type incidentState struct {
	startedAt  time.Time
	resolvedAt time.Time
	resolved   bool
	success    bool
}

func NewActiveTracker() *ActiveTracker {
	return &ActiveTracker{
		active:   make(map[string]*incidentState),
		cooldown: 30 * time.Minute,
	}
}

// IsActive returns true if the service has an active or recently-resolved incident.
func (t *ActiveTracker) IsActive(service, namespace string) bool {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.active[key]
	if !exists {
		return false
	}

	if !state.resolved {
		// Still active
		return true
	}

	// Resolved — check cooldown
	if state.success {
		// PR was opened — 30-min cooldown
		if time.Since(state.resolvedAt) < t.cooldown {
			return true
		}
		// Cooldown expired — clean up
		delete(t.active, key)
		return false
	}

	// Failed — allow retry immediately
	delete(t.active, key)
	return false
}

// MarkActive marks a service as having an active incident.
func (t *ActiveTracker) MarkActive(service, namespace string) {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[key] = &incidentState{
		startedAt: time.Now(),
	}
	log.Printf("[tracker] marked active: %s", key)
}

// MarkResolved marks a service's incident as resolved.
func (t *ActiveTracker) MarkResolved(service, namespace string, success bool) {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()

	if state, exists := t.active[key]; exists {
		state.resolved = true
		state.resolvedAt = time.Now()
		state.success = success
		log.Printf("[tracker] resolved: %s (success=%v, cooldown=%v)", key, success, t.cooldown)
	}
}

// Cleanup removes old resolved entries.
func (t *ActiveTracker) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for key, state := range t.active {
		if state.resolved && now.Sub(state.resolvedAt) > t.cooldown {
			delete(t.active, key)
		}
	}
}

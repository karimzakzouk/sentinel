package detection

import (
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// ActiveTracker prevents duplicate incidents for the same service. Once an
// incident is active for a service+namespace, all new signals are skipped
// until the incident is resolved. After resolution:
//
//   - Success (PR opened): 30-minute cooldown so the same bug is not
//     re-triggered while the fix is rolling out.
//   - Failure: cleared immediately so the next signal can retry.
type ActiveTracker struct {
	mu       sync.Mutex
	active   map[string]*incidentState
	cooldown time.Duration
}

// incidentState is the per-service lifecycle state.
type incidentState struct {
	startedAt  time.Time
	resolvedAt time.Time
	resolved   bool
	success    bool
}

// NewActiveTracker returns a tracker with the default 30-minute success
// cooldown.
func NewActiveTracker() *ActiveTracker {
	return &ActiveTracker{
		active:   make(map[string]*incidentState),
		cooldown: 30 * time.Minute,
	}
}

// SetCooldown overrides the post-success cooldown. Useful for tests.
func (t *ActiveTracker) SetCooldown(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d > 0 {
		t.cooldown = d
	}
}

// IsActive reports whether the service+namespace has an active or
// recently-resolved incident subject to the cooldown policy.
func (t *ActiveTracker) IsActive(service, namespace string) bool {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.active[key]
	if !exists {
		return false
	}
	if !state.resolved {
		return true
	}
	if state.success {
		if time.Since(state.resolvedAt) < t.cooldown {
			return true
		}
		delete(t.active, key)
		return false
	}
	// Failure: allow retry immediately.
	delete(t.active, key)
	return false
}

// MarkActive records that an incident is now in-flight for the
// service+namespace.
func (t *ActiveTracker) MarkActive(service, namespace string) {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[key] = &incidentState{
		startedAt: time.Now(),
	}
	logging.Info("tracker: marked active", "key", key)
}

// MarkResolved marks the in-flight incident resolved. success=true means a
// PR was opened (apply the cooldown); success=false means the investigation
// failed (clear immediately on next signal).
func (t *ActiveTracker) MarkResolved(service, namespace string, success bool) {
	key := service + "/" + namespace
	t.mu.Lock()
	defer t.mu.Unlock()
	if state, exists := t.active[key]; exists {
		state.resolved = true
		state.resolvedAt = time.Now()
		state.success = success
		logging.Info("tracker: resolved",
			"key", key, "success", success, "cooldown", t.cooldown)
	}
}

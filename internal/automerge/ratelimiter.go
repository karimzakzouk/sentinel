// Package automerge — rate limiter + cooldown.
package automerge

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window counter that allows at most N events
// per duration. Used to cap auto-merges per hour.

type RateLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	events   []time.Time // timestamps of recent events
}

// NewRateLimiter creates a RateLimiter that allows max events per window.

func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	if max <= 0 {
		max = 3
	}
	if window <= 0 {
		window = time.Hour
	}
	return &RateLimiter{max: max, window: window}
}

// Allow returns true if an event can be admitted now without exceeding the limit.
// Does NOT record the event — call Record() to actually consume a slot.

func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evict()
	return len(r.events) < r.max
}

// Record consumes a slot. Returns false if the limit was already reached
// (the event was NOT recorded in that case).

func (r *RateLimiter) Record() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evict()
	if len(r.events) >= r.max {
		return false
	}
	r.events = append(r.events, time.Now())
	return true
}

// Count returns the number of events in the current window.

func (r *RateLimiter) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evict()
	return len(r.events)
}

// evict removes events older than the window. Caller must hold the lock.

func (r *RateLimiter) evict() {
	cutoff := time.Now().Add(-r.window)
	// Find the first event that's still within the window.

	idx := 0
	for idx < len(r.events) && r.events[idx].Before(cutoff) {
		idx++
	}
	if idx > 0 {
		r.events = r.events[idx:]
	}
}

// Cooldown tracks a pause-until timestamp. When active, auto-merge is blocked.

type Cooldown struct {
	mu          sync.Mutex
	until       time.Time
	reason      string
}

// NewCooldown creates a Cooldown with the given default pause duration.

func NewCooldown(defaultDuration time.Duration) *Cooldown {
	return &Cooldown{}
}

// Trigger activates the cooldown for the given duration.
// If already active, extends to the later of (current expiry, now + duration).

func (c *Cooldown) Trigger(reason string) {
	c.TriggerFor(c.defaultDuration(), reason)
}

// TriggerFor activates the cooldown for a specific duration.

func (c *Cooldown) TriggerFor(d time.Duration, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	newUntil := time.Now().Add(d)
	if newUntil.After(c.until) {
		c.until = newUntil
		c.reason = reason
	}
}

// IsPaused returns true if the cooldown is currently active.

func (c *Cooldown) IsPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.until)
}

// PausedUntil returns when the cooldown expires (zero time if not active).

func (c *Cooldown) PausedUntil() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.until) {
		return c.until
	}
	return time.Time{}
}

// Reason returns why the cooldown was activated (empty if not active).

func (c *Cooldown) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.until) {
		return c.reason
	}
	return ""
}

// Clear manually clears the cooldown (e.g. via /sentinel override cooldown).

func (c *Cooldown) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = time.Time{}
	c.reason = ""
}

// defaultDuration is the default pause duration when Trigger() is called
// without an explicit duration. We store it on the Cooldown struct.

func (c *Cooldown) defaultDuration() time.Duration {
	// Default 1 hour. Operators can configure via the policy Config.

	return time.Hour
}

package events

import "testing"

// TestNoopBroadcaster verifies the no-op broadcaster accepts any input
// without panicking.
func TestNoopBroadcaster(t *testing.T) {
	b := Noop()
	b.BroadcastEvent("test", map[string]any{"key": "value"})
	b.BroadcastEvent("incident", nil)
	b.BroadcastEvent("", nil)
}

// TestNoopReturnsBroadcaster verifies Noop satisfies the Broadcaster
// interface at compile time.
func TestNoopReturnsBroadcaster(t *testing.T) {
	var _ Broadcaster = Noop()
}

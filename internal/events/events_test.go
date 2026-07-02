package events

import "testing"

func TestNoopBroadcaster(t *testing.T) {
	b := Noop()
	// Should not panic on any operation
	b.BroadcastEvent("test", map[string]any{"key": "value"})
	b.BroadcastEvent("incident", nil)
	b.BroadcastEvent("", nil)
}

func TestNoopReturnsBroadcaster(t *testing.T) {
	b := Noop()
	var _ Broadcaster = b
}

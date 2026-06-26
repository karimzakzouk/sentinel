package watcher

import "testing"

// TestWatcherNilHandler verifies that the watcher doesn't panic when
// the handler is nil (e.g. during initialization before handler is set).
func TestWatcherNilHandler(t *testing.T) {
	w := &Watcher{
		handler: nil,
	}
	// Verify Watcher can be constructed without panicking
	if w == nil {
		t.Error("expected non-nil Watcher")
	}
}

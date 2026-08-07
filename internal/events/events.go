package events

// Broadcaster is the sink for in-process events destined for live UI updates.
// Implementations must be safe for concurrent use.
type Broadcaster interface {
	// BroadcastEvent notifies all subscribers of an event. Implementations
	// must not block on slow subscribers; the data payload is opaque to the
	// broadcaster and is marshaled by the subscriber.
	BroadcastEvent(eventType string, data any)
}

// noopBroadcaster drops every event. Used when the API layer is disabled.
type noopBroadcaster struct{}

// BroadcastEvent implements Broadcaster by silently dropping the event.
func (noopBroadcaster) BroadcastEvent(string, any) {}

// Noop returns a Broadcaster that discards every event.
func Noop() Broadcaster { return noopBroadcaster{} }

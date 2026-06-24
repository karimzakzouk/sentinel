// Package events provides a simple event broadcaster for real-time UI updates.
//
// v3.7.0: The API server's SSE endpoint pushes events to connected browser
// clients. This package provides the interface that the investigation handler
// calls to broadcast events (incident started, pipeline step completed, PR opened).
//
// The API server implements this interface. main.go wires it together.
package events

// Broadcaster pushes events to connected SSE clients.
// Implemented by api.Server.
type Broadcaster interface {
	BroadcastEvent(eventType string, data interface{})
}

// noopBroadcaster does nothing — used when no SSE clients are connected
// or when the API server isn't available (tests).
type noopBroadcaster struct{}

func (noopBroadcaster) BroadcastEvent(string, interface{}) {}

// Noop returns a broadcaster that does nothing. Used in tests.
func Noop() Broadcaster { return noopBroadcaster{} }

// Event types — match what the UI listens for.
const (
	EventIncidentStarted  = "incident"
	EventIncidentResolved = "incident"
	EventPipelineStep     = "pipeline_step"
	EventPRopened         = "pr_opened"
	EventDetection        = "detection"
	EventLog              = "log"
)

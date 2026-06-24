package events

type Broadcaster interface {
	BroadcastEvent(eventType string, data interface{})
}

type noopBroadcaster struct{}
func (noopBroadcaster) BroadcastEvent(string, interface{}) {}
func Noop() Broadcaster { return noopBroadcaster{} }

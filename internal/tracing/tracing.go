package tracing

import (
	"context"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

type spanKey struct{}

// Span is a lightweight tracing span. Start one with Start, end it with End.
type Span struct {
	name       string
	startTime  time.Time
	attributes map[string]string
	parent     *Span
	mu         sync.Mutex
	ended      bool
}

// Start begins a new span and returns a context with the span attached.
// The name should describe the operation (e.g. "investigator.run",
// "llm.call", "sandbox.verify"). Spans nest: the returned context's span
// has the caller's active span as its parent.
func Start(ctx context.Context, name string) (context.Context, *Span) {
	parent, _ := ctx.Value(spanKey{}).(*Span)
	span := &Span{
		name:       name,
		startTime:  time.Now(),
		attributes: make(map[string]string),
		parent:     parent,
	}
	return context.WithValue(ctx, spanKey{}, span), span
}

// SetAttribute sets a key-value attribute on the span. Safe for concurrent
// use (the attributes map is guarded by the span mutex).
func (s *Span) SetAttribute(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attributes[key] = value
}

// End completes the span and emits a structured log line via the logging
// package. Calling End twice is a no-op.
func (s *Span) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()

	duration := time.Since(s.startTime)
	attrs := make([]any, 0, len(s.attributes)*2+2)
	attrs = append(attrs, "span", s.name, "duration_ms", duration.Milliseconds())
	for k, v := range s.attributes {
		attrs = append(attrs, k, v)
	}
	logging.Info("span", attrs...)
}

// FromContext returns the span attached to ctx, or nil if no span is active.
func FromContext(ctx context.Context) *Span {
	span, _ := ctx.Value(spanKey{}).(*Span)
	return span
}

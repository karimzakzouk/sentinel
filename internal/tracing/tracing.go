// Package tracing provides lightweight span tracing for the agent pipeline.
//
// Instead of pulling in the full OpenTelemetry SDK (which would add 20+
// transitive dependencies), this package implements a minimal span tracker
// that logs span start/end/duration via slog. Spans are correlated by
// incident_id (set via context) and can be exported to any structured log
// sink (Datadog, Loki, Splunk).
//
// Future: if/when OTel is needed, wrap these spans with otel-go's StartSpan.
// The API is deliberately similar (ctx, name, attributes) to make migration
// a one-line change per call site.
package tracing

import (
	"context"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

type spanKey struct{}

// Span is a lightweight tracing span. Start one with Start(), end it with End().
type Span struct {
	name       string
	startTime  time.Time
	attributes map[string]string
	parent     *Span
	mu         sync.Mutex
	ended      bool
}

// Start begins a new span and returns a context with the span attached.
// The span name should describe the operation (e.g. "investigator.run",
// "llm.call", "sandbox.verify").
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

// SetAttribute sets a key-value attribute on the span.
func (s *Span) SetAttribute(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attributes[key] = value
}

// End completes the span and logs it via slog.
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

// FromContext returns the span from the context, or nil if no span is active.
func FromContext(ctx context.Context) *Span {
	span, _ := ctx.Value(spanKey{}).(*Span)
	return span
}

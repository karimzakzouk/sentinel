// Package agents — composite trace querier that delegates to Jaeger and/or Tempo.
package agents

import "context"

// CompositeTraceQuerier implements TraceQuerier by delegating to whichever
// backend is configured. When both are set, Jaeger is preferred (richer API).
// When neither is set, all methods return "not configured".
type CompositeTraceQuerier struct {
	Jaeger TraceQuerier // may be nil
	Tempo  TraceQuerier // may be nil
}

// QueryTraceAPI delegates to Tempo (Tempo's native API).
func (c *CompositeTraceQuerier) QueryTraceAPI(ctx context.Context, service string) (map[string]any, error) {
	if c.Tempo != nil {
		return c.Tempo.QueryTraceAPI(ctx, service)
	}
	if c.Jaeger != nil {
		// Fall back to Jaeger if Tempo isn't configured.
		return c.Jaeger.QueryJaegerTraces(ctx, service)
	}
	return map[string]any{"error": "tempo_url not configured"}, nil
}

// QueryJaegerTraces delegates to Jaeger (Jaeger's native API).
func (c *CompositeTraceQuerier) QueryJaegerTraces(ctx context.Context, service string) (map[string]any, error) {
	if c.Jaeger != nil {
		return c.Jaeger.QueryJaegerTraces(ctx, service)
	}
	if c.Tempo != nil {
		// Fall back to Tempo if Jaeger isn't configured.
		return c.Tempo.QueryTraceAPI(ctx, service)
	}
	return map[string]any{"error": "jaeger_url not configured"}, nil
}

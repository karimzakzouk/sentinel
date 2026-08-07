// Package tracing provides lightweight span tracing for the agent pipeline.
//
// Rather than pulling in the full OpenTelemetry SDK (which would add 20+
// transitive dependencies), this package implements a minimal span tracker
// that emits structured span start/end/duration logs via the logging
// package. Spans are correlated by an incident_id (set on a context.Value)
// and nest by parent pointer — enough signal to reconstruct the pipeline
// timeline from a log sink (Loki, Splunk, Datadog).
//
// The API mirrors OpenTelemetry's (Start returns a context + Span; End
// closes the span) so a future migration to OTel would be a one-line
// change per call site.
package tracing

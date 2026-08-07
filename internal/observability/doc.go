// Package observability implements ingestion from the standard 2026
// observability stack: Prometheus, Loki, and Tempo.
//
// Sentinel watches K8s events + Alertmanager out of the box. This package
// adds three read-only HTTP clients so the evidence gatherer (Phase 3) and
// the post-merge regression watcher (Phase 8) can pull additional signals:
//
//   - PrometheusQuerier: runs PromQL instant queries, computes SLO burn
//     rates, and snapshots the per-service health metrics used by the
//     post-merge watcher.
//   - LokiClient: pulls recent error-level log lines for a service.
//   - TempoClient: pulls recent slow traces for a service.
//
// All three clients share a single http.Client each and are read-only —
// Sentinel never writes to the observability stack.
//
// TODO(phase-8): replace local MetricsQuerier and MetricsSource with the
// real slo.MetricsQuerier and postmerge.MetricsSource interfaces once
// those packages are ported. The local definitions below mirror the
// intended method signatures so the swap is a one-line change.
package observability

import (
	"context"
	"time"
)

// SLO describes a service-level objective. TODO(phase-8): replace with
// slo.SLO once the slo package is ported.
type SLO struct {
	Service   string
	Namespace string
	Target    float64 // success ratio target (e.g. 0.999 for three-nines).
	Query     string  // PromQL with {{service}}, {{namespace}}, {{window}} placeholders.
}

// MetricSnapshot is a point-in-time health snapshot of one service.
// TODO(phase-8): replace with postmerge.MetricSnapshot once postmerge is
// ported.
type MetricSnapshot struct {
	Timestamp     time.Time
	ErrorRate     float64
	P99LatencyMs  float64
	RestartCount  int64
	CPUPercent    float64
	MemoryPercent float64
	SLOBurnRate   float64
}

// MetricsQuerier is the SLO burn-rate query interface. TODO(phase-8):
// replace with slo.MetricsQuerier once slo is ported.
type MetricsQuerier interface {
	QueryBurnRate(ctx context.Context, s SLO, longWindow, shortWindow time.Duration) (long, short float64, err error)
}

// MetricsSource is the per-service health snapshot interface.
// TODO(phase-8): replace with postmerge.MetricsSource once postmerge is
// ported.
type MetricsSource interface {
	Snapshot(ctx context.Context, service, namespace string) (*MetricSnapshot, error)
}

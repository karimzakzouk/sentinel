// Package observability implements ingestion from the standard observability
// stack: Prometheus, Loki, and Tempo.
//
// Sentinel watches K8s events + Alertmanager out of the box. This package
// adds three read-only HTTP clients so the evidence gatherer and the
// post-merge regression watcher can pull additional signals:
//
//   - PrometheusQuerier: runs PromQL instant queries, computes SLO burn
//     rates, and snapshots the per-service health metrics used by the
//     post-merge watcher. It satisfies both slo.MetricsQuerier and
//     postmerge.MetricsSource.
//   - LokiClient: pulls recent error-level log lines for a service.
//   - TempoClient: pulls recent slow traces for a service.
//
// All three clients share a single http.Client each and are read-only —
// Sentinel never writes to the observability stack.
package observability

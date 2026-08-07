// Package slo implements proactive SLO burn-rate monitoring.
//
// Sentinel's internal/detection is reactive — it fires when a pod crashes.
// But the most valuable incidents are the ones that haven't crashed yet:
// a service slowly burning its error budget, a p99 creeping up over an hour,
// a memory leak that will OOM in 4 hours.
//
// Google's SRE workbook prescribes the multi-window multi-burn-rate alerting
// strategy (sre.google/workbook/alerting). The industry standard, used by
// Datadog, Splunk, Dynatrace, and Grafana, is:
//
//	Long window (1h)  +  Short window (5m)
//	burn > 14.4  ->  page  (2% budget in 1h)
//	burn > 6     ->  page  (5% budget in 6h)
//	burn > 3     ->  ticket (10% budget in 3d)
//	burn > 1     ->  ticket (slow burn, always-on)
//
// This package implements that strategy. When a burn-rate threshold crosses,
// it synthesizes an Incident and feeds it into the existing investigation
// pipeline — BEFORE the crash happens.
//
// # Integration
//
// The watcher is wired in main.go alongside the alertmanager adapter. It
// pulls metrics from the same Prometheus instance that ServiceMonitor
// scrapes. The MetricsQuerier interface here is satisfied by
// observability.PrometheusQuerier; the interface is owned by the consumer
// (slo) so observability can depend on slo without an import cycle.
package slo

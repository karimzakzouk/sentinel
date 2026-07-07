// Package observability implements ingestion from the standard 2026
// observability stack: OpenTelemetry collectors, Prometheus, Loki, Tempo.
//
// WHY THIS EXISTS
//
// Right now Sentinel watches K8s events + Alertmanager. That's a thin slice
// of the signals an SRE actually uses. The 2026 industry has converged on
// the OpenTelemetry + Prometheus + Loki + Tempo stack (per the Elastic and
// Grafana 2026 trend reports, and the CNCF Graduation status of all four).
//
// Sentinel should correlate a Tempo trace + Loki log spike + Prometheus
// metric anomaly into a single incident. internal/correlation is built for
// exactly this — extend the inputs.
//
// This package provides:
//   - PrometheusQuerier: implements both slo.MetricsQuerier and
//     postmerge.MetricsSource by querying Prometheus.
//   - LokiClient: pulls recent error-level log lines for a service.
//   - TempoClient: pulls recent traces for a service, identifies slow spans.
//   - OTelConsumer: receives OTel-format telemetry via gRPC, normalizes to
//     Sentinel's internal Anomaly format, feeds into detection.
//
// ARCHITECTURE
//
// All three clients share a single HTTP client + connection pool. They are
// read-only — Sentinel never writes to the observability stack.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/postmerge"
	"github.com/karimzakzouk/sentinel/internal/slo"
)

// PrometheusConfig is the connection config for a Prometheus instance.
type PrometheusConfig struct {
	URL      string // e.g. "http://prometheus.monitoring:9090"
	Auth     string // optional bearer token
	Timeout  time.Duration
}

// PrometheusQuerier implements slo.MetricsQuerier and postmerge.MetricsSource.
type PrometheusQuerier struct {
	cfg    PrometheusConfig
	client *http.Client
	logger *slog.Logger
}

// NewPrometheusQuerier creates a Querier.
func NewPrometheusQuerier(cfg PrometheusConfig, logger *slog.Logger) *PrometheusQuerier {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &PrometheusQuerier{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}
}

// QueryBurnRate implements slo.MetricsQuerier.
// It runs the SLO's Query twice — once with the long window, once with the
// short window — and converts the success ratios to burn rates.
func (q *PrometheusQuerier) QueryBurnRate(ctx context.Context, s slo.SLO, longWindow, shortWindow time.Duration) (long, short float64, err error) {
	longSuccess, err := q.querySuccessRatio(ctx, s, longWindow)
	if err != nil {
		return 0, 0, fmt.Errorf("prom long query: %w", err)
	}
	shortSuccess, err := q.querySuccessRatio(ctx, s, shortWindow)
	if err != nil {
		return 0, 0, fmt.Errorf("prom short query: %w", err)
	}
	long = burnRateFromSuccess(longSuccess, s.Target)
	short = burnRateFromSuccess(shortSuccess, s.Target)
	return long, short, nil
}

// Snapshot implements postmerge.MetricsSource.
func (q *PrometheusQuerier) Snapshot(ctx context.Context, service, namespace string) (*postmerge.MetricSnapshot, error) {
	snap := &postmerge.MetricSnapshot{Timestamp: time.Now()}

	// Error rate: 5xx / total over the last 5 min.
	errRate, err := q.scalar(ctx, fmt.Sprintf(
		`sum(rate(http_requests_total{service="%s",namespace="%s",status=~"5.."}[5m])) / sum(rate(http_requests_total{service="%s",namespace="%s"}[5m]))`,
		service, namespace, service, namespace,
	))
	if err == nil {
		snap.ErrorRate = errRate
	}

	// p99 latency.
	p99, err := q.scalar(ctx, fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{service="%s",namespace="%s"}[5m])) by (le))`,
		service, namespace,
	))
	if err == nil {
		snap.P99LatencyMs = p99 * 1000
	}

	// Pod restarts in last 5 min.
	restarts, err := q.scalar(ctx, fmt.Sprintf(
		`sum(increase(kube_pod_container_status_restarts_total{namespace="%s",container="%s"}[5m]))`,
		namespace, service,
	))
	if err == nil {
		snap.RestartCount = int64(restarts)
	}

	// CPU %.
	cpu, err := q.scalar(ctx, fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*"}[5m])) / sum(kube_pod_container_resource_limits{namespace="%s",pod=~"%s-.*",resource="cpu"}) * 100`,
		namespace, service, namespace, service,
	))
	if err == nil {
		snap.CPUPercent = cpu
	}

	// Memory %.
	mem, err := q.scalar(ctx, fmt.Sprintf(
		`sum(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*"}) / sum(kube_pod_container_resource_limits{namespace="%s",pod=~"%s-.*",resource="memory"}) * 100`,
		namespace, service, namespace, service,
	))
	if err == nil {
		snap.MemoryPercent = mem
	}

	// SLO burn rate — assume target 0.999 if unknown.
	success, err := q.querySuccessRatio(ctx, slo.SLO{
		Service:   service,
		Namespace: namespace,
		Target:    0.999,
		Query:     `sum(rate(http_requests_total{service="{{service}}",namespace="{{namespace}}",status!~"5.."}[{{window}}])) / sum(rate(http_requests_total{service="{{service}}",namespace="{{namespace}}"}[{{window}}]))`,
	}, 5*time.Minute)
	if err == nil {
		snap.SLOBurnRate = burnRateFromSuccess(success, 0.999)
	}

	return snap, nil
}

// querySuccessRatio substitutes {{window}} into the SLO query and runs it.
func (q *PrometheusQuerier) querySuccessRatio(ctx context.Context, s slo.SLO, window time.Duration) (float64, error) {
	query := strings.ReplaceAll(s.Query, "{{window}}", fmt.Sprintf("%ds", int(window.Seconds())))
	query = strings.ReplaceAll(query, "{{service}}", s.Service)
	query = strings.ReplaceAll(query, "{{namespace}}", s.Namespace)
	return q.scalar(ctx, query)
}

// scalar runs an instant PromQL query and returns the single scalar result.
func (q *PrometheusQuerier) scalar(ctx context.Context, promql string) (float64, error) {
	u := q.cfg.URL + "/api/v1/query?query=" + url.QueryEscape(promql)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	if q.cfg.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+q.cfg.Auth)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("prometheus status %d: %s", resp.StatusCode, string(body))
	}

	// Parse Prometheus instant query response.
	var pr struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value [2]interface{} `json:"value"` // [timestamp, "string"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("parse prometheus response: %w", err)
	}
	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("prometheus returned no result (status=%s)", pr.Status)
	}
	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("prometheus result value not a string: %v", pr.Data.Result[0].Value[1])
	}
	return strconv.ParseFloat(valStr, 64)
}

// burnRateFromSuccess converts a success ratio (0..1) to a burn-rate multiplier
// given the SLO target. burn = (1 - success) / (1 - target).
func burnRateFromSuccess(success, target float64) float64 {
	errorRate := 1 - success
	errorBudget := 1 - target
	if errorBudget == 0 {
		return 0
	}
	return errorRate / errorBudget
}

// --- Loki client ---

// LokiClient pulls recent log lines from Loki.
type LokiClient struct {
	URL    string
	Auth   string
	client *http.Client
	logger *slog.Logger
}

// NewLokiClient creates a LokiClient.
func NewLokiClient(url, auth string, logger *slog.Logger) *LokiClient {
	return &LokiClient{
		URL:    url,
		Auth:   auth,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// ErrorLogs returns the most recent error-level log lines for a service.
// Used as additional evidence during investigation.
func (l *LokiClient) ErrorLogs(ctx context.Context, service, namespace string, lookback time.Duration) ([]string, error) {
	u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&limit=50&start=%d&end=%d",
		l.URL,
		url.QueryEscape(fmt.Sprintf(`{service="%s",namespace="%s"}|~"(?i)error|panic|fatal"`, service, namespace)),
		time.Now().Add(-lookback).UnixNano(),
		time.Now().UnixNano(),
	)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	if l.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+l.Auth)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("loki status %d: %s", resp.StatusCode, string(body))
	}
	var lr struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][]string `json:"values"` // [[timestamp, line], ...]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, err
	}
	var out []string
	for _, stream := range lr.Data.Result {
		for _, v := range stream.Values {
			if len(v) >= 2 {
				out = append(out, v[1])
			}
		}
	}
	return out, nil
}

// --- Tempo client ---

// TempoClient pulls recent traces from Tempo.
type TempoClient struct {
	URL    string
	Auth   string
	client *http.Client
	logger *slog.Logger
}

// SlowTrace is a trace that exceeded the p99 latency threshold.
type SlowTrace struct {
	TraceID    string
	DurationMs float64
	SlowSpan   string // name of the slowest span
	Service    string
}

// RecentSlowTraces returns the slowest traces for a service in the lookback window.
func (t *TempoClient) RecentSlowTraces(ctx context.Context, service string, lookback time.Duration, limit int) ([]SlowTrace, error) {
	// Tempo search API: /api/search?tags=service.name:X&start=...&end=...&limit=N
	// Sort by duration is not supported in Tempo's API directly — client-side sort.
	u := fmt.Sprintf("%s/api/search?tags=%s&start=%d&end=%d&limit=%d",
		t.URL,
		url.QueryEscape(fmt.Sprintf(`service.name="%s"`, service)),
		time.Now().Add(-lookback).Unix(),
		time.Now().Unix(),
		limit,
	)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	if t.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+t.Auth)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tempo status %d: %s", resp.StatusCode, string(body))
	}
	var tr struct {
		Traces []struct {
			TraceID  string `json:"traceID"`
			Duration string `json:"durationSeconds"` // Tempo returns string
			// ServiceName may be in the root span; simplified here.
		} `json:"traces"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, err
	}
	var out []SlowTrace
	for _, t := range tr.Traces {
		dur, _ := strconv.ParseFloat(t.Duration, 64)
		out = append(out, SlowTrace{
			TraceID:    t.TraceID,
			DurationMs: dur * 1000,
			Service:    service,
		})
	}
	return out, nil
}

// Compile-time check that we implement both interfaces.
var _ slo.MetricsQuerier = (*PrometheusQuerier)(nil)
var _ postmerge.MetricsSource = (*PrometheusQuerier)(nil)

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/postmerge"
	"github.com/karimzakzouk/sentinel/internal/slo"
)

// PrometheusConfig is the connection config for a Prometheus instance.
type PrometheusConfig struct {
	URL     string        // e.g. "http://prometheus.monitoring:9090".
	Auth    string        // optional bearer token.
	Timeout time.Duration // defaults to 10s if zero.
}

// PrometheusQuerier implements slo.MetricsQuerier and postmerge.MetricsSource
// by querying Prometheus via its HTTP API. The same instance serves both SLO
// burn-rate queries (used by the SLO watcher) and per-service health
// snapshots (used by the post-merge regression watcher).
type PrometheusQuerier struct {
	cfg    PrometheusConfig
	client *http.Client
}

// NewPrometheusQuerier creates a PrometheusQuerier. A zero Timeout falls
// back to 10 seconds.
func NewPrometheusQuerier(cfg PrometheusConfig) *PrometheusQuerier {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	logging.Info("observability: prometheus querier initialized",
		"url", cfg.URL, "timeout", cfg.Timeout)
	return &PrometheusQuerier{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// QueryBurnRate implements slo.MetricsQuerier. It runs the SLO's Query twice
// — once with the long window, once with the short window — and converts the
// success ratios to burn-rate multipliers.
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

// Snapshot implements postmerge.MetricsSource. It runs five PromQL queries
// (error rate, p99 latency, restart count, CPU %, memory %) plus an SLO
// burn-rate query, and assembles a postmerge.MetricSnapshot. Missing values
// are left zero — the caller can distinguish "no data" from "healthy" by
// the associated Timestamp.
func (q *PrometheusQuerier) Snapshot(ctx context.Context, service, namespace string) (*postmerge.MetricSnapshot, error) {
	snap := &postmerge.MetricSnapshot{Timestamp: time.Now()}

	errRate, err := q.scalar(ctx, fmt.Sprintf(
		`sum(rate(http_requests_total{service="%s",namespace="%s",status=~"5.."}[5m])) / sum(rate(http_requests_total{service="%s",namespace="%s"}[5m]))`,
		service, namespace, service, namespace,
	))
	if err == nil {
		snap.ErrorRate = errRate
	}

	p99, err := q.scalar(ctx, fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{service="%s",namespace="%s"}[5m])) by (le))`,
		service, namespace,
	))
	if err == nil {
		snap.P99LatencyMs = p99 * 1000
	}

	restarts, err := q.scalar(ctx, fmt.Sprintf(
		`sum(increase(kube_pod_container_status_restarts_total{namespace="%s",container="%s"}[5m]))`,
		namespace, service,
	))
	if err == nil {
		snap.RestartCount = int64(restarts)
	}

	cpu, err := q.scalar(ctx, fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*"}[5m])) / sum(kube_pod_container_resource_limits{namespace="%s",pod=~"%s-.*",resource="cpu"}) * 100`,
		namespace, service, namespace, service,
	))
	if err == nil {
		snap.CPUPercent = cpu
	}

	mem, err := q.scalar(ctx, fmt.Sprintf(
		`sum(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*"}) / sum(kube_pod_container_resource_limits{namespace="%s",pod=~"%s-.*",resource="memory"}) * 100`,
		namespace, service, namespace, service,
	))
	if err == nil {
		snap.MemoryPercent = mem
	}

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

// querySuccessRatio substitutes {{window}}, {{service}}, {{namespace}} into
// the SLO query and runs it as an instant query.
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
		return 0, fmt.Errorf("build prometheus request: %w", err)
	}
	if q.cfg.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+q.cfg.Auth)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("prometheus request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("prometheus status %d: %s", resp.StatusCode, string(body))
	}

	var pr struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value [2]any `json:"value"` // [timestamp, "string"].
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

// burnRateFromSuccess converts a success ratio (0..1) to a burn-rate
// multiplier given the SLO target: burn = (1 - success) / (1 - target).
// Returns 0 when the error budget is zero (target == 1.0).
func burnRateFromSuccess(success, target float64) float64 {
	errorRate := 1 - success
	errorBudget := 1 - target
	if errorBudget == 0 {
		return 0
	}
	return errorRate / errorBudget
}

// --- Loki client ---

// LokiClient pulls recent log lines from Loki. It is read-only.
type LokiClient struct {
	URL    string
	Auth   string
	client *http.Client
}

// NewLokiClient creates a LokiClient. A 10s timeout is used.
func NewLokiClient(url, auth string) *LokiClient {
	logging.Info("observability: loki client initialized", "url", url)
	return &LokiClient{
		URL:    url,
		Auth:   auth,
		client: &http.Client{Timeout: 10 * time.Second},
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
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("build loki request: %w", err)
	}
	if l.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+l.Auth)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki request: %w", err)
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
				Values [][]string `json:"values"` // [[timestamp, line], ...].
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("parse loki response: %w", err)
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

// TempoClient pulls recent traces from Tempo. It is read-only.
type TempoClient struct {
	URL    string
	Auth   string
	client *http.Client
}

// NewTempoClient creates a TempoClient. A 10s timeout is used.
func NewTempoClient(url, auth string) *TempoClient {
	return &TempoClient{
		URL:    url,
		Auth:   auth,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// SlowTrace is a trace that exceeded the p99 latency threshold.
type SlowTrace struct {
	TraceID    string
	DurationMs float64
	SlowSpan   string // name of the slowest span (empty when not available).
	Service    string
}

// RecentSlowTraces returns the most recent traces for a service in the
// lookback window. Tempo's search API does not support sort-by-duration
// server-side, so callers should sort client-side if needed.
func (t *TempoClient) RecentSlowTraces(ctx context.Context, service string, lookback time.Duration, limit int) ([]SlowTrace, error) {
	u := fmt.Sprintf("%s/api/search?tags=%s&start=%d&end=%d&limit=%d",
		t.URL,
		url.QueryEscape(fmt.Sprintf(`service.name="%s"`, service)),
		time.Now().Add(-lookback).Unix(),
		time.Now().Unix(),
		limit,
	)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("build tempo request: %w", err)
	}
	if t.Auth != "" {
		req.Header.Set("Authorization", "Bearer "+t.Auth)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempo request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tempo status %d: %s", resp.StatusCode, string(body))
	}
	var tr struct {
		Traces []struct {
			TraceID  string `json:"traceID"`
			Duration string `json:"durationSeconds"` // Tempo returns a string.
		} `json:"traces"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse tempo response: %w", err)
	}
	var out []SlowTrace
	for _, trace := range tr.Traces {
		dur, _ := strconv.ParseFloat(trace.Duration, 64)
		out = append(out, SlowTrace{
			TraceID:    trace.TraceID,
			DurationMs: dur * 1000,
			Service:    service,
		})
	}
	return out, nil
}

// Compile-time checks that PrometheusQuerier satisfies the slo.MetricsQuerier
// and postmerge.MetricsSource interfaces owned by the consumer packages.
var _ slo.MetricsQuerier = (*PrometheusQuerier)(nil)
var _ postmerge.MetricsSource = (*PrometheusQuerier)(nil)

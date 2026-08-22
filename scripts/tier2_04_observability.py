#!/usr/bin/env python3
"""Add JaegerClient + QueryLogQL/QueryTrace methods to observability package."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
obs_path = REPO / "internal/observability/prometheus.go"
obs = obs_path.read_text()

# Add JaegerClient + QueryLogQL/QueryTrace methods at the end, before the compile-time checks
old_end = """// Compile-time checks that PrometheusQuerier satisfies the slo.MetricsQuerier
// and postmerge.MetricsSource interfaces owned by the consumer packages.
var _ slo.MetricsQuerier = (*PrometheusQuerier)(nil)
var _ postmerge.MetricsSource = (*PrometheusQuerier)(nil)"""

new_end = """// --- Jaeger client ---

// JaegerClient queries Jaeger for traces. It is read-only.
type JaegerClient struct {
\tURL    string
\tAuth   string
\tclient *http.Client
}

// NewJaegerClient creates a JaegerClient. A 10s timeout is used.
func NewJaegerClient(url, auth string) *JaegerClient {
\treturn &JaegerClient{
\t\tURL:    url,
\t\tAuth:   auth,
\t\tclient: &http.Client{Timeout: 10 * time.Second},
\t}
}

// JaegerTrace is a trace from Jaeger with its duration and service info.
type JaegerTrace struct {
\tTraceID    string
\tDurationMs float64
\tService    string
\tSpans      int
}

// RecentTraces returns the most recent traces for a service in the lookback
// window. Jaeger's API returns traces sorted by start time (most recent first).
func (j *JaegerClient) RecentTraces(ctx context.Context, service string, lookback time.Duration, limit int) ([]JaegerTrace, error) {
\tu := fmt.Sprintf("%s/api/traces?service=%s&limit=%d&start=%d&end=%d",
\t\tj.URL,
\t\turl.QueryEscape(service),
\t\tlimit,
\t\ttime.Now().Add(-lookback).UnixMicro(),
\t\ttime.Now().UnixMicro(),
\t)
\treq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
\tif err != nil {
\t\treturn nil, fmt.Errorf("build jaeger request: %w", err)
\t}
\tif j.Auth != "" {
\t\treq.Header.Set("Authorization", "Bearer "+j.Auth)
\t}
\tresp, err := j.client.Do(req)
\tif err != nil {
\t\treturn nil, fmt.Errorf("jaeger request: %w", err)
\t}
\tdefer resp.Body.Close()
\tbody, _ := io.ReadAll(resp.Body)
\tif resp.StatusCode != 200 {
\t\treturn nil, fmt.Errorf("jaeger status %d: %s", resp.StatusCode, string(body))
\t}
\tvar jr struct {
\t\tData []struct {
\t\t\tTraceID string `json:"traceID"`
\t\t\tSpans   []struct {
\t\t\t\tOperationName string `json:"operationName"`
\t\t\t\tDuration      int64  `json:"duration"` // microseconds
\t\t\t} `json:"spans"`
\t\t\tProcesses map[string]struct {
\t\t\t\tServiceName string `json:"serviceName"`
\t\t\t} `json:"processes"`
\t\t} `json:"data"`
\t}
\tif err := json.Unmarshal(body, &jr); err != nil {
\t\treturn nil, fmt.Errorf("parse jaeger response: %w", err)
\t}
\tvar out []JaegerTrace
\tfor _, trace := range jr.Data {
\t\tvar totalDurUs int64
\t\tsvc := service
\t\tfor _, span := range trace.Spans {
\t\t\tif span.Duration > totalDurUs {
\t\t\t\ttotalDurUs = span.Duration
\t\t\t}
\t\t}
\t\tfor _, proc := range trace.Processes {
\t\t\tif proc.ServiceName != "" {
\t\t\t\tsvc = proc.ServiceName
\t\t\t\tbreak
\t\t\t}
\t\t}
\t\tout = append(out, JaegerTrace{
\t\t\tTraceID:    trace.TraceID,
\t\t\tDurationMs: float64(totalDurUs) / 1000.0,
\t\t\tService:    svc,
\t\t\tSpans:      len(trace.Spans),
\t\t})
\t}
\treturn out, nil
}

// --- Pluggable evidence querier methods ---
//
// These methods are called by the agents.EvidenceGatherer when the LLM
// generates a query of the corresponding type. Each returns a simplified
// map that the LLM analyst can reason about.

// QueryLogQL runs a LogQL query against Loki and returns matching log lines.
// The query is expected to be a full LogQL expression (e.g. '{service="x"}|~"error"').
func (l *LokiClient) QueryLogQL(ctx context.Context, logql string) (map[string]any, error) {
\tif l == nil || l.URL == "" {
\t\treturn map[string]any{"error": "loki_url not configured"}, nil
\t}
\tu := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&limit=50&start=%d&end=%d",
\t\tl.URL,
\t\turl.QueryEscape(logql),
\t\ttime.Now().Add(-15*time.Minute).UnixNano(),
\t\ttime.Now().UnixNano(),
\t)
\treq, err := http.NewRequestWithContext(ctx, "GET", u, nil)
\tif err != nil {
\t\treturn nil, fmt.Errorf("build loki request: %w", err)
\t}
\tif l.Auth != "" {
\t\treq.Header.Set("Authorization", "Bearer "+l.Auth)
\t}
\tresp, err := l.client.Do(req)
\tif err != nil {
\t\treturn map[string]any{"error": fmt.Sprintf("loki request: %v", err)}, nil
\t}
\tdefer resp.Body.Close()
\tbody, _ := io.ReadAll(resp.Body)
\tif resp.StatusCode != 200 {
\t\treturn map[string]any{"error": fmt.Sprintf("loki status %d", resp.StatusCode), "body": string(body[:min(len(body), 500)])}, nil
\t}
\tvar lr struct {
\t\tStatus string `json:"status"`
\t\tData   struct {
\t\t\tResult []struct {
\t\t\t\tValues [][]string `json:"values"`
\t\t\t} `json:"result"`
\t\t} `json:"data"`
\t}
\tif err := json.Unmarshal(body, &lr); err != nil {
\t\treturn map[string]any{"error": fmt.Sprintf("parse loki: %v", err)}, nil
\t}
\tvar lines []string
\tfor _, stream := range lr.Data.Result {
\t\tfor _, v := range stream.Values {
\t\t\tif len(v) >= 2 {
\t\t\t\tlines = append(lines, v[1])
\t\t\t}
\t\t}
\t}
\treturn map[string]any{
\t\t"query":  logql,
\t\t"count":  len(lines),
\t\t"lines":  lines,
\t}, nil
}

// QueryTraceAPI searches Tempo for traces matching the service. The query
// is expected to be a service name (Tempo's search API uses tags, not a
// query language).
func (t *TempoClient) QueryTraceAPI(ctx context.Context, service string) (map[string]any, error) {
\tif t == nil || t.URL == "" {
\t\treturn map[string]any{"error": "tempo_url not configured"}, nil
\t}
\ttraces, err := t.RecentSlowTraces(ctx, service, 15*time.Minute, 20)
\tif err != nil {
\t\treturn map[string]any{"error": fmt.Sprintf("tempo: %v", err)}, nil
\t}
\treturn map[string]any{
\t\t"service": service,
\t\t"count":   len(traces),
\t\t"traces":  traces,
\t}, nil
}

// QueryJaegerTraces searches Jaeger for traces matching the service.
func (j *JaegerClient) QueryJaegerTraces(ctx context.Context, service string) (map[string]any, error) {
\tif j == nil || j.URL == "" {
\t\treturn map[string]any{"error": "jaeger_url not configured"}, nil
\t}
\ttraces, err := j.RecentTraces(ctx, service, 15*time.Minute, 20)
\tif err != nil {
\t\treturn map[string]any{"error": fmt.Sprintf("jaeger: %v", err)}, nil
\t}
\treturn map[string]any{
\t\t"service": service,
\t\t"count":   len(traces),
\t\t"traces":  traces,
\t}, nil
}

func min(a, b int) int {
\tif a < b {
\t\treturn a
\t}
\treturn b
}

// Compile-time checks that PrometheusQuerier satisfies the slo.MetricsQuerier
// and postmerge.MetricsSource interfaces owned by the consumer packages.
var _ slo.MetricsQuerier = (*PrometheusQuerier)(nil)
var _ postmerge.MetricsSource = (*PrometheusQuerier)(nil)"""

assert old_end in obs, "observability/prometheus.go: end not found"
obs = obs.replace(old_end, new_end, 1)
obs_path.write_text(obs)
print("OK: observability/prometheus.go — JaegerClient + QueryLogQL/QueryTraceAPI/QueryJaegerTraces added")

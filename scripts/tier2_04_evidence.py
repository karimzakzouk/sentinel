#!/usr/bin/env python3
"""Make evidence_gatherer pluggable — add Loki/Jaeger/Tempo support."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
eg_path = REPO / "internal/agents/evidence_gatherer.go"
eg = eg_path.read_text()

# 1. Add pluggable querier fields to EvidenceGatherer struct
old_struct = """type EvidenceGatherer struct {
\trouter        *llm.Router
\tk8s           *k8s.Client
\tprometheusURL string
\thttpClient    *http.Client
}"""

new_struct = """// LogQuerier is the interface for log backends (Loki, etc.).
type LogQuerier interface {
\tQueryLogQL(ctx context.Context, logql string) (map[string]any, error)
}

// TraceQuerier is the interface for tracing backends (Jaeger, Tempo).
type TraceQuerier interface {
\tQueryTraceAPI(ctx context.Context, service string) (map[string]any, error)
\tQueryJaegerTraces(ctx context.Context, service string) (map[string]any, error)
}

type EvidenceGatherer struct {
\trouter        *llm.Router
\tk8s           *k8s.Client
\tprometheusURL string
\thttpClient    *http.Client

\t// Pluggable evidence backends. nil = not configured (queries return
\t// "not configured" error instead of failing).
\tlogQuerier   LogQuerier   // Loki
\ttraceQuerier TraceQuerier // Jaeger or Tempo
}"""

assert old_struct in eg, "evidence_gatherer.go: struct not found"
eg = eg.replace(old_struct, new_struct, 1)

# 2. Add SetLogQuerier / SetTraceQuerier after SetPrometheusURL
old_set_prom = """func (a *EvidenceGatherer) SetPrometheusURL(url string) {
\ta.prometheusURL = url
}"""

new_set_prom = """func (a *EvidenceGatherer) SetPrometheusURL(url string) {
\ta.prometheusURL = url
}

// SetLogQuerier wires a log backend (e.g. observability.LokiClient) for
// evidence queries of type "loki". nil disables Loki queries.
func (a *EvidenceGatherer) SetLogQuerier(q LogQuerier) {
\ta.logQuerier = q
}

// SetTraceQuerier wires a tracing backend (e.g. observability.JaegerClient or
// observability.TempoClient) for evidence queries of type "jaeger" or "tempo".
// nil disables trace queries.
func (a *EvidenceGatherer) SetTraceQuerier(q TraceQuerier) {
\ta.traceQuerier = q
}"""

assert old_set_prom in eg, "evidence_gatherer.go: SetPrometheusURL not found"
eg = eg.replace(old_set_prom, new_set_prom, 1)

# 3. Update executeQuery to use the pluggable backends
old_exec = """// executeQuery dispatches to the right backend based on query type.
func (a *EvidenceGatherer) executeQuery(ctx context.Context, query map[string]any) map[string]any {
\tqtype, _ := query["type"].(string)
\tq, _ := query["query"].(string)

\tswitch qtype {
\tcase "k8s":
\t\treturn a.queryK8s(ctx, q)
\tcase "prometheus":
\t\treturn a.queryPrometheus(ctx, q)
\tcase "loki", "jaeger":
\t\t// Loki uses LogQL, Jaeger has its own trace query API. Both need
\t\t// client code that hasn't been ported yet.
\t\treturn map[string]any{"error": fmt.Sprintf("%s_url not configured", qtype)}
\tdefault:
\t\treturn map[string]any{"error": fmt.Sprintf("unknown query type: %s", qtype)}
\t}
}"""

new_exec = """// executeQuery dispatches to the right backend based on query type.
// Each backend is pluggable — when not configured, returns a "not configured"
// error instead of failing the whole evidence-gathering pass.
func (a *EvidenceGatherer) executeQuery(ctx context.Context, query map[string]any) map[string]any {
\tqtype, _ := query["type"].(string)
\tq, _ := query["query"].(string)

\tswitch qtype {
\tcase "k8s":
\t\treturn a.queryK8s(ctx, q)
\tcase "prometheus":
\t\treturn a.queryPrometheus(ctx, q)
\tcase "loki":
\t\tif a.logQuerier == nil {
\t\t\treturn map[string]any{"error": "loki_url not configured"}
\t\t}
\t\tresult, err := a.logQuerier.QueryLogQL(ctx, q)
\t\tif err != nil {
\t\t\treturn map[string]any{"error": fmt.Sprintf("loki query failed: %v", err)}
\t\t}
\t\treturn result
\tcase "jaeger":
\t\tif a.traceQuerier == nil {
\t\t\treturn map[string]any{"error": "jaeger_url not configured"}
\t\t}
\t\t// For Jaeger, the "query" is the service name.
\t\tresult, err := a.traceQuerier.QueryJaegerTraces(ctx, q)
\t\tif err != nil {
\t\t\treturn map[string]any{"error": fmt.Sprintf("jaeger query failed: %v", err)}
\t\t}
\t\treturn result
\tcase "tempo":
\t\tif a.traceQuerier == nil {
\t\t\treturn map[string]any{"error": "tempo_url not configured"}
\t\t}
\t\t// For Tempo, the "query" is the service name.
\t\tresult, err := a.traceQuerier.QueryTraceAPI(ctx, q)
\t\tif err != nil {
\t\t\treturn map[string]any{"error": fmt.Sprintf("tempo query failed: %v", err)}
\t\t}
\t\treturn result
\tdefault:
\t\treturn map[string]any{"error": fmt.Sprintf("unknown query type: %s", qtype)}
\t}
}"""

assert old_exec in eg, "evidence_gatherer.go: executeQuery not found"
eg = eg.replace(old_exec, new_exec, 1)

eg_path.write_text(eg)
print("OK: evidence_gatherer.go — pluggable Loki/Jaeger/Tempo support")

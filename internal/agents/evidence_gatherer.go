package agents

// evidence_gatherer.go: the LLM agent that generates queries against
// Loki/Prometheus/Jaeger/K8s, executes them in parallel, and attaches
// evidence to each hypothesis. The analyst then uses the evidence to falsify
// or support each hypothesis via Bayes + Beta priors.
//
// Backends:
//   - K8s: events, pods, deployments (via the in-cluster clientset)
//   - Prometheus: PromQL instant queries via /api/v1/query
//   - Loki / Jaeger: stubs returning "not configured" — need their own query
//     languages and client code, which lands in a later phase.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

const evidenceSystemPrompt = `You are Sentinel's Evidence Gatherer agent.

You are given a list of root cause hypotheses. For each hypothesis, decide:
  1. What query would *support* this hypothesis? (e.g. a Prometheus query for high connection count, a Loki log query for the specific error message)
  2. What query would *contradict* this hypothesis?

You can query:
  - Loki (log search): {"type": "loki", "query": "{service=\"X\",namespace=\"Y\"} |= \"keyword\""}
  - Prometheus (metrics): {"type": "prometheus", "query": "sum(rate(http_requests_total{status=\"500\"}[5m]))"}
  - Jaeger (traces): {"type": "jaeger", "query": "service:X operation:Y tags:error=true"}
  - Kubernetes (events): {"type": "k8s", "query": "namespace:Y kind:Pod reason:BackOff"}

Respond with a JSON object of shape:
{
  "evidence_queries": [
    {
      "hypothesis_id": "H1",
      "supports_or_contradicts": "supports|contradicts",
      "query": { ... },
      "expectation": "what we expect to see if hypothesis is true"
    }
  ],
  "rewind": null
}

Set "rewind" ONLY if you discover a brand-new fact that invalidates the entire hypothesis set (e.g. a hard upstream dependency is down). Leave it null 99% of the time.`

// EvidenceGatherer generates queries, executes them in parallel, and attaches
// evidence to each hypothesis.
// LogQuerier is the interface for log backends (Loki, etc.).
type LogQuerier interface {
	QueryLogQL(ctx context.Context, logql string) (map[string]any, error)
}

// TraceQuerier is the interface for tracing backends (Jaeger, Tempo).
type TraceQuerier interface {
	QueryTraceAPI(ctx context.Context, service string) (map[string]any, error)
	QueryJaegerTraces(ctx context.Context, service string) (map[string]any, error)
}

type EvidenceGatherer struct {
	router        *llm.Router
	k8s           *k8s.Client
	prometheusURL string
	httpClient    *http.Client

	// Pluggable evidence backends. nil = not configured (queries return
	// "not configured" error instead of failing).
	logQuerier   LogQuerier   // Loki
	traceQuerier TraceQuerier // Jaeger or Tempo
}

// NewEvidenceGatherer creates an EvidenceGatherer.
func NewEvidenceGatherer(router *llm.Router, k8sClient *k8s.Client) *EvidenceGatherer {
	return &EvidenceGatherer{
		router:     router,
		k8s:        k8sClient,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SetPrometheusURL configures the Prometheus backend for evidence queries.
// When set, evidence queries of type "prometheus" are executed against this
// URL. When empty, prometheus queries return a "not configured" error.
func (a *EvidenceGatherer) SetPrometheusURL(url string) {
	a.prometheusURL = url
}

// SetLogQuerier wires a log backend (e.g. observability.LokiClient) for
// evidence queries of type "loki". nil disables Loki queries.
func (a *EvidenceGatherer) SetLogQuerier(q LogQuerier) {
	a.logQuerier = q
}

// SetTraceQuerier wires a tracing backend (e.g. observability.JaegerClient or
// observability.TempoClient) for evidence queries of type "jaeger" or "tempo".
// nil disables trace queries.
func (a *EvidenceGatherer) SetTraceQuerier(q TraceQuerier) {
	a.traceQuerier = q
}

// Run executes the evidence gatherer agent. An LLM failure is non-fatal —
// the analyst will pick the top hypothesis by prior confidence.
func (a *EvidenceGatherer) Run(ctx context.Context, state *schemas.AgentState) error {
	if len(state.Hypotheses) == 0 {
		return nil
	}

	userMsg := a.buildUserMessage(state)
	result, err := a.router.FastJSON(ctx, evidenceSystemPrompt, userMsg, 4000)
	if err != nil {
		// Don't fail the whole investigation — continue with no evidence.
		// The Analyst will pick the top hypothesis by prior confidence.
		state.Evidence = nil
		state.AgentTrace = append(state.AgentTrace, map[string]any{
			"agent":  "evidence_gatherer",
			"action": "llm_failed_no_evidence",
			"error":  truncateErr(fmt.Sprintf("%v", err)),
		})
		return nil
	}

	// Check for rewind.
	if rewind, ok := result["rewind"].(map[string]any); ok && len(rewind) > 0 {
		reason, _ := rewind["reason"].(string)
		newCtx, _ := rewind["new_context"].(map[string]any)
		ev := schemas.RewindEvent{
			RaisedBy:   "evidence_gatherer",
			Reason:     reason,
			NewContext: newCtx,
			TS:         time.Now().UTC(),
		}
		state.RewindEvents = append(state.RewindEvents, ev)
		state.Hypotheses = nil // wipe — Investigator will re-run
		state.AgentTrace = append(state.AgentTrace, map[string]any{
			"agent":  "evidence_gatherer",
			"action": "rewind",
			"reason": reason,
		})
		return nil
	}

	// Execute all queries in parallel (with per-query timeout).
	queries, _ := result["evidence_queries"].([]any)
	var (
		mu       sync.Mutex
		evidence []schemas.Evidence
		wg       sync.WaitGroup
	)

	for i, q := range queries {
		qMap, ok := q.(map[string]any)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(idx int, qm map[string]any) {
			defer wg.Done()
			defer recoverPanic("evidence_gatherer")
			ev := a.runSingleQuery(ctx, idx, qm)
			mu.Lock()
			evidence = append(evidence, ev)
			mu.Unlock()
		}(i, qMap)
	}
	wg.Wait()

	state.Evidence = evidence
	state.AgentTrace = append(state.AgentTrace, map[string]any{
		"agent":          "evidence_gatherer",
		"evidence_count": len(evidence),
	})
	return nil
}

// buildUserMessage constructs the prompt for the LLM: service info,
// hypotheses to test, and the cluster context for query construction.
func (a *EvidenceGatherer) buildUserMessage(state *schemas.AgentState) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("# Service: %s (namespace: %s)", state.Cluster.Service, state.Cluster.Namespace))
	parts = append(parts, "")
	parts = append(parts, "## Hypotheses to test:")
	for _, h := range state.Hypotheses {
		parts = append(parts, fmt.Sprintf("### %s (confidence=%.2f)", h.HID, h.Confidence))
		parts = append(parts, h.Statement)
		if len(h.SupportingEvidence) > 0 {
			parts = append(parts, "Supporting evidence so far:")
			for _, e := range h.SupportingEvidence {
				parts = append(parts, fmt.Sprintf("  - %s", e))
			}
		}
		parts = append(parts, "")
	}
	parts = append(parts, "## Cluster context (for query construction):")
	parts = append(parts, state.Cluster.Summary)
	parts = append(parts, "")
	parts = append(parts, "## Your Task")
	parts = append(parts, "Produce evidence queries for each hypothesis. Be concrete — use the actual service name and namespace.")
	return strings.Join(parts, "\n")
}

// runSingleQuery executes a single LLM-generated query and returns the
// Evidence struct with the result attached.
func (a *EvidenceGatherer) runSingleQuery(ctx context.Context, idx int, q map[string]any) schemas.Evidence {
	query, _ := q["query"].(map[string]any)
	qtype, _ := query["type"].(string)
	hypID, _ := q["hypothesis_id"].(string)
	supportsOrContradicts, _ := q["supports_or_contradicts"].(string)

	queryJSON, _ := json.Marshal(query)
	ev := schemas.Evidence{
		EID:    fmt.Sprintf("E%d", idx+1),
		Source: qtype,
		Query:  string(queryJSON),
		TS:     time.Now().UTC(),
	}
	switch supportsOrContradicts {
	case "supports":
		ev.SupportsHypothesisID = &hypID
	case "contradicts":
		ev.ContradictsHypothesisID = &hypID
	}

	// Per-query timeout so one slow backend doesn't block the whole batch.
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ev.Result = a.executeQuery(queryCtx, query)
	return ev
}

// executeQuery dispatches to the right backend based on query type.
// Each backend is pluggable — when not configured, returns a "not configured"
// error instead of failing the whole evidence-gathering pass.
func (a *EvidenceGatherer) executeQuery(ctx context.Context, query map[string]any) map[string]any {
	qtype, _ := query["type"].(string)
	q, _ := query["query"].(string)

	switch qtype {
	case "k8s":
		return a.queryK8s(ctx, q)
	case "prometheus":
		return a.queryPrometheus(ctx, q)
	case "loki":
		if a.logQuerier == nil {
			return map[string]any{"error": "loki_url not configured"}
		}
		result, err := a.logQuerier.QueryLogQL(ctx, q)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("loki query failed: %v", err)}
		}
		return result
	case "jaeger":
		if a.traceQuerier == nil {
			return map[string]any{"error": "jaeger_url not configured"}
		}
		// For Jaeger, the "query" is the service name.
		result, err := a.traceQuerier.QueryJaegerTraces(ctx, q)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("jaeger query failed: %v", err)}
		}
		return result
	case "tempo":
		if a.traceQuerier == nil {
			return map[string]any{"error": "tempo_url not configured"}
		}
		// For Tempo, the "query" is the service name.
		result, err := a.traceQuerier.QueryTraceAPI(ctx, q)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("tempo query failed: %v", err)}
		}
		return result
	default:
		return map[string]any{"error": fmt.Sprintf("unknown query type: %s", qtype)}
	}
}

// queryPrometheus executes a PromQL instant query against the configured
// Prometheus. Returns the result as a simplified map (metric labels + values)
// — the LLM analyst doesn't need the full Prometheus response format.
func (a *EvidenceGatherer) queryPrometheus(ctx context.Context, promql string) map[string]any {
	if a.prometheusURL == "" {
		return map[string]any{"error": "prometheus_url not configured"}
	}

	u := strings.TrimRight(a.prometheusURL, "/") + "/api/v1/query"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("build request: %v", err)}
	}
	q := req.URL.Query()
	q.Set("query", promql)
	req.URL.RawQuery = q.Encode()

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("prometheus request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("read response: %v", err)}
	}

	if resp.StatusCode != 200 {
		return map[string]any{
			"error": fmt.Sprintf("prometheus returned %d", resp.StatusCode),
			"body":  truncateStr(string(body), 500),
		}
	}

	// Parse Prometheus response: {"status":"success","data":{"resultType":"vector","result":[{...}]}}
	var promResp struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"` // [timestamp, "value"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &promResp); err != nil {
		return map[string]any{"error": fmt.Sprintf("unmarshal: %v", err)}
	}
	if promResp.Status != "success" {
		return map[string]any{
			"error": promResp.Error,
			"query": promql,
		}
	}

	// Simplify the result for the LLM: just metric labels + values.
	results := make([]map[string]any, 0, len(promResp.Data.Result))
	for _, r := range promResp.Data.Result {
		entry := map[string]any{
			"metric": r.Metric,
		}
		if len(r.Value) >= 2 {
			// r.Value[1] is a string like "123.456"
			if v, ok := r.Value[1].(string); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					entry["value"] = f
				} else {
					entry["value"] = v
				}
			}
		}
		results = append(results, entry)
	}
	return map[string]any{
		"query":      promql,
		"resultType": promResp.Data.ResultType,
		"results":    results,
		"count":      len(results),
	}
}

// queryK8s queries the K8s API for events, pods, or deployments. The query
// format is "namespace:N kind:K [labelSelector:K=V] [fieldSelector:K=V] [limit:N]".
func (a *EvidenceGatherer) queryK8s(ctx context.Context, query string) map[string]any {
	parts := parseQueryParts(query)
	namespace := parts["namespace"]
	if namespace == "" {
		namespace = "default"
	}
	kind := parts["kind"]
	limit := int64(50)
	if l, ok := parts["limit"]; ok {
		if _, err := fmt.Sscanf(l, "%d", &limit); err != nil {
			limit = 50 // invalid limit — keep default
		}
	}

	listOpts := metav1.ListOptions{Limit: limit}
	if ls, ok := parts["labelSelector"]; ok {
		listOpts.LabelSelector = ls
	}
	if fs, ok := parts["fieldSelector"]; ok {
		listOpts.FieldSelector = fs
	}

	cs := a.k8s.Clientset()
	switch strings.ToLower(kind) {
	case "event", "events":
		events, err := cs.CoreV1().Events(namespace).List(ctx, listOpts)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		items := make([]map[string]any, 0, len(events.Items))
		for i, e := range events.Items {
			if int64(i) >= limit {
				break
			}
			items = append(items, map[string]any{
				"name":            e.Name,
				"reason":          e.Reason,
				"message":         e.Message,
				"count":           e.Count,
				"involved_object": fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
				"type":            e.Type,
			})
		}
		return map[string]any{"items": items, "count": len(items)}
	case "pod", "pods":
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, listOpts)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		items := make([]map[string]any, 0, len(pods.Items))
		for i, p := range pods.Items {
			if int64(i) >= limit {
				break
			}
			restarts := 0
			for _, cs := range p.Status.ContainerStatuses {
				restarts += int(cs.RestartCount)
			}
			items = append(items, map[string]any{
				"name":          p.Name,
				"phase":         string(p.Status.Phase),
				"restart_count": restarts,
				"node":          p.Spec.NodeName,
			})
		}
		return map[string]any{"items": items, "count": len(items)}
	case "deployment", "deployments":
		deps, err := cs.AppsV1().Deployments(namespace).List(ctx, listOpts)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		items := make([]map[string]any, 0, len(deps.Items))
		for i, d := range deps.Items {
			if int64(i) >= limit {
				break
			}
			containers := []string{}
			for _, c := range d.Spec.Template.Spec.Containers {
				containers = append(containers, c.Image)
			}
			items = append(items, map[string]any{
				"name":           d.Name,
				"replicas":       d.Spec.Replicas,
				"ready_replicas": d.Status.ReadyReplicas,
				"image":          containers,
			})
		}
		return map[string]any{"items": items, "count": len(items)}
	default:
		return map[string]any{"error": fmt.Sprintf("unsupported k8s kind: %q. Use 'event', 'pod', or 'deployment'.", kind)}
	}
}

// parseQueryParts parses "key:value key:value" pairs into a map.
func parseQueryParts(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Fields(s) {
		if idx := strings.Index(part, ":"); idx > 0 {
			out[part[:idx]] = part[idx+1:]
		}
	}
	return out
}

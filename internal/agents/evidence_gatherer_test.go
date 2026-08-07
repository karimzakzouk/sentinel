package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestEvidenceGatherer_Prometheus verifies that the Prometheus backend
// executes PromQL queries and parses the response correctly.
func TestEvidenceGatherer_Prometheus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("expected /api/v1/query, got %s", r.URL.Path)
		}
		query := r.URL.Query().Get("query")
		if query != "up{job=\"api\"}" {
			t.Errorf("expected query 'up{job=\"api\"}', got %q", query)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "vector",
				"result": [
					{
						"metric": {"__name__": "up", "job": "api", "instance": "api:8080"},
						"value": [1700000000, "1"]
					},
					{
						"metric": {"__name__": "up", "job": "api", "instance": "api:8081"},
						"value": [1700000000, "0"]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	result := eg.queryPrometheus(context.Background(), "up{job=\"api\"}")

	if err, ok := result["error"]; ok {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["resultType"] != "vector" {
		t.Errorf("resultType = %v, want vector", result["resultType"])
	}
	results, ok := result["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results should be []map[string]any, got %T", result["results"])
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0]["value"] != 1.0 {
		t.Errorf("first value = %v, want 1.0", results[0]["value"])
	}
	if results[1]["value"] != 0.0 {
		t.Errorf("second value = %v, want 0.0", results[1]["value"])
	}
}

// TestEvidenceGatherer_PrometheusNotConfigured verifies backward
// compatibility: when prometheusURL is empty, queries return "not configured".
func TestEvidenceGatherer_PrometheusNotConfigured(t *testing.T) {
	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	result := eg.queryPrometheus(context.Background(), "up")
	if result["error"] != "prometheus_url not configured" {
		t.Errorf("expected 'not configured' error, got %v", result)
	}
}

// TestEvidenceGatherer_PrometheusError verifies error handling when
// Prometheus returns a non-200 status code.
func TestEvidenceGatherer_PrometheusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`internal server error`))
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	result := eg.queryPrometheus(context.Background(), "up")
	if _, ok := result["error"]; !ok {
		t.Errorf("expected error for 500 response, got %v", result)
	}
}

// TestEvidenceGatherer_PrometheusErrorResponse verifies handling of a
// Prometheus "error" status (e.g. bad PromQL syntax).
func TestEvidenceGatherer_PrometheusErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{
			"status": "error",
			"error": "unexpected end of input"
		}`))
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	result := eg.queryPrometheus(context.Background(), "up{")
	if result["error"] != "unexpected end of input" {
		t.Errorf("expected error 'unexpected end of input', got %v", result["error"])
	}
}

// TestEvidenceGatherer_ExecuteQueryDispatch verifies that executeQuery routes
// "prometheus" type queries to queryPrometheus, and unknown/Loki/Jaeger
// backends return "not configured" / "unknown query type".
func TestEvidenceGatherer_ExecuteQueryDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	// Prometheus should execute.
	result := eg.executeQuery(context.Background(), map[string]any{
		"type":  "prometheus",
		"query": "up",
	})
	if _, ok := result["error"]; ok {
		t.Errorf("prometheus dispatch should succeed, got error: %v", result["error"])
	}

	// Loki should still be "not configured".
	result = eg.executeQuery(context.Background(), map[string]any{
		"type":  "loki",
		"query": "{} |= \"error\"",
	})
	if result["error"] != "loki_url not configured" {
		t.Errorf("loki should return 'not configured', got %v", result)
	}

	// Jaeger should still be "not configured".
	result = eg.executeQuery(context.Background(), map[string]any{
		"type":  "jaeger",
		"query": "service:api",
	})
	if result["error"] != "jaeger_url not configured" {
		t.Errorf("jaeger should return 'not configured', got %v", result)
	}

	// Unknown type.
	result = eg.executeQuery(context.Background(), map[string]any{
		"type":  "unknown",
		"query": "whatever",
	})
	if _, ok := result["error"]; !ok {
		t.Errorf("unknown type should return error, got %v", result)
	}
}

// TestParseQueryParts verifies the "key:value key:value" parser.
func TestParseQueryParts(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"namespace:default kind:Pod", map[string]string{"namespace": "default", "kind": "Pod"}},
		{"namespace:default kind:Pod labelSelector:app=api", map[string]string{"namespace": "default", "kind": "Pod", "labelSelector": "app=api"}},
		{"", map[string]string{}},
		{"no-colon-here", map[string]string{}},
		{":value-only", map[string]string{}},
		{"key:", map[string]string{"key": ""}},
	}
	for _, c := range cases {
		got := parseQueryParts(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseQueryParts(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("parseQueryParts(%q)[%q] = %q, want %q", c.in, k, got[k], v)
			}
		}
	}
}

// TestEvidenceGatherer_Run_ParallelExecution verifies that queries execute in
// parallel. Uses a mock LLM that returns N evidence_queries; each query hits
// a mock Prometheus that sleeps 200ms before responding. If queries ran
// serially, total time would be N*200ms; in parallel, ~200ms.
func TestEvidenceGatherer_Run_ParallelExecution(t *testing.T) {
	const numQueries = 4
	const perQueryDelay = 200 * time.Millisecond

	// Mock Prometheus: sleeps perQueryDelay, then returns a result.
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(perQueryDelay)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promServer.Close()

	// Build the LLM response: numQueries evidence_queries, each a prometheus
	// query targeting the mock server.
	queries := make([]map[string]any, numQueries)
	for i := range queries {
		queries[i] = map[string]any{
			"hypothesis_id":           fmt.Sprintf("H%d", i+1),
			"supports_or_contradicts": "supports",
			"query":                   map[string]any{"type": "prometheus", "query": "up"},
			"expectation":             "metric should exist",
		}
	}
	llmContent, _ := json.Marshal(map[string]any{"evidence_queries": queries, "rewind": nil})

	llmServer := httptest.NewServer(llmJSONResponseFunc(t, string(llmContent)))
	defer llmServer.Close()

	eg := NewEvidenceGatherer(llm.New(llmServer.URL, "k", "m", "f"), &k8s.Client{})
	eg.SetPrometheusURL(promServer.URL)

	state := &schemas.AgentState{
		Hypotheses: []schemas.Hypothesis{{HID: "H1", Statement: "test", Confidence: 0.5}},
	}

	start := time.Now()
	if err := eg.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	elapsed := time.Since(start)

	// In serial, this would take numQueries*perQueryDelay = 800ms.
	// In parallel, ~perQueryDelay = 200ms (with some overhead).
	// Allow up to 2x perQueryDelay as the upper bound for parallel execution.
	if elapsed > 2*perQueryDelay {
		t.Errorf("queries took %v — expected parallel execution (< %v)", elapsed, 2*perQueryDelay)
	}
	if len(state.Evidence) != numQueries {
		t.Errorf("expected %d evidence entries, got %d", numQueries, len(state.Evidence))
	}
}

// TestEvidenceGatherer_Run_LLMFailureIsNonFatal verifies that an LLM failure
// does NOT fail the whole investigation — the analyst will pick the top
// hypothesis by prior confidence.
func TestEvidenceGatherer_Run_LLMFailureIsNonFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	}))
	defer server.Close()

	router := llm.New(server.URL, "bad-key", "m", "f")
	router.SetRetryConfig(0, 0) // fail fast
	eg := NewEvidenceGatherer(router, &k8s.Client{})

	state := &schemas.AgentState{
		Hypotheses: []schemas.Hypothesis{{HID: "H1", Statement: "test", Confidence: 0.5}},
	}
	if err := eg.Run(context.Background(), state); err != nil {
		t.Fatalf("Run should not return an error for LLM failure: %v", err)
	}
	if state.Evidence != nil {
		t.Errorf("expected nil evidence on LLM failure, got %v", state.Evidence)
	}
	// Trace should record the LLM failure.
	found := false
	for _, e := range state.AgentTrace {
		if e["agent"] == "evidence_gatherer" && e["action"] == "llm_failed_no_evidence" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected llm_failed_no_evidence trace entry, got %v", state.AgentTrace)
	}
}

// TestEvidenceGatherer_Run_Rewind verifies that a rewind response from the
// LLM wipes hypotheses and records a RewindEvent.
func TestEvidenceGatherer_Run_Rewind(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"evidence_queries": []any{},
		"rewind": map[string]any{
			"reason":      "upstream_down",
			"new_context": map[string]any{"dependency": "postgres"},
		},
	})
	server := httptest.NewServer(llmJSONResponseFunc(t, string(content)))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New(server.URL, "k", "m", "f"), &k8s.Client{})
	state := &schemas.AgentState{
		Hypotheses: []schemas.Hypothesis{{HID: "H1", Statement: "test"}},
	}
	if err := eg.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(state.Hypotheses) != 0 {
		t.Errorf("expected hypotheses to be wiped on rewind, got %v", state.Hypotheses)
	}
	if len(state.RewindEvents) != 1 {
		t.Fatalf("expected 1 rewind event, got %d", len(state.RewindEvents))
	}
	if state.RewindEvents[0].RaisedBy != "evidence_gatherer" {
		t.Errorf("raised_by = %q, want evidence_gatherer", state.RewindEvents[0].RaisedBy)
	}
	if state.RewindEvents[0].Reason != "upstream_down" {
		t.Errorf("reason = %q, want upstream_down", state.RewindEvents[0].Reason)
	}
}

// TestEvidenceGatherer_Run_NoHypotheses verifies the early return when there
// are no hypotheses to gather evidence for.
func TestEvidenceGatherer_Run_NoHypotheses(t *testing.T) {
	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	state := &schemas.AgentState{}
	if err := eg.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(state.Evidence) != 0 {
		t.Errorf("expected no evidence, got %d", len(state.Evidence))
	}
}

// TestEvidenceGatherer_RunSingleQuery verifies that a single query produces
// an Evidence with the correct EID, source, and hypothesis link.
func TestEvidenceGatherer_RunSingleQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	q := map[string]any{
		"hypothesis_id":           "H1",
		"supports_or_contradicts": "supports",
		"query":                   map[string]any{"type": "prometheus", "query": "up"},
	}
	ev := eg.runSingleQuery(context.Background(), 0, q)
	if ev.EID != "E1" {
		t.Errorf("EID = %q, want E1", ev.EID)
	}
	if ev.Source != "prometheus" {
		t.Errorf("Source = %q, want prometheus", ev.Source)
	}
	if ev.SupportsHypothesisID == nil || *ev.SupportsHypothesisID != "H1" {
		t.Errorf("SupportsHypothesisID = %v, want H1", ev.SupportsHypothesisID)
	}
	if ev.ContradictsHypothesisID != nil {
		t.Errorf("ContradictsHypothesisID should be nil for supports query")
	}
}

// TestEvidenceGatherer_RunSingleQuery_Contradicts verifies the contradicts
// link is set when the LLM marks the query as contradicting.
func TestEvidenceGatherer_RunSingleQuery_Contradicts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	q := map[string]any{
		"hypothesis_id":           "H2",
		"supports_or_contradicts": "contradicts",
		"query":                   map[string]any{"type": "prometheus", "query": "up"},
	}
	ev := eg.runSingleQuery(context.Background(), 2, q)
	if ev.EID != "E3" {
		t.Errorf("EID = %q, want E3", ev.EID)
	}
	if ev.ContradictsHypothesisID == nil || *ev.ContradictsHypothesisID != "H2" {
		t.Errorf("ContradictsHypothesisID = %v, want H2", ev.ContradictsHypothesisID)
	}
	if ev.SupportsHypothesisID != nil {
		t.Errorf("SupportsHypothesisID should be nil for contradicts query")
	}
}

// TestEvidenceGatherer_Run_ParallelQueryCount verifies that the evidence
// gatherer issues the right number of queries in parallel by counting server
// hits.
func TestEvidenceGatherer_Run_ParallelQueryCount(t *testing.T) {
	var promHits int32
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&promHits, 1)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promServer.Close()

	queries := []map[string]any{
		{"hypothesis_id": "H1", "supports_or_contradicts": "supports", "query": map[string]any{"type": "prometheus", "query": "up"}},
		{"hypothesis_id": "H2", "supports_or_contradicts": "contradicts", "query": map[string]any{"type": "prometheus", "query": "rate(http_requests_total[5m])"}},
		{"hypothesis_id": "H3", "supports_or_contradicts": "supports", "query": map[string]any{"type": "prometheus", "query": "probe_success"}},
	}
	llmContent, _ := json.Marshal(map[string]any{"evidence_queries": queries, "rewind": nil})
	llmServer := httptest.NewServer(llmJSONResponseFunc(t, string(llmContent)))
	defer llmServer.Close()

	eg := NewEvidenceGatherer(llm.New(llmServer.URL, "k", "m", "f"), &k8s.Client{})
	eg.SetPrometheusURL(promServer.URL)

	state := &schemas.AgentState{
		Hypotheses: []schemas.Hypothesis{{HID: "H1", Statement: "test"}},
	}
	if err := eg.Run(context.Background(), state); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if got := atomic.LoadInt32(&promHits); got != 3 {
		t.Errorf("expected 3 prometheus hits, got %d", got)
	}
}

// TestEvidenceGatherer_BuildUserMessage verifies that the user message
// includes the service, hypotheses, and cluster context.
func TestEvidenceGatherer_BuildUserMessage(t *testing.T) {
	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "api",
			Namespace: "default",
			Summary:   "elevated 5xx rate",
		},
		Hypotheses: []schemas.Hypothesis{
			{HID: "H1", Statement: "DB connection pool exhausted", Confidence: 0.7, SupportingEvidence: []string{"Prometheus shows 100 conns"}},
			{HID: "H2", Statement: "Bad deploy", Confidence: 0.4},
		},
	}
	msg := eg.buildUserMessage(state)
	if !strings.Contains(msg, "api") {
		t.Errorf("expected service name in message")
	}
	if !strings.Contains(msg, "DB connection pool exhausted") {
		t.Errorf("expected H1 statement in message")
	}
	if !strings.Contains(msg, "Bad deploy") {
		t.Errorf("expected H2 statement in message")
	}
	if !strings.Contains(msg, "elevated 5xx rate") {
		t.Errorf("expected cluster summary in message")
	}
	if !strings.Contains(msg, "Prometheus shows 100 conns") {
		t.Errorf("expected supporting evidence in message")
	}
}

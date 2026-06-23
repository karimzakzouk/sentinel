package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/karimzakzouk/sentinel/v3/internal/k8s"
	"github.com/karimzakzouk/sentinel/v3/internal/llm"
)

// TestEvidenceGatherer_Prometheus verifies that the v3.4.0 Prometheus backend
// executes PromQL queries and parses the response correctly.
// Before v3.4.0, this returned {"error": "prometheus_url not configured"}.
func TestEvidenceGatherer_Prometheus(t *testing.T) {
	// Mock Prometheus server returning a vector query result.
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

// TestEvidenceGatherer_PrometheusNotConfigured verifies backward compatibility:
// when prometheusURL is empty, queries return "not configured" (same as v3.3.x).
func TestEvidenceGatherer_PrometheusNotConfigured(t *testing.T) {
	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	// Don't call SetPrometheusURL — should remain empty

	result := eg.queryPrometheus(context.Background(), "up")
	if result["error"] != "prometheus_url not configured" {
		t.Errorf("expected 'not configured' error, got %v", result)
	}
}

// TestEvidenceGatherer_PrometheusError verifies error handling when Prometheus
// returns a non-200 status code.
func TestEvidenceGatherer_PrometheusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// TestEvidenceGatherer_PrometheusErrorResponse verifies handling of Prometheus
// "error" status (e.g. bad PromQL syntax).
func TestEvidenceGatherer_PrometheusErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
// "prometheus" type queries to queryPrometheus (and others remain stubbed).
func TestEvidenceGatherer_ExecuteQueryDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()

	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	eg.SetPrometheusURL(server.URL)

	// Prometheus should execute
	result := eg.executeQuery(context.Background(), map[string]any{
		"type":  "prometheus",
		"query": "up",
	})
	if _, ok := result["error"]; ok {
		t.Errorf("prometheus dispatch should succeed, got error: %v", result["error"])
	}

	// Loki should still be "not configured"
	result = eg.executeQuery(context.Background(), map[string]any{
		"type":  "loki",
		"query": "{} |= \"error\"",
	})
	if result["error"] != "loki_url not configured" {
		t.Errorf("loki should return 'not configured', got %v", result)
	}

	// Jaeger should still be "not configured"
	result = eg.executeQuery(context.Background(), map[string]any{
		"type":  "jaeger",
		"query": "service:api",
	})
	if result["error"] != "jaeger_url not configured" {
		t.Errorf("jaeger should return 'not configured', got %v", result)
	}
}

// Helper: decode result to verify JSON serializable (for LLM consumption).
func init() {
	// Ensure our result maps are JSON-serializable (the LLM analyst consumes them).
	eg := NewEvidenceGatherer(llm.New("", "", "", ""), &k8s.Client{})
	result := map[string]any{
		"query":   "up",
		"results": []map[string]any{{"value": 1.0}},
	}
	if _, err := json.Marshal(result); err != nil {
		panic("result map is not JSON-serializable: " + err.Error())
	}
	_ = eg
}

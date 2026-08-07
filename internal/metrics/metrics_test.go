package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetMetrics clears the global metricSet so tests start from a known
// baseline. Tests in this package share the package-level var; without
// reset, ordering between tests would matter.
func resetMetrics(t *testing.T) {
	t.Helper()
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.incidentsTotal = make(map[string]float64)
	metrics.sandboxVerifications = make(map[string]float64)
	metrics.llmCallsTotal = make(map[string]float64)
	metrics.llmInputTokensTotal = make(map[string]int)
	metrics.llmOutputTokensTotal = make(map[string]int)
	metrics.llmTotalCostUSD = make(map[string]float64)
	metrics.sandboxLevelFailures = make(map[string]int)
	metrics.sandboxRetryCount = make(map[string]int)
	metrics.investigationBuckets = make(map[string]float64)
	metrics.llmCallBuckets = make(map[string]float64)
	metrics.sandboxBuckets = make(map[string]float64)
	metrics.investigationsStarted = 0
	metrics.prsOpened = 0
	metrics.investigationDurationCount = 0
	metrics.investigationDurationSum = 0
	metrics.llmCallDurationCount = 0
	metrics.llmCallDurationSum = 0
	metrics.sandboxDurationCount = 0
	metrics.sandboxDurationSum = 0
	metrics.activeIncidents = 0
	metrics.llmInFlight = 0
}

// TestMetrics_Render verifies that render() produces a valid Prometheus
// text exposition with the expected metric names + label values.
func TestMetrics_Render(t *testing.T) {
	resetMetrics(t)
	RecordInvestigationStart()
	RecordInvestigationComplete("verified", 5*time.Second)
	RecordInvestigationStart()
	RecordInvestigationComplete("failed", 2*time.Second)
	RecordPROpened()
	RecordSandboxVerification(true, 30*time.Second)
	RecordSandboxVerification(false, 10*time.Second)
	RecordLLMCall("gpt-4", "success", 1500*time.Millisecond)
	RecordLLMCall("gpt-4", "rate_limited", 100*time.Millisecond)

	output := render()

	expectedMetrics := []string{
		"# HELP sentinel_incidents_total",
		"# TYPE sentinel_incidents_total counter",
		"sentinel_incidents_total{status=\"failed\"} 1",
		"sentinel_incidents_total{status=\"verified\"} 1",
		"# HELP sentinel_investigations_started_total",
		"sentinel_investigations_started_total 2",
		"# HELP sentinel_prs_opened_total",
		"sentinel_prs_opened_total 1",
		"# HELP sentinel_sandbox_verifications_total",
		"sentinel_sandbox_verifications_total{verified=\"false\"} 1",
		"sentinel_sandbox_verifications_total{verified=\"true\"} 1",
		"# HELP sentinel_llm_calls_total",
		"sentinel_llm_calls_total{model=\"gpt-4\",status=\"success\"} 1",
		"sentinel_llm_calls_total{model=\"gpt-4\",status=\"rate_limited\"} 1",
		"# HELP sentinel_active_incidents",
		"# TYPE sentinel_active_incidents gauge",
		"# HELP sentinel_llm_in_flight_calls",
	}
	for _, expected := range expectedMetrics {
		if !strings.Contains(output, expected) {
			t.Errorf("expected output to contain %q\noutput:\n%s", expected, output)
		}
	}
}

// TestMetrics_Handler verifies the HTTP handler serves the metrics in
// Prometheus text format with the correct Content-Type.
func TestMetrics_Handler(t *testing.T) {
	resetMetrics(t)
	RecordInvestigationStart()
	RecordInvestigationComplete("verified", 1*time.Second)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "sentinel_incidents_total") {
		t.Errorf("body should contain sentinel_incidents_total, got:\n%s", body)
	}
}

// TestMetrics_InvestigationGauge verifies that active_incidents returns to
// 0 after balanced start/complete pairs.
func TestMetrics_InvestigationGauge(t *testing.T) {
	resetMetrics(t)
	RecordInvestigationStart()
	RecordInvestigationStart()
	RecordInvestigationComplete("verified", 1*time.Second)
	RecordInvestigationComplete("verified", 1*time.Second)

	output := render()
	if !strings.Contains(output, "sentinel_active_incidents 0") {
		t.Errorf("active_incidents should be 0 after balanced start/complete; output:\n%s", output)
	}
}

// TestMetrics_LLMCallStatusLabels verifies all 4 LLM status labels work.
func TestMetrics_LLMCallStatusLabels(t *testing.T) {
	resetMetrics(t)
	RecordLLMCall("m1", "success", 1*time.Second)
	RecordLLMCall("m1", "error", 2*time.Second)
	RecordLLMCall("m1", "rate_limited", 500*time.Millisecond)
	RecordLLMCall("m1", "client_error", 100*time.Millisecond)

	output := render()
	for _, status := range []string{"success", "error", "rate_limited", "client_error"} {
		expected := "sentinel_llm_calls_total{model=\"m1\",status=\"" + status + "\"} 1"
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output", expected)
		}
	}
}

// TestMetrics_PrometheusFormat verifies the rendered output conforms to the
// Prometheus text exposition format: every metric has a HELP and TYPE line
// preceding it, and the value is a valid number.
func TestMetrics_PrometheusFormat(t *testing.T) {
	resetMetrics(t)
	RecordInvestigationStart()
	RecordInvestigationComplete("verified", 1*time.Second)
	RecordLLMCall("gpt-4o", "success", 100*time.Millisecond)
	RecordLLMTokens("gpt-4o", 100, 50)
	RecordSandboxLevelFailure(2)
	RecordSandboxRetry(1)
	LLMCallStart()
	LLMCallEnd()

	output := render()
	lines := strings.Split(output, "\n")

	// Walk the lines and verify every metric line is preceded by a # HELP
	// and # TYPE for its name. Comments and blank lines are skipped.
	seenHelp := map[string]bool{}
	seenType := map[string]bool{}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "# HELP ") {
			name := strings.TrimPrefix(line, "# HELP ")
			if name != line {
				// Extract just the metric name (first token).
				if sp := strings.IndexByte(name, ' '); sp >= 0 {
					name = name[:sp]
				}
				seenHelp[name] = true
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			rest := strings.TrimPrefix(line, "# TYPE ")
			sp := strings.IndexByte(rest, ' ')
			if sp >= 0 {
				seenType[rest[:sp]] = true
			}
			continue
		}
		// Metric line: extract the name (up to '{' or ' ').
		name := line
		if br := strings.IndexAny(line, "{ "); br >= 0 {
			name = line[:br]
		}
		if name == "" {
			continue
		}
		if !seenHelp[name] {
			t.Errorf("metric %q missing # HELP line before its value", name)
		}
		if !seenType[name] {
			t.Errorf("metric %q missing # TYPE line before its value", name)
		}
	}

	// Spot-check a few specific Prometheus format invariants.
	if !strings.Contains(output, "# HELP sentinel_llm_cost_usd_total") {
		t.Error("missing sentinel_llm_cost_usd_total HELP line")
	}
	if !strings.Contains(output, "sentinel_sandbox_level_failures_total{level=\"2\"} 1") {
		t.Error("missing sentinel_sandbox_level_failures_total counter for level 2")
	}
	if !strings.Contains(output, "sentinel_sandbox_retry_count{attempt=\"1\"} 1") {
		t.Error("missing sentinel_sandbox_retry_count counter for attempt 1")
	}
}

// TestSplitModelStatus verifies the "model:status" key splitting.
func TestSplitModelStatus(t *testing.T) {
	cases := []struct {
		in     string
		model  string
		status string
	}{
		{"gpt-4:success", "gpt-4", "success"},
		{"gpt-4-1106-preview:rate_limited", "gpt-4-1106-preview", "rate_limited"},
		{"no-status", "no-status", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		model, status := splitModelStatus(c.in)
		if model != c.model || status != c.status {
			t.Errorf("splitModelStatus(%q) = (%q, %q), want (%q, %q)",
				c.in, model, status, c.model, c.status)
		}
	}
}

// TestMetrics_LLMCostEstimate verifies the per-model cost estimate for one
// known model and one unknown model (fallback path).
func TestMetrics_LLMCostEstimate(t *testing.T) {
	resetMetrics(t)
	// gpt-4o: $2.50/1M in, $10/1M out. 1M in + 1M out = $12.50.
	cost := estimateCostUSD("gpt-4o", 1_000_000, 1_000_000)
	if cost < 12.49 || cost > 12.51 {
		t.Errorf("gpt-4o cost = %v, want ~12.50", cost)
	}

	// Unknown model falls back to gpt-4o-mini pricing ($0.15/$0.60 per 1M).
	unknown := estimateCostUSD("some-new-model", 1_000_000, 1_000_000)
	if unknown < 0.74 || unknown > 0.76 {
		t.Errorf("unknown model fallback cost = %v, want ~0.75", unknown)
	}
}

// TestMetrics_InFlightGauge verifies LLMCallStart/End maintain the in-flight
// gauge correctly under balanced + unbalanced use.
func TestMetrics_InFlightGauge(t *testing.T) {
	resetMetrics(t)
	LLMCallStart()
	LLMCallStart()
	LLMCallStart()
	LLMCallEnd()
	// 2 in-flight now.
	output := render()
	if !strings.Contains(output, "sentinel_llm_in_flight_calls 2") {
		t.Errorf("expected in_flight=2 after 3 start + 1 end, output:\n%s", output)
	}
	LLMCallEnd()
	LLMCallEnd()
	output = render()
	if !strings.Contains(output, "sentinel_llm_in_flight_calls 0") {
		t.Errorf("expected in_flight=0 after balanced, output:\n%s", output)
	}
}

// TestMetrics_HistogramBuckets verifies the cumulative bucket counts are
// emitted with the +Inf bucket.
func TestMetrics_HistogramBuckets(t *testing.T) {
	resetMetrics(t)
	// 3s investigation → falls into 30, 60, 120, 180, 300, 600, 1200, 1800 + +Inf.
	RecordInvestigationComplete("verified", 3*time.Second)
	output := render()
	if !strings.Contains(output, `sentinel_investigation_duration_seconds_bucket{le="30"} 1`) {
		t.Errorf("30s bucket should be 1; output:\n%s", output)
	}
	if !strings.Contains(output, `sentinel_investigation_duration_seconds_bucket{le="+Inf"} 1`) {
		t.Errorf("+Inf bucket should be 1; output:\n%s", output)
	}
}

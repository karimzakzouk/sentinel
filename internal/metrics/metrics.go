// Package metrics provides Prometheus instrumentation for Sentinel.
//
// v3.4.0: Replaces the stub /metrics endpoint that returned a hardcoded
// "sentinel_incidents_total 0". Now exports real counters/histograms/gauges
// so Sentinel's own health is observable in Prometheus/Grafana.
//
// v3.8.4: All accessors are now mutex-guarded. The package-level metricSet
// is mutated from multiple goroutines (3 concurrent investigations + N LLM
// calls + SSE broadcaster), so unsynchronized map writes triggered the race
// detector under real load. A single sync.Mutex protects every field; the
// critical sections are tiny (a map increment or a float64 add) so contention
// is negligible for a single-pod system.
//
// Metrics exported:
//
//	Counters (monotonically increasing):
//	  sentinel_incidents_total{status="verified|failed|escalated"}
//	  sentinel_investigations_started_total
//	  sentinel_prs_opened_total
//	  sentinel_sandbox_verifications_total{verified="true|false"}
//	  sentinel_llm_calls_total{model, status="success|error|rate_limited"}
//
//	Histograms (duration distributions):
//	  sentinel_investigation_duration_seconds{status}
//	  sentinel_llm_call_duration_seconds{model}
//	  sentinel_sandbox_duration_seconds
//
//	Gauges (point-in-time values):
//	  sentinel_active_incidents
//	  sentinel_llm_in_flight_calls
//
// Usage:
//
//	metrics.RecordInvestigationStart()
//	defer metrics.RecordInvestigationComplete("verified", time.Since(start))
package metrics

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// v3.4.0: All metrics use the prometheus client_golang library... but we don't
// want to add a new dependency (the project prides itself on minimal deps).
// Instead we implement a minimal Prometheus exposition format by hand — it's
// just text, and we control all the metric definitions.
//
// This keeps the binary small and avoids the prometheus/client_golang dependency
// tree (which is ~2MB compiled). The tradeoff is we lose client-side histogram
// bucketing — but for a single-pod system, server-side aggregation is fine.

// metricSet holds all our metric values. Single source of truth for /metrics.
// v3.8.4: mu guards every field. All Record* / *Start / *End functions take
// the lock for the duration of their read-modify-write. The render() function
// takes a read lock so it sees a consistent snapshot.
type metricSet struct {
	mu sync.Mutex

	// Counters
	incidentsTotal        map[string]float64 // by status
	investigationsStarted float64
	prsOpened             float64
	sandboxVerifications  map[string]float64 // by verified
	llmCallsTotal         map[string]float64 // by "model:status"

	// Histograms (simplified — we track count + sum, server computes avg)
	investigationDurationCount float64
	investigationDurationSum   float64
	llmCallDurationCount       float64
	llmCallDurationSum         float64
	sandboxDurationCount       float64
	sandboxDurationSum         float64

	// Gauges
	activeIncidents float64
	llmInFlight     float64
}

var metrics = &metricSet{
	incidentsTotal:       make(map[string]float64),
	sandboxVerifications: make(map[string]float64),
	llmCallsTotal:        make(map[string]float64),
}

// RecordInvestigationStart increments the investigations_started counter
// and bumps the active_incidents gauge.
func RecordInvestigationStart() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.investigationsStarted++
	metrics.activeIncidents++
}

// RecordInvestigationComplete records the investigation duration and
// increments the incidents_total counter by status. Decrements active_incidents.
func RecordInvestigationComplete(status string, duration time.Duration) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.incidentsTotal[status]++
	metrics.activeIncidents--
	metrics.investigationDurationCount++
	metrics.investigationDurationSum += duration.Seconds()
}

// RecordPROpened increments the prs_opened counter.
func RecordPROpened() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.prsOpened++
}

// RecordSandboxVerification records a sandbox verification result + duration.
func RecordSandboxVerification(verified bool, duration time.Duration) {
	key := "false"
	if verified {
		key = "true"
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.sandboxVerifications[key]++
	metrics.sandboxDurationCount++
	metrics.sandboxDurationSum += duration.Seconds()
}

// RecordLLMCall records an LLM API call: model, status (success/error/rate_limited), duration.
func RecordLLMCall(model, status string, duration time.Duration) {
	key := model + ":" + status
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.llmCallsTotal[key]++
	metrics.llmCallDurationCount++
	metrics.llmCallDurationSum += duration.Seconds()
}

// LLMCallStart increments the in-flight gauge. Call LLMCallEnd when done.
func LLMCallStart() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.llmInFlight++
}

// LLMCallEnd decrements the in-flight gauge.
func LLMCallEnd() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.llmInFlight--
}

// Handler returns an http.HandlerFunc that writes Prometheus-format metrics.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write([]byte(render()))
	}
}

// render produces the Prometheus text exposition format.
// We emit HELP/TYPE lines for each metric, then the values.
func render() string {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	// Snapshot the map values under the lock so the builder doesn't need to
	// touch the shared state. Maps can't be safely read while another goroutine
	// is writing even if we hold the lock here, because the builder runs after
	// we return — so copy now.
	incidentsTotal := mapClone(metrics.incidentsTotal)
	sandboxVerifications := mapClone(metrics.sandboxVerifications)
	llmCallsTotal := mapClone(metrics.llmCallsTotal)

	investigationsStarted := metrics.investigationsStarted
	prsOpened := metrics.prsOpened
	investigationDurationCount := metrics.investigationDurationCount
	investigationDurationSum := metrics.investigationDurationSum
	llmCallDurationCount := metrics.llmCallDurationCount
	llmCallDurationSum := metrics.llmCallDurationSum
	sandboxDurationCount := metrics.sandboxDurationCount
	sandboxDurationSum := metrics.sandboxDurationSum
	activeIncidents := metrics.activeIncidents
	llmInFlight := metrics.llmInFlight

	var b builder

	// Counters
	b.help("sentinel_incidents_total", "Total incidents processed by final status")
	b.typ("sentinel_incidents_total", "counter")
	for _, status := range sortedKeys(incidentsTotal) {
		b.metric("sentinel_incidents_total", incidentsTotal[status], "status", status)
	}

	b.help("sentinel_investigations_started_total", "Total investigations started")
	b.typ("sentinel_investigations_started_total", "counter")
	b.metric("sentinel_investigations_started_total", investigationsStarted)

	b.help("sentinel_prs_opened_total", "Total PRs opened by Sentinel")
	b.typ("sentinel_prs_opened_total", "counter")
	b.metric("sentinel_prs_opened_total", prsOpened)

	b.help("sentinel_sandbox_verifications_total", "Total sandbox verifications by result")
	b.typ("sentinel_sandbox_verifications_total", "counter")
	for _, verified := range sortedKeys(sandboxVerifications) {
		b.metric("sentinel_sandbox_verifications_total", sandboxVerifications[verified], "verified", verified)
	}

	b.help("sentinel_llm_calls_total", "Total LLM API calls by model and status")
	b.typ("sentinel_llm_calls_total", "counter")
	for _, key := range sortedKeys(llmCallsTotal) {
		model, status := splitModelStatus(key)
		b.metric("sentinel_llm_calls_total", llmCallsTotal[key], "model", model, "status", status)
	}

	// Histograms (simplified — count + sum, no buckets)
	b.help("sentinel_investigation_duration_seconds_count", "Investigation duration count")
	b.typ("sentinel_investigation_duration_seconds_count", "counter")
	b.metric("sentinel_investigation_duration_seconds_count", investigationDurationCount)

	b.help("sentinel_investigation_duration_seconds_sum", "Investigation duration sum (seconds)")
	b.typ("sentinel_investigation_duration_seconds_sum", "counter")
	b.metric("sentinel_investigation_duration_seconds_sum", investigationDurationSum)

	b.help("sentinel_llm_call_duration_seconds_count", "LLM call duration count")
	b.typ("sentinel_llm_call_duration_seconds_count", "counter")
	b.metric("sentinel_llm_call_duration_seconds_count", llmCallDurationCount)

	b.help("sentinel_llm_call_duration_seconds_sum", "LLM call duration sum (seconds)")
	b.typ("sentinel_llm_call_duration_seconds_sum", "counter")
	b.metric("sentinel_llm_call_duration_seconds_sum", llmCallDurationSum)

	b.help("sentinel_sandbox_duration_seconds_count", "Sandbox verification duration count")
	b.typ("sentinel_sandbox_duration_seconds_count", "counter")
	b.metric("sentinel_sandbox_duration_seconds_count", sandboxDurationCount)

	b.help("sentinel_sandbox_duration_seconds_sum", "Sandbox verification duration sum (seconds)")
	b.typ("sentinel_sandbox_duration_seconds_sum", "counter")
	b.metric("sentinel_sandbox_duration_seconds_sum", sandboxDurationSum)

	// Gauges
	b.help("sentinel_active_incidents", "Currently active investigations")
	b.typ("sentinel_active_incidents", "gauge")
	b.metric("sentinel_active_incidents", activeIncidents)

	b.help("sentinel_llm_in_flight_calls", "Currently in-flight LLM API calls")
	b.typ("sentinel_llm_in_flight_calls", "gauge")
	b.metric("sentinel_llm_in_flight_calls", llmInFlight)

	return b.String()
}

// mapClone returns a shallow copy of m. Used by render() to snapshot map
// state under the lock so the builder can iterate without holding the lock.
func mapClone(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// splitModelStatus splits "model:status" back into two parts.
func splitModelStatus(key string) (model, status string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// sortedKeys returns the keys of a map in sorted order (deterministic output).
// We sort to make test assertions stable — Prometheus doesn't require sorting
// but it makes /metrics output reproducible.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// builder is a string builder for Prometheus text format.
type builder struct {
	buf []byte
}

func (b *builder) help(name, text string) {
	b.buf = append(b.buf, "# HELP "...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, ' ')
	b.buf = append(b.buf, text...)
	b.buf = append(b.buf, '\n')
}

func (b *builder) typ(name, typ string) {
	b.buf = append(b.buf, "# TYPE "...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, ' ')
	b.buf = append(b.buf, typ...)
	b.buf = append(b.buf, '\n')
}

func (b *builder) metric(name string, value float64, labels ...string) {
	b.buf = append(b.buf, name...)
	if len(labels) > 0 {
		b.buf = append(b.buf, '{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				b.buf = append(b.buf, ',')
			}
			b.buf = append(b.buf, labels[i]...)
			b.buf = append(b.buf, '=')
			b.buf = append(b.buf, '"')
			b.buf = append(b.buf, labels[i+1]...)
			b.buf = append(b.buf, '"')
		}
		b.buf = append(b.buf, '}')
	}
	b.buf = append(b.buf, ' ')
	b.buf = strconv.AppendFloat(b.buf, value, 'g', -1, 64)
	b.buf = append(b.buf, '\n')
}

func (b *builder) String() string {
	return string(b.buf)
}

// _ keeps strings imported (used in splitModelStatus).
var _ = strings.Contains

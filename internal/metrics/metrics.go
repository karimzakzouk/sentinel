// Package metrics provides Prometheus instrumentation for Sentinel.
// Exports real counters/histograms/gauges so Sentinel's own health is
// observable in Prometheus/Grafana. The metricSet is mutated from multiple
// goroutines (3 concurrent investigations + N LLM calls + SSE broadcaster),
// so unsynchronized map writes triggered the race detector under real load.
// A single sync.Mutex protects every field; the critical sections are tiny
// (a map increment or a float64 add) so contention is negligible for a
// single-pod system.
// Metrics exported:
//      Counters (monotonically increasing):
//        sentinel_incidents_total{status="verified|failed|escalated"}
//        sentinel_investigations_started_total
//        sentinel_prs_opened_total
//        sentinel_sandbox_verifications_total{verified="true|false"}
//        sentinel_llm_calls_total{model, status="success|error|rate_limited"}
//      Histograms (duration distributions):
//        sentinel_investigation_duration_seconds{status}
//        sentinel_llm_call_duration_seconds{model}
//        sentinel_sandbox_duration_seconds
//      Gauges (point-in-time values):
//        sentinel_active_incidents
//        sentinel_llm_in_flight_calls
// Usage:
//      metrics.RecordInvestigationStart()
//      defer metrics.RecordInvestigationComplete("verified", time.Since(start))
package metrics

import (
        "fmt"
        "net/http"
        "sort"
        "strconv"
        "sync"
        "time"
)

// want to add a new dependency (the project prides itself on minimal deps).
// Instead we implement a minimal Prometheus exposition format by hand — it's
// just text, and we control all the metric definitions.
// This keeps the binary small and avoids the prometheus/client_golang dependency
// tree (which is ~2MB compiled). The tradeoff is we lose client-side histogram
// bucketing — but for a single-pod system, server-side aggregation is fine.
// metricSet holds all our metric values. Single source of truth for /metrics.
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

        llmInputTokensTotal  map[string]int // by model
        llmOutputTokensTotal map[string]int // by model
        llmTotalCostUSD      map[string]float64 // by model

        sandboxLevelFailures map[string]int // by level: "1", "2", "3"
        sandboxRetryCount    map[string]int // by attempt index

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

        // Each bucket counts investigations/LLM calls/sandbox runs that took
        // <= the bucket's upper bound. Prometheus can compute quantiles from
        // these via histogram_quantile().

        investigationBuckets map[string]float64 // key: bucket upper bound (e.g. "30", "60", "120", "300", "+Inf")
        llmCallBuckets       map[string]float64
        sandboxBuckets       map[string]float64
}

// Standard bucket boundaries (seconds).


var (
        investBuckets = []float64{30, 60, 120, 180, 300, 600, 1200, 1800}
        llmBuckets    = []float64{1, 5, 10, 30, 60, 120, 300}
        sandboxBuckets= []float64{30, 60, 120, 240, 360, 600}
)

var metrics = &metricSet{
        incidentsTotal:       make(map[string]float64),
        sandboxVerifications: make(map[string]float64),
        llmCallsTotal:        make(map[string]float64),
        llmInputTokensTotal:  make(map[string]int),
        llmOutputTokensTotal: make(map[string]int),
        llmTotalCostUSD:      make(map[string]float64),
        sandboxLevelFailures: make(map[string]int),
        sandboxRetryCount:    make(map[string]int),
        investigationBuckets: make(map[string]float64),
        llmCallBuckets:       make(map[string]float64),
        sandboxBuckets:       make(map[string]float64),
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

        bumpBucket(metrics.investigationBuckets, investBuckets, duration.Seconds())
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

        bumpBucket(metrics.sandboxBuckets, sandboxBuckets, duration.Seconds())
}

// bumpBucket increments all buckets whose upper bound is >= the value.
// This produces a cumulative histogram (each bucket includes counts from
// all smaller buckets, as Prometheus expects).

func bumpBucket(buckets map[string]float64, bounds []float64, value float64) {
        for _, b := range bounds {
                if value <= b {
                        buckets[fmt.Sprintf("%g", b)]++
                }
        }
        buckets["+Inf"]++
}

// RecordLLMCall records an LLM API call: model, status (success/error/rate_limited), duration.

func RecordLLMCall(model, status string, duration time.Duration) {
        key := model + ":" + status
        metrics.mu.Lock()
        defer metrics.mu.Unlock()
        metrics.llmCallsTotal[key]++
        metrics.llmCallDurationCount++
        metrics.llmCallDurationSum += duration.Seconds()

        bumpBucket(metrics.llmCallBuckets, llmBuckets, duration.Seconds())
}

// RecordLLMTokens records token usage + estimated cost for an LLM call.
// Pricing is approximate (per 1M tokens, USD) and based on public list prices as of 2024-12.
// Real production cost tracking should use the provider's billing API.

func RecordLLMTokens(model string, inputTokens, outputTokens int) {
        metrics.mu.Lock()
        defer metrics.mu.Unlock()
        metrics.llmInputTokensTotal[model] += inputTokens
        metrics.llmOutputTokensTotal[model] += outputTokens
        metrics.llmTotalCostUSD[model] += estimateCostUSD(model, inputTokens, outputTokens)
}

// estimateCostUSD returns an approximate cost in USD for a single LLM call.
// Prices are per 1M tokens, from public provider pricing pages.
// Returns 0 for unknown models (operator should add their pricing here).

func estimateCostUSD(model string, inputTokens, outputTokens int) float64 {
        type pricing struct{ in, out float64 } // USD per 1M tokens
        prices := map[string]pricing{
                // OpenAI

                "gpt-4o":              {2.50, 10.00},
                "gpt-4o-mini":         {0.15, 0.60},
                "gpt-4-turbo":         {10.00, 30.00},
                "o1":                  {15.00, 60.00},
                "o1-mini":             {3.00, 12.00},
                "o3-mini":             {3.00, 12.00},
                // Anthropic

                "claude-3-5-sonnet-latest":  {3.00, 15.00},
                "claude-3-5-haiku-latest":   {0.80, 4.00},
                "claude-3-opus-latest":      {15.00, 75.00},
                // Google

                "gemini-1.5-pro":      {1.25, 5.00},
                "gemini-1.5-flash":    {0.075, 0.30},
                "gemini-2.0-flash":    {0.10, 0.40},
                // z.ai

                "glm-4.5":             {2.00, 8.00},
                "glm-4-flash":         {0.50, 0.50},
                "glm-4-air":           {0.50, 0.50},
        }
        p, ok := prices[model]
        if !ok {
                // Fall back to a conservative estimate (use gpt-4o-mini pricing).

                p = pricing{0.15, 0.60}
        }
        return float64(inputTokens)/1_000_000*p.in + float64(outputTokens)/1_000_000*p.out
}

// RecordSandboxLevelFailure records which sandbox verification level failed.


func RecordSandboxLevelFailure(level int) {
        metrics.mu.Lock()
        defer metrics.mu.Unlock()
        metrics.sandboxLevelFailures[fmt.Sprintf("%d", level)]++
}

// RecordSandboxRetry records which sandbox retry attempt was used.


func RecordSandboxRetry(attempt int) {
        metrics.mu.Lock()
        defer metrics.mu.Unlock()
        metrics.sandboxRetryCount[fmt.Sprintf("%d", attempt)]++
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

        llmInputTokensTotal := mapCloneInt(metrics.llmInputTokensTotal)
        llmOutputTokensTotal := mapCloneInt(metrics.llmOutputTokensTotal)
        llmTotalCostUSD := mapCloneFloat(metrics.llmTotalCostUSD)
        sandboxLevelFailures := mapCloneInt(metrics.sandboxLevelFailures)
        sandboxRetryCount := mapCloneInt(metrics.sandboxRetryCount)

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

        b.help("sentinel_llm_input_tokens_total", "Total LLM input (prompt) tokens by model")
        b.typ("sentinel_llm_input_tokens_total", "counter")
        for _, model := range sortedKeysInt(llmInputTokensTotal) {
                b.metric("sentinel_llm_input_tokens_total", float64(llmInputTokensTotal[model]), "model", model)
        }

        b.help("sentinel_llm_output_tokens_total", "Total LLM output (completion) tokens by model")
        b.typ("sentinel_llm_output_tokens_total", "counter")
        for _, model := range sortedKeysInt(llmOutputTokensTotal) {
                b.metric("sentinel_llm_output_tokens_total", float64(llmOutputTokensTotal[model]), "model", model)
        }

        b.help("sentinel_llm_cost_usd_total", "Estimated LLM cost in USD by model (approximate pricing)")
        b.typ("sentinel_llm_cost_usd_total", "counter")
        for _, model := range sortedKeysFloat(llmTotalCostUSD) {
                b.metric("sentinel_llm_cost_usd_total", llmTotalCostUSD[model], "model", model)
        }

        b.help("sentinel_sandbox_level_failures_total", "Sandbox verification failures by level (1=pod ready, 2=endpoints, 3=original error)")
        b.typ("sentinel_sandbox_level_failures_total", "counter")
        for _, level := range sortedKeysInt(sandboxLevelFailures) {
                b.metric("sentinel_sandbox_level_failures_total", float64(sandboxLevelFailures[level]), "level", level)
        }

        b.help("sentinel_sandbox_retry_count", "Sandbox retry attempt distribution")
        b.typ("sentinel_sandbox_retry_count", "counter")
        for _, attempt := range sortedKeysInt(sandboxRetryCount) {
                b.metric("sentinel_sandbox_retry_count", float64(sandboxRetryCount[attempt]), "attempt", attempt)
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

        // histogram_quantile() in Prometheus.

        b.help("sentinel_investigation_duration_seconds_bucket", "Investigation duration histogram (cumulative buckets)")
        b.typ("sentinel_investigation_duration_seconds_bucket", "histogram")
        for _, bound := range investBuckets {
                b.metric("sentinel_investigation_duration_seconds_bucket", metrics.investigationBuckets[fmt.Sprintf("%g", bound)], "le", fmt.Sprintf("%g", bound))
        }
        b.metric("sentinel_investigation_duration_seconds_bucket", metrics.investigationBuckets["+Inf"], "le", "+Inf")

        b.help("sentinel_llm_call_duration_seconds_bucket", "LLM call duration histogram (cumulative buckets)")
        b.typ("sentinel_llm_call_duration_seconds_bucket", "histogram")
        for _, bound := range llmBuckets {
                b.metric("sentinel_llm_call_duration_seconds_bucket", metrics.llmCallBuckets[fmt.Sprintf("%g", bound)], "le", fmt.Sprintf("%g", bound))
        }
        b.metric("sentinel_llm_call_duration_seconds_bucket", metrics.llmCallBuckets["+Inf"], "le", "+Inf")

        b.help("sentinel_sandbox_duration_seconds_bucket", "Sandbox duration histogram (cumulative buckets)")
        b.typ("sentinel_sandbox_duration_seconds_bucket", "histogram")
        for _, bound := range sandboxBuckets {
                b.metric("sentinel_sandbox_duration_seconds_bucket", metrics.sandboxBuckets[fmt.Sprintf("%g", bound)], "le", fmt.Sprintf("%g", bound))
        }
        b.metric("sentinel_sandbox_duration_seconds_bucket", metrics.sandboxBuckets["+Inf"], "le", "+Inf")

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

func mapCloneInt(m map[string]int) map[string]int {
        out := make(map[string]int, len(m))
        for k, v := range m {
                out[k] = v
        }
        return out
}

func mapCloneFloat(m map[string]float64) map[string]float64 {
        out := make(map[string]float64, len(m))
        for k, v := range m {
                out[k] = v
        }
        return out
}

func sortedKeysInt(m map[string]int) []string {
        keys := make([]string, 0, len(m))
        for k := range m {
                keys = append(keys, k)
        }
        sort.Strings(keys)
        return keys
}

func sortedKeysFloat(m map[string]float64) []string {
        return sortedKeys(m)
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


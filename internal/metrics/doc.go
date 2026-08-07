// Package metrics provides Prometheus instrumentation for Sentinel.
//
// Sentinel exports counters, gauges, and histograms in the Prometheus text
// exposition format so the standard kube-prometheus-stack can scrape
// /metrics without any extra configuration. The implementation is
// hand-rolled rather than depending on prometheus/client_golang — the
// metric set is small and fixed, and dropping the dependency keeps the
// binary small.
//
// Exported metrics:
//
//   - sentinel_incidents_total{status}                  counter
//   - sentinel_investigations_started_total             counter
//   - sentinel_prs_opened_total                         counter
//   - sentinel_sandbox_verifications_total{verified}    counter
//   - sentinel_llm_calls_total{model,status}            counter
//   - sentinel_llm_input_tokens_total{model}            counter
//   - sentinel_llm_output_tokens_total{model}           counter
//   - sentinel_llm_cost_usd_total{model}                counter
//   - sentinel_sandbox_level_failures_total{level}      counter
//   - sentinel_sandbox_retry_count{attempt}             counter
//   - sentinel_investigation_duration_seconds{_count,_sum,_bucket}  histogram
//   - sentinel_llm_call_duration_seconds{_count,_sum,_bucket}       histogram
//   - sentinel_sandbox_duration_seconds{_count,_sum,_bucket}        histogram
//   - sentinel_active_incidents                         gauge
//   - sentinel_llm_in_flight_calls                      gauge
//
// All recorders are safe for concurrent use; a single sync.Mutex guards
// the metricSet (the critical sections are tiny map updates so contention
// is negligible for a single-pod system).
//
// main.go wires the LLM recorders into the llm package via
// llm.SetMetricsRecorder and llm.SetInFlightHooks — those callbacks land
// here as RecordLLMCall / RecordLLMTokens / LLMCallStart / LLMCallEnd.
package metrics

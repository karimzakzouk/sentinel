"""Prometheus metrics — shared module used by every Sentinel service.

Exposes standard Sentinel metrics on the /metrics endpoint. Each service
imports `metrics_app` and mounts it, plus calls the counter/histogram
functions as things happen.

Metrics exposed:
  sentinel_incidents_total{status,service}         — counter
  sentinel_llm_calls_total{model,agent,outcome}    — counter
  sentinel_llm_tokens_total{model,direction}       — counter (input/output)
  sentinel_llm_cost_usd_total{model}               — counter
  sentinel_kafka_consumer_lag{topic,consumer_group} — gauge
  sentinel_agent_iterations{agent}                 — counter
  sentinel_rewind_events_total{agent}              — counter
  sentinel_governance_decisions_total{verdict}     — counter
  sentinel_execution_duration_seconds{fix_type}    — histogram
  sentinel_incident_investigation_duration_seconds — histogram
"""
from __future__ import annotations

from prometheus_client import Counter, Histogram, Gauge, generate_latest, CONTENT_TYPE_LATEST
from fastapi import Response


# --- Counters ---
INCIDENTS_TOTAL = Counter(
    "sentinel_incidents_total",
    "Total incidents processed",
    ["status", "service"],
)

LLM_CALLS_TOTAL = Counter(
    "sentinel_llm_calls_total",
    "Total LLM API calls",
    ["model", "agent", "outcome"],  # outcome: success | error | timeout
)

LLM_TOKENS_TOTAL = Counter(
    "sentinel_llm_tokens_total",
    "Total LLM tokens used",
    ["model", "direction"],  # direction: input | output
)

LLM_COST_USD_TOTAL = Counter(
    "sentinel_llm_cost_usd_total",
    "Total LLM cost in USD",
    ["model"],
)

AGENT_ITERATIONS = Counter(
    "sentinel_agent_iterations",
    "Total agent invocations",
    ["agent"],
)

REWIND_EVENTS_TOTAL = Counter(
    "sentinel_rewind_events_total",
    "Total rewind events",
    ["agent"],
)

GOVERNANCE_DECISIONS_TOTAL = Counter(
    "sentinel_governance_decisions_total",
    "Total governance decisions",
    ["verdict"],  # allow | require_human_approval | deny
)

EXECUTIONS_TOTAL = Counter(
    "sentinel_executions_total",
    "Total fix executions",
    ["fix_type", "outcome"],  # outcome: success | failure
)

# --- Gauges ---
KAFKA_CONSUMER_LAG = Gauge(
    "sentinel_kafka_consumer_lag",
    "Kafka consumer lag (messages behind)",
    ["topic", "consumer_group"],
)

ACTIVE_INCIDENTS = Gauge(
    "sentinel_active_incidents",
    "Currently active incidents (not in terminal state)",
)

# --- Histograms ---
EXECUTION_DURATION = Histogram(
    "sentinel_execution_duration_seconds",
    "Time to execute a fix",
    ["fix_type"],
)

INVESTIGATION_DURATION = Histogram(
    "sentinel_incident_investigation_duration_seconds",
    "Time from incident detection to fix proposal",
)

LLM_CALL_DURATION = Histogram(
    "sentinel_llm_call_duration_seconds",
    "LLM API call duration",
    ["model"],
)


# Rough cost per 1K tokens (USD) — update with real pricing
COST_PER_1K_TOKENS = {
    "gpt-4o": 0.0175,
    "gpt-4o-mini": 0.00015,
    "gemini-2.0-flash": 0.0001,
    "gemini-2.5-pro": 0.00125,
    "glm-4.5": 0.002,
    "glm-4-flash": 0.0001,
    "anthropic/claude-3-5-sonnet-20241022": 0.003,
    "anthropic/claude-3-5-haiku-20241022": 0.0008,
}


def record_llm_call(model: str, agent: str, outcome: str,
                    input_tokens: int = 0, output_tokens: int = 0,
                    duration_seconds: float = 0.0) -> None:
    """Record an LLM API call — call this after every LLM interaction."""
    LLM_CALLS_TOTAL.labels(model=model, agent=agent, outcome=outcome).inc()
    if input_tokens:
        LLM_TOKENS_TOTAL.labels(model=model, direction="input").inc(input_tokens)
    if output_tokens:
        LLM_TOKENS_TOTAL.labels(model=model, direction="output").inc(output_tokens)
    cost = (input_tokens + output_tokens) / 1000 * COST_PER_1K_TOKENS.get(model, 0.001)
    LLM_COST_USD_TOTAL.labels(model=model).inc(cost)
    if duration_seconds:
        LLM_CALL_DURATION.labels(model=model).observe(duration_seconds)


def metrics_endpoint() -> Response:
    """FastAPI endpoint that returns Prometheus-format metrics."""
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)

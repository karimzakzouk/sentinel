"""Evidence Gatherer agent — queries Loki/Prometheus/Jaeger to test hypotheses.

For each hypothesis, this agent:
  - Decides what evidence would support it
  - Decides what evidence would contradict it
  - Issues the actual queries (via the context_fetcher's HTTP client)
  - Tags each evidence item with the hypothesis ID it supports/contradicts

This agent is allowed to throw a RewindEvent (Layer 6 edge case #3): if it
discovers that the entire hypothesis set is wrong (e.g. an upstream dependency
is down), it wipes the hypotheses and returns control to the Investigator
with the new context.
"""
from __future__ import annotations

import json
import logging
from typing import Any

from sentinel_common import Evidence, RewindEvent
from sentinel_common.schemas import AgentState

log = logging.getLogger(__name__)


SYSTEM_PROMPT = """You are Sentinel's Evidence Gatherer agent.

You are given a list of root cause hypotheses. For each hypothesis, decide:
  1. What query would *support* this hypothesis? (e.g. a Prometheus query for high connection count, a Loki log query for the specific error message)
  2. What query would *contradict* this hypothesis?

You can query:
  - Loki (log search): {type: "loki", query: "{service=\\"X\\",namespace=\\"Y\\"} |= \\"keyword\\""}
  - Prometheus (metrics): {type: "prometheus", query: "sum(rate(http_requests_total{status=\\"500\\"}[5m]))"}
  - Jaeger (traces): {type: "jaeger", query: "service:X operation:Y tags:error=true"}
  - Kubernetes (events): {type: "k8s", query: "namespace:Y kind:Pod reason:BackOff"}

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
  "rewind": null | { "reason": "...", "new_context": {...} }
}

Set `rewind` ONLY if you discover a brand-new fact that invalidates the entire
hypothesis set (e.g. a hard upstream dependency is down). Leave it null 99%
of the time."""


async def evidence_gatherer_node(state: AgentState, *, llm_router: Any,
                                  context_fetcher: Any, event_store: Any) -> dict[str, Any]:
    """Generate queries, execute them, and attach evidence to each hypothesis."""
    incident_id = state.incident_id
    if not state.hypotheses:
        log.warning("Evidence Gatherer called with no hypotheses")
        return state.model_dump(mode="json")

    # 1. Ask LLM for the evidence queries
    user_msg = _build_user_message(state)
    try:
        result = await llm_router.fast_json(SYSTEM_PROMPT, user_msg, max_tokens=2000)
    except Exception as e:  # noqa: BLE001
        log.error("Evidence Gatherer LLM call failed: %s", e)
        state.status = "failed"
        state.error = f"evidence_gatherer_llm_failure: {e}"
        return state.model_dump(mode="json")

    # 2. Check for rewind (Layer 6 edge case #3)
    rewind = result.get("rewind")
    if rewind:
        log.info("RewindEvent raised by Evidence Gatherer: %s", rewind.get("reason"))
        ev = RewindEvent(
            raised_by="evidence_gatherer",
            reason=rewind.get("reason", "unknown"),
            new_context=rewind.get("new_context", {}),
        )
        state.rewind_events.append(ev)
        state.hypotheses = []  # wipe — Investigator will re-run
        state.agent_trace.append({
            "agent": "evidence_gatherer",
            "action": "rewind",
            "reason": ev.reason,
        })
        await event_store.append(incident_id, "reasoning", "rewind_event",
                                  agent="evidence_gatherer",
                                  payload=ev.model_dump())
        return state.model_dump(mode="json")

    # 3. Execute each query
    queries = result.get("evidence_queries", [])
    new_evidence: list[Evidence] = []
    for i, q in enumerate(queries):
        try:
            result_data = await _execute_query(q.get("query", {}), context_fetcher)
            ev = Evidence(
                e_id=f"E{i+1}",
                source=q.get("query", {}).get("type", "unknown"),
                query=json.dumps(q.get("query", {})),
                result=result_data,
                supports_hypothesis_id=q.get("hypothesis_id") if q.get("supports_or_contradicts") == "supports" else None,
                contradicts_hypothesis_id=q.get("hypothesis_id") if q.get("supports_or_contradicts") == "contradicts" else None,
            )
            new_evidence.append(ev)
        except Exception as e:  # noqa: BLE001
            log.warning("Evidence query failed: %s", e)
            new_evidence.append(Evidence(
                e_id=f"E{i+1}",
                source=q.get("query", {}).get("type", "unknown"),
                query=json.dumps(q.get("query", {})),
                result={"error": str(e)},
                supports_hypothesis_id=q.get("hypothesis_id") if q.get("supports_or_contradicts") == "supports" else None,
                contradicts_hypothesis_id=q.get("hypothesis_id") if q.get("supports_or_contradicts") == "contradicts" else None,
            ))

    state.evidence = new_evidence
    state.agent_trace.append({
        "agent": "evidence_gatherer",
        "evidence_count": len(new_evidence),
    })
    await event_store.append(incident_id, "reasoning", "evidence_gathered",
                              agent="evidence_gatherer",
                              payload={"evidence": [e.model_dump() for e in new_evidence]})

    return state.model_dump(mode="json")


def _build_user_message(state: AgentState) -> str:
    parts: list[str] = []
    parts.append(f"# Service: {state.cluster.service} (namespace: {state.cluster.namespace})")
    parts.append("")
    parts.append("## Hypotheses to test:")
    for h in state.hypotheses:
        parts.append(f"### {h.h_id} (confidence={h.confidence:.2f})")
        parts.append(h.statement)
        if h.supporting_evidence:
            parts.append("Supporting evidence so far:")
            for e in h.supporting_evidence:
                parts.append(f"  - {e}")
        parts.append("")
    parts.append("## Cluster context (for query construction):")
    parts.append(state.cluster.summary)
    parts.append("")
    parts.append("## Your Task")
    parts.append("Produce evidence queries for each hypothesis. Be concrete — use the actual service name and namespace.")
    return "\n".join(parts)


async def _execute_query(query: dict[str, Any], context_fetcher: Any) -> dict[str, Any]:
    """Execute a query against Loki / Prometheus / Jaeger / K8s."""
    qtype = query.get("type")
    if qtype == "loki":
        return await _query_loki(query.get("query", ""), context_fetcher)
    if qtype == "prometheus":
        return await _query_prometheus(query.get("query", ""), context_fetcher)
    if qtype == "jaeger":
        return await _query_jaeger(query.get("query", ""), context_fetcher)
    if qtype == "k8s":
        return await _query_k8s(query.get("query", ""), context_fetcher)
    return {"error": f"unknown query type: {qtype}"}


async def _query_loki(query: str, context_fetcher: Any) -> dict[str, Any]:
    import os
    import httpx
    loki_url = os.getenv("LOKI_URL")
    if not loki_url:
        return {"error": "LOKI_URL not configured"}
    async with httpx.AsyncClient(timeout=10.0) as c:
        r = await c.get(f"{loki_url}/loki/api/v1/query", params={"query": query, "limit": 50})
        r.raise_for_status()
        return r.json().get("data", {})


async def _query_prometheus(query: str, context_fetcher: Any) -> dict[str, Any]:
    import os
    import httpx
    prom_url = os.getenv("PROMETHEUS_URL")
    if not prom_url:
        return {"error": "PROMETHEUS_URL not configured"}
    async with httpx.AsyncClient(timeout=10.0) as c:
        r = await c.get(f"{prom_url}/api/v1/query", params={"query": query})
        r.raise_for_status()
        return r.json().get("data", {})


async def _query_jaeger(query: str, context_fetcher: Any) -> dict[str, Any]:
    import os
    import httpx
    jaeger_url = os.getenv("JAEGER_URL")
    if not jaeger_url:
        return {"error": "JAEGER_URL not configured"}
    async with httpx.AsyncClient(timeout=10.0) as c:
        r = await c.get(f"{jaeger_url}/api/traces", params={"tags": query})
        r.raise_for_status()
        return r.json().get("data", {})


async def _query_k8s(query: str, context_fetcher: Any) -> dict[str, Any]:
    # The reasoning service doesn't have direct K8s API access — that's the
    # operator's job. We'd send a request to the operator's API.
    return {"note": "K8s queries are routed through the operator; not yet implemented in this node"}

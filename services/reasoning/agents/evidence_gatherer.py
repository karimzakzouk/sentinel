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

import asyncio
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


async def evidence_gatherer_node(
    state: AgentState, *, llm_router: Any, context_fetcher: Any, event_store: Any
) -> dict[str, Any]:
    """Generate queries, execute them, and attach evidence to each hypothesis."""
    incident_id = state.incident_id
    if not state.hypotheses:
        log.warning("Evidence Gatherer called with no hypotheses")
        return state.model_dump(mode="json")

    # 1. Ask LLM for the evidence queries
    user_msg = _build_user_message(state)
    try:
        result = await llm_router.fast_json(SYSTEM_PROMPT, user_msg, max_tokens=4000)
    except Exception as e:
        log.error("Evidence Gatherer LLM call failed: %s", e)
        # Don't fail the whole investigation — continue with no evidence.
        # The Analyst will pick the top hypothesis by prior confidence.
        # This is more resilient than crashing.
        state.evidence = []
        state.agent_trace.append(
            {
                "agent": "evidence_gatherer",
                "action": "llm_failed_no_evidence",
                "error": str(e)[:200],
            }
        )
        return state.model_dump(mode="json")

    # 2. Normalize the result — LLMs sometimes return a list instead of an object.
    # If result is a list, try to extract the first dict element. If it's not a
    # dict at all, treat it as empty (no evidence queries, no rewind).
    if isinstance(result, list):
        result = result[0] if result and isinstance(result[0], dict) else {}
    if not isinstance(result, dict):
        result = {}

    # 3. Check for rewind (Layer 6 edge case #3)
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
        state.agent_trace.append(
            {
                "agent": "evidence_gatherer",
                "action": "rewind",
                "reason": ev.reason,
            }
        )
        await event_store.append(
            incident_id, "reasoning", "rewind_event", agent="evidence_gatherer", payload=ev.model_dump(mode="json")
        )
        return state.model_dump(mode="json")

    # 3. Execute all queries IN PARALLEL (with per-query timeout).
    # Previously this was a serial for loop — 10 queries × 15s = 150s worst case.
    # Now all queries run concurrently via asyncio.gather — max time = 15s
    # regardless of query count.
    queries = result.get("evidence_queries", [])

    async def _run_single_query(i: int, q: dict) -> Evidence:
        """Run one evidence query with a 15s timeout. Returns Evidence (never raises)."""
        try:
            result_data = await asyncio.wait_for(
                _execute_query(q.get("query", {}), context_fetcher),
                timeout=15.0,
            )
            return Evidence(
                e_id=f"E{i + 1}",
                source=q.get("query", {}).get("type", "unknown"),
                query=json.dumps(q.get("query", {})),
                result=result_data,
                supports_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "supports"
                else None,
                contradicts_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "contradicts"
                else None,
            )
        except TimeoutError:
            log.warning("Evidence query %d timed out after 15s", i + 1)
            return Evidence(
                e_id=f"E{i + 1}",
                source=q.get("query", {}).get("type", "unknown"),
                query=json.dumps(q.get("query", {})),
                result={"error": "query timed out after 15s"},
                supports_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "supports"
                else None,
                contradicts_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "contradicts"
                else None,
            )
        except Exception as e:
            log.warning("Evidence query %d failed: %s", i + 1, e)
            return Evidence(
                e_id=f"E{i + 1}",
                source=q.get("query", {}).get("type", "unknown"),
                query=json.dumps(q.get("query", {})),
                result={"error": str(e)},
                supports_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "supports"
                else None,
                contradicts_hypothesis_id=q.get("hypothesis_id")
                if q.get("supports_or_contradicts") == "contradicts"
                else None,
            )

    # Run all queries in parallel — returns results in order
    if queries:
        new_evidence = await asyncio.gather(*[_run_single_query(i, q) for i, q in enumerate(queries)])
        new_evidence = list(new_evidence)
    else:
        new_evidence = []

    state.evidence = new_evidence
    state.agent_trace.append(
        {
            "agent": "evidence_gatherer",
            "evidence_count": len(new_evidence),
        }
    )
    await event_store.append(
        incident_id,
        "reasoning",
        "evidence_gathered",
        agent="evidence_gatherer",
        payload={"evidence": [e.model_dump(mode="json") for e in new_evidence]},
    )

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
    parts.append(
        "Produce evidence queries for each hypothesis. Be concrete — use the actual service name and namespace."
    )
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
    """Query the Kubernetes API for evidence.

    Supports query formats:
      - "namespace:N kind:K [labelSelector:K=V] [fieldSelector:K=V] [limit:N]"
        → lists resources of kind K in namespace N
      - "events:namespace:N [fieldSelector:K=V] [limit:N]"
        → lists recent events for the namespace (useful for BackOff, FailedScheduling, etc.)
      - "logs:namespace:N pod:P [container:C] [limitBytes:N] [sinceSeconds:N]"
        → fetches pod logs (truncated to 4KB to stay within LLM context budget)

    Returns the raw K8s API response as a dict. Errors are returned as
    {"error": "..."} so the Analyst can mark the evidence as weak.
    """
    import re

    try:
        from kubernetes import client
        from kubernetes.client import ApiClient
        from kubernetes.config import load_incluster_config
    except ImportError:
        return {"error": "kubernetes python client not installed"}

    # Load in-cluster config (the reasoning pod's service account)
    try:
        load_incluster_config()
    except Exception:
        # Fall back to kubeconfig if running outside cluster (dev mode)
        try:
            from kubernetes.config import load_kube_config

            load_kube_config()
        except Exception as e:
            return {"error": f"no k8s config available: {e}"}

    # Parse the query string
    parts = dict(re.findall(r"(\w+):(\S+)", query))
    namespace = parts.get("namespace", "default")
    kind = parts.get("kind", "").lower()
    limit = int(parts.get("limit", "50"))

    api = ApiClient()

    try:
        # Events endpoint (most useful for incident investigation)
        if kind == "event" or "events:" in query:
            v1 = client.CoreV1Api(api)
            field_selector = parts.get("fieldSelector", "")
            kwargs = {"limit": limit}
            if field_selector:
                kwargs["field_selector"] = field_selector
            events = v1.list_namespaced_event(namespace, **kwargs)
            return {
                "items": [
                    {
                        "name": e.metadata.name,
                        "reason": e.reason,
                        "message": e.message,
                        "last_timestamp": e.last_timestamp.isoformat() if e.last_timestamp else None,
                        "count": e.count,
                        "involved_object": f"{e.involved_object.kind}/{e.involved_object.name}",
                        "type": e.type,
                    }
                    for e in events.items[:limit]
                ],
                "count": len(events.items),
            }

        # Pod logs
        if kind == "log" or "logs:" in query:
            pod = parts.get("pod")
            if not pod:
                return {"error": "logs query requires 'pod:P' parameter"}
            v1 = client.CoreV1Api(api)
            kwargs = {"timestamps": True}
            if "container" in parts:
                kwargs["container"] = parts["container"]
            if "sinceSeconds" in parts:
                kwargs["since_seconds"] = int(parts["sinceSeconds"])
            if "limitBytes" in parts:
                kwargs["limit_bytes"] = int(parts["limitBytes"])
            else:
                kwargs["limit_bytes"] = 4096  # cap at 4KB for LLM context budget
            log_text = v1.read_namespaced_pod_log(pod, namespace, **kwargs)
            return {"pod": pod, "namespace": namespace, "log_tail": log_text[-4000:] if log_text else ""}

        # Generic list (pods, deployments, etc.)
        if kind in ("pod", "pods"):
            v1 = client.CoreV1Api(api)
            kwargs = {"limit": limit}
            if "labelSelector" in parts:
                kwargs["label_selector"] = parts["labelSelector"]
            if "fieldSelector" in parts:
                kwargs["field_selector"] = parts["fieldSelector"]
            pods = v1.list_namespaced_pod(namespace, **kwargs)
            return {
                "items": [
                    {
                        "name": p.metadata.name,
                        "phase": p.status.phase,
                        "restart_count": sum(cs.restart_count for cs in (p.status.container_statuses or [])),
                        "ready": all(cs.ready for cs in (p.status.container_statuses or []))
                        if p.status.container_statuses
                        else False,
                        "image": [cs.image for cs in (p.status.container_statuses or [])],
                        "node": p.spec.node_name,
                    }
                    for p in pods.items[:limit]
                ],
                "count": len(pods.items),
            }

        if kind in ("deployment", "deployments"):
            apps = client.AppsV1Api(api)
            deps = apps.list_namespaced_deployment(namespace, limit=limit)
            return {
                "items": [
                    {
                        "name": d.metadata.name,
                        "replicas": d.spec.replicas,
                        "ready_replicas": d.status.ready_replicas or 0,
                        "image": [c.image for c in d.spec.template.spec.containers],
                        "conditions": [
                            {"type": c.type, "status": c.status, "reason": c.reason}
                            for c in (d.status.conditions or [])
                        ],
                    }
                    for d in deps.items[:limit]
                ],
                "count": len(deps.items),
            }

        return {"error": f"unsupported k8s kind: {kind!r}. Use 'event', 'pod', 'deployment', or 'log'."}
    except Exception as e:
        return {"error": f"k8s query failed: {type(e).__name__}: {e}"}

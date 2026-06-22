"""Investigator agent — forms up to 5 root cause hypotheses.

Input: AgentState with cluster + retrieved_incidents (priors from Weaviate).
Output: Updated state with up to 5 Hypothesis objects.

Uses the frontier LLM model because hypothesis generation requires deep
reasoning over the anomaly signature, the code, and historical incidents.
"""

from __future__ import annotations

import json
import logging
from typing import Any

from pydantic import ValidationError

from agents._repo_discovery import _discover_repo_from_state
from sentinel_common import Hypothesis
from sentinel_common.schemas import AgentState, IncidentStatus

log = logging.getLogger(__name__)


SYSTEM_PROMPT = """You are Sentinel's Investigator agent — a senior SRE forming root cause hypotheses.

Given:
- An anomaly cluster describing what's wrong with a service
- Recent log lines and trace spans (if available)
- Similar past incidents retrieved from the vector DB
- Source code of files mentioned in stack traces

Your job: produce UP TO 5 root cause hypotheses. Each hypothesis must be:
  - Specific (e.g. NOT "the database is slow" — instead "the connection pool is exhausted because max_connections was reduced in commit abc123")
  - Testable (the Evidence Gatherer must be able to confirm or refute it via Loki/Prometheus/Jaeger queries)
  - Independent (hypotheses should not just be rephrasings of each other)

Rank hypotheses by likelihood given the priors.

Respond with a JSON object of shape:
{
  "hypotheses": [
    {
      "statement": "...",
      "supporting_evidence": ["..."],
      "confidence": 0.0-1.0,
      "source_files": ["src/foo.py"]
    }
  ]
}

Do NOT make up evidence — only cite things that appear in the context."""


async def investigator_node(
    state: AgentState, *, llm_router: Any, context_fetcher: Any, event_store: Any
) -> dict[str, Any]:
    """Generate up to 5 hypotheses based on cluster + context."""
    incident_id = state.incident_id
    cluster = state.cluster

    # 1. Anchor on trace if available (Layer 6 edge case #4)
    anchor = await context_fetcher.anchor_span(cluster)

    # 2. Parse stack traces from logs to find source files
    all_text = cluster.summary + "\n"
    for a in cluster.anomalies:
        all_text += a.explanation + "\n"
    source_files = context_fetcher.parse_stack_trace(all_text)
    state.source_files = list(set(state.source_files + source_files))

    # 3. Retrieve similar past incidents from Weaviate (with code-churn decay)
    similar = await context_fetcher.retrieve_similar_incidents(cluster, top_k=5)
    state.retrieved_incidents = similar

    # 4. Auto-discover the GitHub repo from the cluster's annotations.
    # The ingestion layer (or CRD watcher, for manual triggers) populates
    # state.cluster.annotations with sentinel.io/repo. Without this, we
    # can't fetch source code and the Investigator has no code context
    # to form hypotheses against.
    repo = _discover_repo_from_state(state)

    # 5. Fetch the source code for the files we identified
    fetched = await context_fetcher.fetch_files_for_cluster(cluster, repo=repo)
    state.fetched_code = fetched

    # 6. Build the LLM prompt
    user_msg = _build_user_message(state, anchor, similar, fetched)

    # 7. Call LLM
    try:
        result = await llm_router.frontier_json(SYSTEM_PROMPT, user_msg, max_tokens=4000)
    except Exception as e:
        log.error("Investigator LLM call failed: %s", e)
        state.status = IncidentStatus.FAILED
        state.error = f"investigator_llm_failure: {e}"
        return state.model_dump(mode="json")

    # 8. Parse + validate hypotheses
    try:
        # Normalize: LLMs sometimes return a list instead of an object
        if isinstance(result, list):
            result = result[0] if result and isinstance(result[0], dict) else {}
        if not isinstance(result, dict):
            result = {}
        raw_hyps = result.get("hypotheses", [])
        new_hyps: list[Hypothesis] = []
        for i, h in enumerate(raw_hyps[:5], start=1):
            new_hyps.append(
                Hypothesis(
                    h_id=f"H{i}",
                    statement=h.get("statement", ""),
                    supporting_evidence=h.get("supporting_evidence", []),
                    contradicting_evidence=[],
                    confidence=float(h.get("confidence", 0.5)),
                    falsified=False,
                    source_files=h.get("source_files", []),
                )
            )
        state.hypotheses = new_hyps
        state.iterations += 1
        state.agent_trace.append(
            {
                "agent": "investigator",
                "iteration": state.iterations,
                "hypothesis_count": len(new_hyps),
                "had_trace_anchor": anchor is not None,
                "fetched_files": list(fetched.keys()),
            }
        )
        await event_store.append(
            incident_id,
            "reasoning",
            "hypotheses_formed",
            agent="investigator",
            payload={"hypotheses": [h.model_dump(mode="json") for h in new_hyps]},
        )
    except (ValidationError, ValueError) as ve:
        log.error("Hypothesis validation failed: %s", ve)
        state.status = IncidentStatus.FAILED
        state.error = f"hypothesis_validation: {ve}"
        return state.model_dump(mode="json")

    return state.model_dump(mode="json")


def _build_user_message(
    state: AgentState, anchor: dict[str, Any] | None, similar: list[dict[str, Any]], fetched_code: dict[str, str]
) -> str:
    parts: list[str] = []
    parts.append(f"# Service: {state.cluster.service}")
    parts.append(f"# Namespace: {state.cluster.namespace}")
    parts.append(f"# Severity: {state.cluster.severity}")
    parts.append("")
    parts.append("## Anomaly Cluster Summary")
    parts.append(state.cluster.summary)
    parts.append("")
    parts.append("## Anomalies")
    for a in state.cluster.anomalies[:8]:
        parts.append(f"- [{a.detector}] score={a.score:.2f}: {a.explanation}")
        parts.append(f"  dimensions: {a.dimensions}")
    parts.append("")
    if anchor is not None:
        parts.append("## Trace Anchor (first failing span)")
        parts.append(json.dumps(anchor, indent=2)[:2000])
        parts.append("")
    if similar:
        parts.append("## Similar Past Incidents (with code-churn decay)")
        for s in similar[:5]:
            parts.append(
                f"- score={s.get('decayed_score', 0):.2f} "
                f"(semantic={s.get('semantic_score', 0):.2f}, "
                f"decay={s.get('churn_decay', 0):.2f}): "
                f"{s.get('summary', '')}"
            )
            parts.append(f"  root_cause: {s.get('root_cause', '')}")
            parts.append(f"  fix: {s.get('fix_pattern', '')}")
        parts.append("")
    if fetched_code:
        parts.append("## Source Code (extracted from stack traces)")
        for path, content in list(fetched_code.items())[:5]:
            parts.append(f"### {path}")
            parts.append("```")
            parts.append(content[:4000])  # cap per-file size
            parts.append("```")
        parts.append("")
    parts.append("## Your Task")
    parts.append("Produce up to 5 root cause hypotheses. Be specific and testable.")
    return "\n".join(parts)

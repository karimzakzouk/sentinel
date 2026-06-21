"""Analyst agent — kills falsified hypotheses, computes strict confidence, picks survivor.

The Analyst is the *math* agent. It does NOT use the LLM for confidence
scoring — it uses Bayes' rule and the Beta-distribution priors from the
feedback layer.

Inputs:
  - The list of hypotheses (with prior confidences from the Investigator)
  - The list of evidence items (with supports/contradicts tags)
  - Beta-distribution priors from the feedback layer (per fix-pattern)

Output:
  - Each hypothesis is marked falsified=True if any strong contradicting evidence was found
  - The survivor hypothesis is selected as root_cause
  - Confidence is recomputed via Bayes: posterior = (prior * likelihood) / marginal
"""
from __future__ import annotations

import logging
import math
from typing import Any

from sentinel_common import RootCause
from sentinel_common.schemas import AgentState, IncidentStatus

log = logging.getLogger(__name__)


async def analyst_node(state: AgentState, *, llm_router: Any,
                        beta_store: Any, event_store: Any) -> dict[str, Any]:
    """Apply falsification + Bayes posterior to pick the root cause."""
    incident_id = state.incident_id
    if not state.hypotheses:
        log.warning("Analyst called with no hypotheses")
        state.status = IncidentStatus.FAILED
        state.error = "analyst_no_hypotheses"
        return state.model_dump(mode="json")

    # 1. Falsify: any hypothesis with strong contradicting evidence is dead.
    for h in state.hypotheses:
        contradicting = [e for e in state.evidence if e.contradicts_hypothesis_id == h.h_id]
        supporting = [e for e in state.evidence if e.supports_hypothesis_id == h.h_id]
        # A "strong" contradiction is one where the query returned a non-empty
        # result that explicitly contradicts the hypothesis.
        strong_contradictions = [e for e in contradicting if _is_strong_result(e.result)]
        if strong_contradictions:
            h.falsified = True
            h.contradicting_evidence = [e.e_id for e in strong_contradictions]
            # Pull down confidence to ~0
            h.confidence = min(h.confidence, 0.05)
        # Strong supporting evidence nudges confidence up
        strong_supports = [e for e in supporting if _is_strong_result(e.result)]
        if strong_supports:
            h.supporting_evidence = [e.e_id for e in strong_supports]
            h.confidence = min(1.0, h.confidence + 0.1 * len(strong_supports))

    # 2. Pick survivor: highest non-falsified confidence
    survivors = [h for h in state.hypotheses if not h.falsified]
    if not survivors:
        log.warning("All hypotheses falsified — rewinding to Investigator")
        # Trigger a rewind — the Investigator will get a fresh shot
        from sentinel_common import RewindEvent
        state.rewind_events.append(RewindEvent(
            raised_by="analyst",
            reason="all_hypotheses_falsified",
            new_context={"falsified_hypotheses": [h.model_dump(mode="json") for h in state.hypotheses]},
        ))
        state.hypotheses = []
        state.agent_trace.append({"agent": "analyst", "action": "rewind_all_falsified"})
        await event_store.append(incident_id, "reasoning", "rewind_event",
                                  agent="analyst",
                                  payload={"reason": "all_hypotheses_falsified"})
        return state.model_dump(mode="json")

    survivors.sort(key=lambda h: h.confidence, reverse=True)
    survivor = survivors[0]

    # 3. Compute posterior via Beta prior + evidence likelihood
    # The Beta prior comes from the feedback layer: alpha/(alpha+beta) is the
    # historical success rate of fixes for this category.
    category = _classify_category(survivor.statement)
    service = state.cluster.service
    alpha, beta, succ, fail = await beta_store.get(service, category)
    prior_mean = alpha / (alpha + beta)
    # Likelihood: how much the supporting evidence shifts us above the prior
    n_support = len([e for e in state.evidence if e.supports_hypothesis_id == survivor.h_id and _is_strong_result(e.result)])
    n_contra = len([e for e in state.evidence if e.contradicts_hypothesis_id == survivor.h_id and _is_strong_result(e.result)])
    # Bayesian update on a Beta prior with binomial likelihood:
    #   posterior_alpha = alpha + n_support
    #   posterior_beta  = beta  + n_contra
    post_alpha = alpha + n_support
    post_beta = beta + n_contra
    posterior_mean = post_alpha / (post_alpha + post_beta)

    # Combine LLM prior confidence with Bayesian posterior (weighted average)
    llm_prior_weight = 0.4
    final_confidence = (llm_prior_weight * survivor.confidence +
                        (1 - llm_prior_weight) * posterior_mean)

    # 4. Determine if a schema-migration is involved (Layer 6 edge case #1)
    has_schema_migration = _detect_schema_migration_signal(survivor.statement, state)

    # 5. Build the RootCause object
    state.root_cause = RootCause(
        statement=survivor.statement,
        confidence=final_confidence,
        evidence_ids=[e.e_id for e in state.evidence if e.supports_hypothesis_id == survivor.h_id],
        source_files=survivor.source_files,
        category=category,
        has_schema_migration=has_schema_migration,
        git_commit=state.cluster.anomalies[0].signal_id and None,  # would come from signal
    )
    state.status = IncidentStatus.ROOT_CAUSED
    state.agent_trace.append({
        "agent": "analyst",
        "survivor": survivor.h_id,
        "falsified_count": len(state.hypotheses) - 1,
        "prior_mean": prior_mean,
        "posterior_mean": posterior_mean,
        "final_confidence": final_confidence,
        "has_schema_migration": has_schema_migration,
    })
    await event_store.append(incident_id, "reasoning", "root_cause_identified",
                              agent="analyst",
                              payload={"root_cause": state.root_cause.model_dump(mode="json"),
                                       "falsified_hypotheses": [h.h_id for h in state.hypotheses if h.falsified]})

    return state.model_dump(mode="json")


def _is_strong_result(result: dict[str, Any]) -> bool:
    """A 'strong' result is one that returned actual data (not just an error)."""
    if "error" in result:
        return False
    # Loki returns {"result": [...]}
    if "result" in result:
        return len(result["result"]) > 0
    # Prometheus returns {"result": [...]}
    if "result" in result:
        return len(result["result"]) > 0
    return bool(result)


# Mapping from hypothesis keywords to categories — used for Beta-prior lookup.
_CATEGORY_KEYWORDS = [
    ("oom", "oom"),
    ("out of memory", "oom"),
    ("oomkilled", "oom"),
    ("connection pool", "conn_pool_exhausted"),
    ("timeout", "timeout"),
    ("deadline exceeded", "timeout"),
    ("bad deploy", "bad_deploy"),
    ("regression", "bad_deploy"),
    ("config drift", "config_drift"),
    ("config change", "config_drift"),
    ("upstream", "upstream_down"),
    ("dependency", "upstream_down"),
    ("migration", "schema_migration"),
    ("schema", "schema_migration"),
    ("dns", "dns"),
    ("certificate", "cert"),
    ("auth", "auth"),
    ("ratelimit", "ratelimit"),
    ("rate limit", "ratelimit"),
]


def _classify_category(statement: str) -> str:
    s = statement.lower()
    for kw, cat in _CATEGORY_KEYWORDS:
        if kw in s:
            return cat
    return "other"


def _detect_schema_migration_signal(statement: str, state: AgentState) -> bool:
    """Detect whether this incident involves a schema migration.

    We look at:
      - The hypothesis statement for migration keywords
      - The cluster's anomalies' payloads for CD history signals with
        has_schema_migration=True
    """
    s = statement.lower()
    if any(kw in s for kw in ["migration", "alter table", "schema"]):
        return True
    # Check the cluster's source signals — the cluster summary and anomalies
    # carry the context from the ingestion layer
    cluster_text = state.cluster.summary.lower()
    for a in state.cluster.anomalies:
        cluster_text += " " + a.explanation.lower()
    if "has_schema_migration" in cluster_text or "schema migration" in cluster_text:
        return True
    return False

"""LangGraph state machine — wires the 4 agents into a recursive graph.

The graph supports the RewindEvent (Layer 6 edge case #3): any agent can
wipe the hypotheses and send control back to the Investigator.

Layout:

    START
      |
      v
    investigator ────────────┐
      |                       |
      v                       |
    evidence_gatherer <───────┤  (rewind)
      |                       |
      v                       |
    analyst ──────────────────┤  (rewind)
      |                       |
      v                       |
    fix_proposer              |
      |                       |
      v                       |
    END                       |
                              ^
                              └── (rewind goes back to investigator)
"""

from __future__ import annotations

import logging
from typing import Any

from langgraph.graph import END, StateGraph  # type: ignore

from agents.analyst import analyst_node
from agents.evidence_gatherer import evidence_gatherer_node
from agents.fix_proposer import fix_proposer_node
from agents.investigator import investigator_node
from sentinel_common.schemas import AgentState

log = logging.getLogger(__name__)


def build_graph(
    *, llm_router: Any, context_fetcher: Any, incident_store: Any, event_store: Any, beta_store: Any
) -> Any:
    """Build and compile the LangGraph reasoning graph.

    Every node takes the AgentState dict and returns a dict with a `state` key
    containing the updated state. We use a single-state graph (no channels)
    so the state flows through cleanly.
    """
    g = StateGraph(state_schema=dict)

    # Bind dependencies into each node via closures
    async def _investigator(state: dict[str, Any]) -> dict[str, Any]:
        s = AgentState.model_validate(state)
        # The Investigator can be invoked after a rewind — if so, the
        # rewind_events list has new context. Merge it into the prompt.
        return await investigator_node(
            s,
            llm_router=llm_router,
            context_fetcher=context_fetcher,
            event_store=event_store,
        )

    async def _evidence(state: dict[str, Any]) -> dict[str, Any]:
        s = AgentState.model_validate(state)
        return await evidence_gatherer_node(
            s,
            llm_router=llm_router,
            context_fetcher=context_fetcher,
            event_store=event_store,
        )

    async def _analyst(state: dict[str, Any]) -> dict[str, Any]:
        s = AgentState.model_validate(state)
        return await analyst_node(
            s,
            llm_router=llm_router,
            beta_store=beta_store,
            event_store=event_store,
        )

    async def _fix(state: dict[str, Any]) -> dict[str, Any]:
        s = AgentState.model_validate(state)
        return await fix_proposer_node(
            s,
            llm_router=llm_router,
            event_store=event_store,
        )

    g.add_node("investigator", _investigator)
    g.add_node("evidence_gatherer", _evidence)
    g.add_node("analyst", _analyst)
    g.add_node("fix_proposer", _fix)

    g.set_entry_point("investigator")

    # Conditional edges for rewind support
    g.add_conditional_edges(
        "investigator",
        _after_investigator,
        {"evidence_gatherer": "evidence_gatherer", END: END},
    )
    g.add_conditional_edges(
        "evidence_gatherer",
        _after_evidence,
        {"investigator": "investigator", "analyst": "analyst", END: END},
    )
    g.add_conditional_edges(
        "analyst",
        _after_analyst,
        {"investigator": "investigator", "fix_proposer": "fix_proposer", END: END},
    )
    g.add_edge("fix_proposer", END)

    return g.compile()


def _unwrap(state: dict[str, Any]) -> dict[str, Any]:
    """Unwrap state if nodes returned {'state': {...}} instead of the fields directly."""
    if "state" in state and isinstance(state["state"], dict) and "incident_id" not in state:
        return state["state"]
    return state


def _after_investigator(state: dict[str, Any]) -> str:
    state = _unwrap(state)
    try:
        s = AgentState.model_validate(state)
    except Exception:
        return END
    if s.status == "failed":
        return END
    if not s.hypotheses:
        return END
    return "evidence_gatherer"


def _after_evidence(state: dict[str, Any]) -> str:
    state = _unwrap(state)
    try:
        s = AgentState.model_validate(state)
    except Exception:
        return END
    if s.status == "failed":
        return END
    # If Evidence Gatherer raised a rewind, go back to Investigator
    if s.rewind_events and s.rewind_events[-1].raised_by == "evidence_gatherer":
        if s.iterations < s.max_iterations:
            return "investigator"
        else:
            log.warning("Max iterations hit during rewind — ending")
            return END
    return "analyst"


def _after_analyst(state: dict[str, Any]) -> str:
    state = _unwrap(state)
    try:
        s = AgentState.model_validate(state)
    except Exception:
        return END
    if s.status == "failed":
        return END
    if s.root_cause is None:
        # All hypotheses were falsified — rewind
        if s.rewind_events and s.rewind_events[-1].raised_by == "analyst":
            if s.iterations < s.max_iterations:
                return "investigator"
            return END
        return END
    return "fix_proposer"

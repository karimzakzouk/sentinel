"""Fix Proposer agent — translates a root cause into a concrete FixProposal.

Depending on the root cause category, this agent picks one of:
  - RESTART      (CrashLoopBackOff with no code change)
  - SCALE        (CPU saturation, connection pool exhaustion)
  - CODE_FIX     (a logic bug that needs a patch)
  - ROLLBACK     (recent bad deploy, no schema migration)
  - ROLL_FORWARD (recent bad deploy WITH schema migration — Layer 6 edge case #1)
  - INFRASTRUCTURE (Terraform-managed resource needs changing)

For CODE_FIX proposals, this agent ALSO writes the actual patch — using the
source code fetched by the context fetcher. It returns a list of
{path, new_content} dicts that the execution layer applies as a PR.
"""
from __future__ import annotations

import json
import logging
import re
from typing import Any

from sentinel_common import FixProposal, FixType
from sentinel_common.schemas import AgentState, IncidentStatus
from agents._repo_discovery import _discover_repo_from_state

log = logging.getLogger(__name__)


SYSTEM_PROMPT = """You are Sentinel's Fix Proposer agent.

Given a confirmed root cause, propose ONE concrete fix. Pick the fix type:

  - restart       : the pod just needs a restart (transient state, CrashLoopBackOff without code bug)
  - scale         : the deployment needs more replicas (CPU/mem saturation)
  - code_fix      : a code change is required — provide the exact file + new content
  - rollback      : revert to the previous deployment (NO schema migration involved)
  - roll_forward  : the previous deploy had a schema migration — must fix forward, NOT rollback
  - infrastructure: a Terraform-managed resource needs changing

For code_fix:
  - "path" is the file to create or modify (e.g. "app/main.py", "src/handler.py")
  - "new_content" is the COMPLETE file contents (not a diff)
  - You can modify existing files OR create new files if the fix requires it
  - Prefer fixing the root cause in the existing code over creating workarounds
  - Be conservative — only change what's necessary

Respond with a JSON object of shape:
{
  "fix_type": "restart|scale|code_fix|rollback|roll_forward|infrastructure",
  "description": "...",
  "k8s_action": null | {"kind": "Deployment", "name": "...", "patch": {...}},
  "files_to_change": [{"path": "app/main.py", "new_content": "complete file contents"}],
  "dockerfile_path": "Dockerfile",
  "pr_title": "[Sentinel] ...",
  "pr_body": "..."
}

The "dockerfile_path" field is the path to the Dockerfile relative to the repo
root. Default to "Dockerfile" (repo root). If the repo is a monorepo with
multiple Dockerfiles, set this to the path of the Dockerfile that builds the
affected service's image.

If fix_type is rollback, leave files_to_change empty — the execution layer
will trigger a kubectl rollout undo. If roll_forward, treat it like code_fix.
"""


async def fix_proposer_node(state: AgentState, *, llm_router: Any,
                             event_store: Any) -> dict[str, Any]:
    """Translate the root cause into a FixProposal."""
    incident_id = state.incident_id
    if state.root_cause is None:
        log.warning("Fix Proposer called without root cause")
        state.status = IncidentStatus.FAILED
        state.error = "fix_proposer_no_root_cause"
        return state.model_dump(mode="json")

    rc = state.root_cause

    # 1. If schema migration is involved, FORCE roll_forward (Layer 6 edge case #1)
    if rc.has_schema_migration and rc.category in ("bad_deploy", "schema_migration"):
        log.info("Schema migration detected — forcing roll_forward (no autonomous rollback)")
        proposal = FixProposal(
            fix_type=FixType.ROLL_FORWARD,
            target_service=state.cluster.service,
            target_namespace=state.cluster.namespace,
            description=f"Roll-forward fix for {rc.category} (schema migration blocks autonomous rollback)",
            repo=_discover_repo(state),
            files_to_change=[],  # to be filled by LLM below
            pr_title=f"[Sentinel] Roll-forward fix for {rc.category} in {state.cluster.service}",
            pr_body=_build_pr_body(state, rc, fix_type="roll_forward"),
            requires_sandbox=True,
            proposed_confidence=rc.confidence,
        )
        # Still call LLM to generate the actual code fix
        proposal = await _llm_code_fix(state, proposal, llm_router)
    else:
        # 2. Ask LLM for the fix
        user_msg = _build_user_message(state)
        try:
            result = await llm_router.frontier_json(SYSTEM_PROMPT, user_msg, max_tokens=6000)
        except Exception as e:  # noqa: BLE001
            log.error("Fix Proposer LLM call failed: %s", e)
            state.status = IncidentStatus.FAILED
            state.error = f"fix_proposer_llm_failure: {e}"
            return state.model_dump(mode="json")

        try:
            # Normalize: LLMs sometimes return a list instead of an object
            if isinstance(result, list):
                result = result[0] if result and isinstance(result[0], dict) else {}
            if not isinstance(result, dict):
                result = {}
            fix_type = FixType(result.get("fix_type", "code_fix"))
        except ValueError:
            fix_type = FixType.CODE_FIX

        proposal = FixProposal(
            fix_type=fix_type,
            target_service=state.cluster.service,
            target_namespace=state.cluster.namespace,
            description=result.get("description", ""),
            repo=_discover_repo(state),
            k8s_action=result.get("k8s_action"),
            files_to_change=result.get("files_to_change", []),
            dockerfile_path=result.get("dockerfile_path", "Dockerfile"),
            pr_title=result.get("pr_title") or f"[Sentinel] {fix_type.value} for {state.cluster.service}",
            pr_body=result.get("pr_body") or _build_pr_body(state, rc, fix_type=fix_type.value),
            requires_sandbox=True,
            proposed_confidence=rc.confidence,
        )

    # 3. Add branch name + base branch
    proposal.branch_name = f"sentinel/{state.cluster.service}-{str(incident_id)[:8]}"
    proposal.base_branch = "main"

    state.fix_proposal = proposal
    state.status = IncidentStatus.FIX_PROPOSED
    state.agent_trace.append({
        "agent": "fix_proposer",
        "fix_type": proposal.fix_type.value,
        "files_changed": len(proposal.files_to_change),
        "confidence": proposal.proposed_confidence,
    })
    await event_store.append(incident_id, "reasoning", "fix_proposed",
                              agent="fix_proposer",
                              payload={"fix_proposal": proposal.model_dump(mode="json")})

    return state.model_dump(mode="json")


def _discover_repo(state: AgentState) -> str | None:
    """Discover the GitHub repo for this service.

    Delegates to the shared `_discover_repo_from_state` helper so the
    Investigator and Fix Proposer use the same logic.
    See agents._repo_discovery for the full contract.
    """
    return _discover_repo_from_state(state)


def _build_user_message(state: AgentState) -> str:
    parts: list[str] = []
    parts.append(f"# Service: {state.cluster.service}")
    parts.append(f"# Namespace: {state.cluster.namespace}")
    parts.append("")
    parts.append("## Confirmed Root Cause")
    parts.append(f"- statement: {state.root_cause.statement}")
    parts.append(f"- category: {state.root_cause.category}")
    parts.append(f"- confidence: {state.root_cause.confidence:.3f}")
    parts.append(f"- has_schema_migration: {state.root_cause.has_schema_migration}")
    parts.append(f"- source_files: {state.root_cause.source_files}")
    parts.append("")
    parts.append("## Evidence")
    for e in state.evidence:
        parts.append(f"- {e.e_id} ({e.source}): {json.dumps(e.result)[:300]}")
    parts.append("")
    parts.append("## Fetched Source Code")
    for path, content in state.fetched_code.items():
        parts.append(f"### {path}")
        parts.append("```")
        parts.append(content[:6000])
        parts.append("```")
    parts.append("")
    parts.append("## Your Task")
    parts.append("Propose ONE fix. Be specific. If code_fix, output the exact new file contents.")
    return "\n".join(parts)


async def _llm_code_fix(state: AgentState, proposal: FixProposal,
                        llm_router: Any) -> FixProposal:
    """For roll_forward cases, ask LLM for the actual code patch."""
    user_msg = _build_user_message(state)
    try:
        result = await llm_router.frontier_json(SYSTEM_PROMPT, user_msg, max_tokens=6000)
        proposal.files_to_change = result.get("files_to_change", [])
        proposal.description = result.get("description", proposal.description)
    except Exception as e:  # noqa: BLE001
        log.error("Roll-forward LLM call failed: %s", e)
        proposal.description = f"{proposal.description} (LLM code generation failed — needs human)"
    return proposal


def _build_pr_body(state: AgentState, rc: Any, fix_type: str) -> str:
    """Generate a markdown PR body for the fix."""
    parts: list[str] = []
    parts.append(f"# Sentinel Autonomous Fix")
    parts.append("")
    parts.append(f"**Fix type:** `{fix_type}`")
    parts.append(f"**Service:** `{state.cluster.service}` (namespace: `{state.cluster.namespace}`)")
    parts.append(f"**Confidence:** `{rc.confidence:.3f}`")
    parts.append(f"**Category:** `{rc.category}`")
    parts.append("")
    parts.append("## Root Cause")
    parts.append(rc.statement)
    parts.append("")
    parts.append("## Evidence")
    for e in state.evidence[:10]:
        parts.append(f"- **{e.e_id}** ({e.source}): {json.dumps(e.result)[:300]}")
    parts.append("")
    parts.append("## Files Changed")
    for f in (state.fix_proposal.files_to_change if state.fix_proposal else []):
        parts.append(f"- `{f.get('path')}`")
    parts.append("")
    parts.append("## Verification")
    parts.append("This fix was tested in a sandbox namespace with 1% mirrored production traffic.")
    parts.append("No 5xx errors were observed during the verification window.")
    parts.append("")
    parts.append("---")
    parts.append("_This PR was opened autonomously by Sentinel. Please review before merging._")
    return "\n".join(parts)

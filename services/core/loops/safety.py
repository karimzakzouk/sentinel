"""Safety loop — consumes incidents, evaluates OPA policies, routes to execution."""
from __future__ import annotations

import asyncio
import logging
from typing import Any

from sentinel_common import GovernanceDecision, GovernanceVerdict, IncidentStatus
from sentinel_common.opa import OPAClient

log = logging.getLogger(__name__)


async def safety_loop(
    bus: Any,
    incident_store: Any,
    event_store: Any,
    settings: Any,
) -> None:
    """Consume incidents, evaluate OPA policies, forward approved fixes to execution."""
    opa = OPAClient(settings.opa_url)
    consumer = bus.consumer(settings.kafka_topic_incidents, "sentinel-core-safety")
    try:
        async for raw in consumer:
            try:
                from sentinel_common import Incident

                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.state.fix_proposal is None:
                    continue
                asyncio.create_task(_safety_evaluate(incident, opa, bus, incident_store, event_store, settings))
            except Exception as e:
                log.warning("Safety consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Safety loop cancelled")


async def _safety_evaluate(
    incident: Any,
    opa: OPAClient,
    bus: Any,
    incident_store: Any,
    event_store: Any,
    settings: Any,
) -> None:
    """Run OPA decision + route to execution or hold for human approval."""
    fp = incident.state.fix_proposal
    rc = incident.state.root_cause
    if fp is None or rc is None:
        return

    opa_input = {
        "fix_type": fp.fix_type.value,
        "target_service": fp.target_service,
        "target_namespace": fp.target_namespace,
        "confidence": fp.proposed_confidence,
        "has_schema_migration": rc.has_schema_migration,
        "is_high_risk_service": fp.target_service in {"payment-api", "billing-api", "auth-service"},
        "replica_change_pct": 0,
        "is_production_namespace": fp.target_namespace in settings.production_namespace_list,
        "actor": "sentinel",
    }
    try:
        verdict_result = await opa.decide("sentinel/verdict", opa_input)
        verdict_str = verdict_result if isinstance(verdict_result, str) else (
            verdict_result.get("verdict") if isinstance(verdict_result, dict) else "deny"
        )
        rules_result = await opa.decide("sentinel/rules_evaluated", opa_input)
        rollback_result = await opa.decide("sentinel/rollback_blocked", opa_input)
        result = {
            "verdict": verdict_str,
            "rules_evaluated": rules_result if isinstance(rules_result, list) else [],
            "confidence_at_decision": fp.proposed_confidence,
            "rollback_blocked": bool(rollback_result) if not isinstance(rollback_result, dict) else False,
        }
    except Exception as e:
        log.warning("OPA failed: %s — treating as require_human_approval", e)
        result = {
            "verdict": "require_human_approval",
            "rules_evaluated": [],
            "confidence_at_decision": fp.proposed_confidence,
            "rollback_blocked": False,
        }

    try:
        verdict = GovernanceVerdict(result.get("verdict", "deny"))
    except ValueError:
        verdict = GovernanceVerdict.DENY

    decision = GovernanceDecision(
        verdict=verdict,
        confidence_at_decision=result.get("confidence_at_decision", fp.proposed_confidence),
        rules_evaluated=result.get("rules_evaluated", []),
        rollback_blocked_reason=result.get("rollback_blocked_reason"),
        schema_migration_blocks_rollback=result.get("rollback_blocked", False),
    )
    incident.state.governance_decision = decision

    if verdict == GovernanceVerdict.ALLOW:
        incident.state.status = IncidentStatus.APPROVED
        incident.status = IncidentStatus.APPROVED
        await bus.publish_model(settings.kafka_topic_fixes, incident, key=fp.target_service)
        log.info("Incident %s ALLOWED → execution", incident.incident_id)
    elif verdict == GovernanceVerdict.REQUIRE_HUMAN_APPROVAL:
        incident.state.status = IncidentStatus.GOVERNANCE_REVIEW
        incident.status = IncidentStatus.GOVERNANCE_REVIEW
        log.info("Incident %s needs human approval", incident.incident_id)
    else:
        incident.state.status = IncidentStatus.ESCALATED
        incident.status = IncidentStatus.ESCALATED
        log.info("Incident %s DENIED", incident.incident_id)

    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))
    await event_store.append(
        incident.incident_id, "safety", "governance_decision",
        agent="safety", payload=decision.model_dump(mode="json"),
    )

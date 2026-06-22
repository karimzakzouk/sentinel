"""
Sentinel Layer 4 — Safety and Governance

Consumes Incident messages (with FixProposal) from the `sentinel.incidents`
Kafka topic, evaluates each fix against the OPA policies, and:

  - If `allow`: forwards to the execution layer (publishes to `sentinel.fixes`)
  - If `require_human_approval`: posts to the configured approval channel
    (Slack webhook / custom webhook) and waits for a human click
  - If `deny`: marks the incident as `escalated` and writes a governance
    decision record for audit

Every decision — allow, require_human_approval, or deny — is persisted to
the `governance_decisions` table for compliance audits.
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys
from datetime import UTC, datetime
from typing import Any
from uuid import uuid4

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

import httpx
import sqlalchemy as sa
import uvicorn
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sqlalchemy.ext.asyncio import create_async_engine

from sentinel_common import (
    GovernanceDecision,
    GovernanceVerdict,
    Incident,
    IncidentStatus,
)
from sentinel_common.config import get_settings
from sentinel_common.db import (
    EventStore,
    IncidentStore,
    governance_decisions_table,
    init_db,
)
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry
from sentinel_common.opa import OPAClient

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.safety")

settings = get_settings()

app = FastAPI(title="Sentinel Safety", version="1.0.0")

bus: KafkaBus | None = None
opa: OPAClient | None = None
incident_store: IncidentStore | None = None
event_store: EventStore | None = None
engine: Any = None
consumer_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, opa, incident_store, event_store, engine, consumer_task
    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()

    opa = OPAClient(settings.opa_url)

    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, pool_size=5)
    await init_db(engine)
    incident_store = IncidentStore(engine)
    event_store = EventStore(engine)

    consumer_task = asyncio.create_task(_consumer_loop())
    log.info("Safety service started")


@app.on_event("shutdown")
async def _shutdown() -> None:
    if consumer_task:
        consumer_task.cancel()
    if bus:
        await bus.stop()
    if opa:
        await opa.close()
    if engine:
        await engine.dispose()


async def _consumer_loop() -> None:
    assert bus is not None
    consumer = bus.consumer(settings.kafka_topic_incidents, "sentinel-safety")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.state.fix_proposal is None:
                    continue
                asyncio.create_task(_process_incident(incident))
            except Exception as e:
                log.warning("Failed to process incident: %s", e)
    except asyncio.CancelledError:
        log.info("Consumer cancelled")


async def _process_incident(incident: Incident) -> None:
    """Run the OPA decision and route accordingly."""
    assert opa is not None and bus is not None and incident_store is not None
    assert event_store is not None and engine is not None

    fp = incident.state.fix_proposal
    rc = incident.state.root_cause

    # Build the input for OPA
    opa_input = {
        "fix_type": fp.fix_type.value,
        "target_service": fp.target_service,
        "target_namespace": fp.target_namespace,
        "confidence": fp.proposed_confidence,
        "has_schema_migration": rc.has_schema_migration if rc else False,
        "is_high_risk_service": fp.target_service in {"payment-api", "billing-api", "auth-service"},
        "replica_change_pct": _compute_replica_pct(fp),
        "is_production_namespace": fp.target_namespace in settings.production_namespace_list,
        "actor": "sentinel",
    }

    # Query OPA — query verdict directly (decision object rule is unreliable)
    try:
        verdict_result = await opa.decide("sentinel/verdict", opa_input)
        verdict_str = (
            verdict_result
            if isinstance(verdict_result, str)
            else (verdict_result.get("verdict") if isinstance(verdict_result, dict) else "deny")
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
        log.error("OPA decision failed: %s", e)
        # Fail safe — treat as require_human_approval
        result = {
            "verdict": "require_human_approval",
            "rules_evaluated": [],
            "confidence_at_decision": fp.proposed_confidence,
            "rollback_blocked": False,
        }

    verdict_str = result.get("verdict", "deny")
    try:
        verdict = GovernanceVerdict(verdict_str)
    except ValueError:
        verdict = GovernanceVerdict.DENY

    decision = GovernanceDecision(
        verdict=verdict,
        confidence_at_decision=result.get("confidence_at_decision", fp.proposed_confidence),
        rules_evaluated=result.get("rules_evaluated", []),
        approval_request_id=str(uuid4()) if verdict == GovernanceVerdict.REQUIRE_HUMAN_APPROVAL else None,
        approval_channel="slack:#ops" if verdict == GovernanceVerdict.REQUIRE_HUMAN_APPROVAL else None,
        rollback_blocked_reason=result.get("rollback_blocked_reason") if verdict == GovernanceVerdict.DENY else None,
        schema_migration_blocks_rollback=result.get("rollback_blocked", False),
    )

    incident.state.governance_decision = decision
    incident.state.agent_trace.append(
        {
            "agent": "safety",
            "verdict": verdict.value,
            "rules": decision.rules_evaluated,
        }
    )

    # Persist decision to governance_decisions table for audit
    async with engine.begin() as conn:
        await conn.execute(
            governance_decisions_table.insert().values(
                incident_id=incident.incident_id,
                ts=datetime.now(UTC),
                verdict=verdict.value,
                rules_evaluated=decision.rules_evaluated,
                confidence_at_decision=decision.confidence_at_decision,
                approval_request_id=decision.approval_request_id,
                approval_channel=decision.approval_channel,
                rollback_blocked_reason=decision.rollback_blocked_reason,
            )
        )

    await event_store.append(
        incident.incident_id, "safety", "governance_decision", agent="safety", payload=decision.model_dump(mode="json")
    )

    # Route
    if verdict == GovernanceVerdict.ALLOW:
        incident.state.status = IncidentStatus.APPROVED
        incident.status = IncidentStatus.APPROVED
        # Forward to execution
        await bus.publish_model(settings.kafka_topic_fixes, incident, key=fp.target_service)
        log.info("Incident %s ALLOWED — forwarded to execution", incident.incident_id)
    elif verdict == GovernanceVerdict.REQUIRE_HUMAN_APPROVAL:
        incident.state.status = IncidentStatus.GOVERNANCE_REVIEW
        incident.status = IncidentStatus.GOVERNANCE_REVIEW
        # Post to approval channel
        await _post_approval_request(incident, decision)
        log.info("Incident %s requires human approval — posted to %s", incident.incident_id, decision.approval_channel)
    else:  # DENY
        incident.state.status = IncidentStatus.ESCALATED
        incident.status = IncidentStatus.ESCALATED
        log.info("Incident %s DENIED — escalated", incident.incident_id)

    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))


def _compute_replica_pct(fp: Any) -> int:
    """Compute the replica change percentage from a fix proposal's k8s_action."""
    if not fp.k8s_action:
        return 0
    patch = fp.k8s_action.get("patch", {}) if isinstance(fp.k8s_action, dict) else {}
    replicas = patch.get("spec", {}).get("replicas")
    if replicas is None:
        return 0
    # Get the current replica count from the actual deployment
    try:
        from kubernetes import client

        apps = client.AppsV1Api()
        deploy = apps.read_namespaced_deployment(
            name=fp.target_service,
            namespace=fp.target_namespace,
        )
        current = deploy.spec.replicas or 1
    except Exception:
        current = replicas  # fallback: assume no change
    if current == 0:
        return 100 if replicas > 0 else 0
    return int(abs(replicas - current) / current * 100)


async def _post_approval_request(incident: Incident, decision: GovernanceDecision) -> None:
    """Post the approval request to Slack (or generic webhook)."""
    webhook = os.getenv("APPROVAL_WEBHOOK_URL")
    if not webhook:
        log.info("No APPROVAL_WEBHOOK_URL configured — skipping approval post")
        return
    fp = incident.state.fix_proposal
    rc = incident.state.root_cause
    payload = {
        "text": f"Sentinel approval required: {fp.fix_type.value} on {fp.target_service}",
        "blocks": [
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": f"*Sentinel Governance Approval Required*\n"
                    f"• Incident: `{incident.incident_id}`\n"
                    f"• Service: `{fp.target_service}` (ns: `{fp.target_namespace}`)\n"
                    f"• Fix type: `{fp.fix_type.value}`\n"
                    f"• Confidence: `{fp.proposed_confidence:.3f}`\n"
                    f"• Root cause: {rc.statement if rc else 'unknown'}\n"
                    f"• Approval ID: `{decision.approval_request_id}`",
                },
            },
            {
                "type": "actions",
                "elements": [
                    {
                        "type": "button",
                        "text": {"type": "plain_text", "text": "Approve"},
                        "style": "primary",
                        "value": decision.approval_request_id,
                        "url": f"{settings.ui_url}/approve/{decision.approval_request_id}",
                    },
                    {
                        "type": "button",
                        "text": {"type": "plain_text", "text": "Deny"},
                        "style": "danger",
                        "value": decision.approval_request_id,
                        "url": f"{settings.ui_url}/deny/{decision.approval_request_id}",
                    },
                ],
            },
        ],
    }
    try:
        async with httpx.AsyncClient(timeout=10.0) as c:
            r = await c.post(webhook, json=payload)
            r.raise_for_status()
    except Exception as e:
        log.warning("Approval webhook post failed: %s", e)


# ---------- HTTP API for human approval callbacks ----------


class ApprovalResponse(BaseModel):
    approval_request_id: str
    approved: bool


@app.post("/admin/approval")
async def handle_approval(resp: ApprovalResponse) -> dict[str, str]:
    """Handle the human approval/denial callback from Slack/UI."""
    assert engine is not None and bus is not None and incident_store is not None
    # Find the incident with this approval_request_id
    async with engine.begin() as conn:
        r = await conn.execute(
            sa.select(governance_decisions_table).where(
                governance_decisions_table.c.approval_request_id == resp.approval_request_id
            )
        )
        row = r.mappings().first()
        if not row:
            raise HTTPException(404, "approval request not found")
        incident_id = row["incident_id"]
        # Update the decision record
        await conn.execute(
            governance_decisions_table.update()
            .where(governance_decisions_table.c.approval_request_id == resp.approval_request_id)
            .values(verdict="allow" if resp.approved else "deny")
        )

    # Re-fetch the incident and forward if approved
    incident = await incident_store.get(incident_id)
    if incident is None:
        raise HTTPException(404, "incident not found")
    inc = Incident.model_validate(incident)
    if resp.approved:
        inc.state.status = IncidentStatus.APPROVED
        inc.status = IncidentStatus.APPROVED
        await bus.publish_model(settings.kafka_topic_fixes, inc, key=inc.cluster.service)
        await event_store.append(incident_id, "safety", "human_approved", agent="human")
    else:
        inc.state.status = IncidentStatus.REJECTED
        inc.status = IncidentStatus.REJECTED
        await event_store.append(incident_id, "safety", "human_rejected", agent="human")
    inc.touch()
    await incident_store.upsert(inc.model_dump(mode="json"))
    return {"status": "ok"}


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "safety"}


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

"""
Sentinel Layer 5 — Execution

Consumes approved incidents from the `sentinel.fixes` topic and executes them.
Execution is GitOps-first:

  - RESTART / SCALE   -> direct K8s API patch (kubectl rollout restart / scale)
  - CODE_FIX          -> GitHub PR (clone repo, write files, push branch, open PR,
                         trigger CI/CD, observe canary)
  - ROLLBACK          -> kubectl rollout undo (only if no schema migration)
  - ROLL_FORWARD      -> GitHub PR (same as code_fix)
  - INFRASTRUCTURE    -> terraform plan, post to Slack, await human click, apply

Before any production touch, the execution layer runs the fix in a sandbox
namespace with 1% mirrored production traffic (Istio traffic mirroring).
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
from datetime import datetime, timezone
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

from fastapi import FastAPI
import uvicorn

from sentinel_common import (
    ExecutionResult, FixType, Incident, IncidentStatus,
)
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry
from sentinel_common.db import IncidentStore, EventStore, init_db
from sqlalchemy.ext.asyncio import create_async_engine

from executors.kubernetes import K8sExecutor
from executors.github import GitHubExecutor
from executors.terraform import TerraformExecutor
from sandbox import SandboxRunner

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.execution")

settings = get_settings()

app = FastAPI(title="Sentinel Execution", version="1.0.0")

bus: KafkaBus | None = None
incident_store: IncidentStore | None = None
event_store: EventStore | None = None
k8s_exec: K8sExecutor | None = None
gh_exec: GitHubExecutor | None = None
tf_exec: TerraformExecutor | None = None
sandbox: SandboxRunner | None = None
consumer_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, incident_store, event_store, k8s_exec, gh_exec, tf_exec, sandbox, consumer_task
    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()

    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, pool_size=5)
    await init_db(engine)
    incident_store = IncidentStore(engine)
    event_store = EventStore(engine)

    k8s_exec = K8sExecutor(settings)
    gh_exec = GitHubExecutor(settings)
    tf_exec = TerraformExecutor(settings)
    sandbox = SandboxRunner(settings, k8s_exec)

    consumer_task = asyncio.create_task(_consumer_loop())
    log.info("Execution service started")


@app.on_event("shutdown")
async def _shutdown() -> None:
    if consumer_task:
        consumer_task.cancel()
    if bus:
        await bus.stop()


async def _consumer_loop() -> None:
    assert bus is not None
    consumer = bus.consumer(settings.kafka_topic_fixes, "sentinel-execution")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(
                    raw if isinstance(raw, bytes) else raw.value
                )
                asyncio.create_task(_execute_incident(incident))
            except Exception as e:  # noqa: BLE001
                log.warning("Failed to process fix: %s", e)
    except asyncio.CancelledError:
        log.info("Consumer cancelled")


async def _execute_incident(incident: Incident) -> None:
    """Run the approved fix through the appropriate executor."""
    assert incident_store is not None and event_store is not None
    fp = incident.state.fix_proposal
    if fp is None:
        return

    # Security check: verify Sentinel is allowed to write in this namespace
    if not settings.can_write_to(fp.target_namespace):
        log.warning("Execution denied — namespace %s not in write allowlist", fp.target_namespace)
        incident.state.status = IncidentStatus.ESCALATED
        incident.status = IncidentStatus.ESCALATED
        incident.state.agent_trace.append({
            "agent": "execution",
            "action": "denied",
            "reason": f"namespace_not_in_write_allowlist: {fp.target_namespace}",
        })
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        await event_store.append(incident.incident_id, "execution", "execution_denied",
                                  payload={"reason": "namespace_not_in_write_allowlist",
                                           "namespace": fp.target_namespace})
        return

    incident.state.status = IncidentStatus.EXECUTING
    incident.status = IncidentStatus.EXECUTING
    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))
    await event_store.append(incident.incident_id, "execution", "execution_started",
                              payload={"fix_type": fp.fix_type.value})

    result = ExecutionResult(
        fix_id=fp.fix_id,
        fix_type=fp.fix_type,
        success=False,
        started_at=datetime.now(timezone.utc),
    )

    try:
        # 1. Run sandbox verification (Layer 6 edge case #2)
        if fp.requires_sandbox and settings.enable_traffic_mirroring:
            sb = await sandbox.run(incident)
            result.sandbox_verified = sb.verified
            result.sandbox_summary = sb.summary
            if not sb.verified:
                result.success = False
                result.error = f"sandbox_verification_failed: {sb.summary}"
                result.finished_at = datetime.now(timezone.utc)
                incident.state.execution_result = result
                incident.state.status = IncidentStatus.FAILED
                incident.status = IncidentStatus.FAILED
                incident.touch()
                await incident_store.upsert(incident.model_dump(mode="json"))
                await event_store.append(incident.incident_id, "execution", "sandbox_failed",
                                          payload={"summary": sb.summary})
                return

        # 2. Execute the actual fix
        if fp.fix_type in (FixType.CODE_FIX, FixType.ROLL_FORWARD):
            # Use the split GitHub executor: prepare_branch → open_pr
            branch_info = await gh_exec.prepare_branch(incident)
            if branch_info.get("success"):
                result = await gh_exec.open_pr(incident, branch_info,
                                               sandbox_verified=result.sandbox_verified,
                                               sandbox_summary=result.sandbox_summary or "")
            else:
                result.success = False
                result.error = f"branch_preparation_failed: {branch_info.get('error')}"
        elif fp.fix_type in (FixType.RESTART, FixType.SCALE):
            result = await k8s_exec.execute(incident, result)
        elif fp.fix_type == FixType.ROLLBACK:
            result = await k8s_exec.rollback(incident, result)
        elif fp.fix_type == FixType.INFRASTRUCTURE:
            result = await tf_exec.execute(incident, result)
        else:
            result.success = False
            result.error = f"unknown_fix_type: {fp.fix_type}"

        result.finished_at = datetime.now(timezone.utc)
        incident.state.execution_result = result
        incident.state.status = IncidentStatus.VERIFIED if result.success else IncidentStatus.FAILED
        incident.status = incident.state.status
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        await event_store.append(incident.incident_id, "execution", "execution_completed",
                                  payload={"success": result.success, "pr_url": result.pr_url})

        log.info("Incident %s execution %s (fix_type=%s, pr=%s)",
                 incident.incident_id,
                 "succeeded" if result.success else "failed",
                 fp.fix_type.value, result.pr_url)
    except Exception as e:  # noqa: BLE001
        log.exception("Execution failed: %s", e)
        result.success = False
        result.error = str(e)
        result.finished_at = datetime.now(timezone.utc)
        incident.state.execution_result = result
        incident.state.status = IncidentStatus.FAILED
        incident.status = IncidentStatus.FAILED
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "execution"}


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

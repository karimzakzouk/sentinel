"""Execution loop — consumes approved incidents, runs the fix (sandbox + executor).

This is the most complex loop because CODE_FIX has a 4-phase flow:
  1. prepare_branch — push fix files to a new Git branch
  2. Kaniko build — build a canary image with the fix applied
  3. Sandbox verification — deploy canary, run 3 verification levels
  4. open_pr — open a PR with the sandbox result
"""

from __future__ import annotations

import asyncio
import logging
from datetime import UTC, datetime
from typing import Any

from sentinel_common import ExecutionResult, FixType, Incident, IncidentStatus

log = logging.getLogger(__name__)


async def execution_loop(
    bus: Any,
    incident_store: Any,
    event_store: Any,
    k8s_exec: Any,
    gh_exec: Any,
    sandbox: Any,
    settings: Any,
) -> None:
    """Consume approved incidents, run the fix through the appropriate executor."""
    consumer = bus.consumer(settings.kafka_topic_fixes, "sentinel-core-execution")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                asyncio.create_task(
                    _execute_incident(incident, bus, incident_store, event_store, k8s_exec, gh_exec, sandbox, settings)
                )
            except Exception as e:
                log.warning("Execution consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Execution loop cancelled")


async def _execute_incident(
    incident: Incident,
    bus: Any,
    incident_store: Any,
    event_store: Any,
    k8s_exec: Any,
    gh_exec: Any,
    sandbox: Any,
    settings: Any,
) -> None:
    """Apply the approved fix through the right executor."""
    fp = incident.state.fix_proposal
    if fp is None:
        return

    # RBAC check
    if not settings.can_write_to(fp.target_namespace):
        log.warning("Execution denied for %s (namespace not in allowlist)", fp.target_namespace)
        incident.state.status = IncidentStatus.ESCALATED
        incident.status = IncidentStatus.ESCALATED
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        return

    incident.state.status = IncidentStatus.EXECUTING
    incident.status = IncidentStatus.EXECUTING
    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))

    result = ExecutionResult(
        fix_id=fp.fix_id,
        fix_type=fp.fix_type,
        success=False,
        started_at=datetime.now(UTC),
    )
    try:
        if fp.fix_type in (FixType.CODE_FIX, FixType.ROLL_FORWARD):
            result = await _execute_code_fix(incident, gh_exec, sandbox, settings)
        elif fp.fix_type in (FixType.RESTART, FixType.SCALE):
            result = await _execute_k8s_action(incident, k8s_exec, sandbox, settings, result)
        elif fp.fix_type == FixType.ROLLBACK:
            result = await k8s_exec.rollback(incident, result)
        else:
            result.success = False
            result.error = f"unknown_fix_type: {fp.fix_type}"

        result.finished_at = datetime.now(UTC)
        incident.state.execution_result = result
        incident.state.status = IncidentStatus.VERIFIED if result.success else IncidentStatus.FAILED
        incident.status = incident.state.status
        log.info(
            "Incident %s execution %s",
            incident.incident_id,
            "succeeded" if result.success else "failed",
        )
        await bus.publish_model(settings.kafka_topic_resolved, incident, key=incident.cluster.service)
    except Exception as e:
        log.exception("Execution failed: %s", e)
        result.success = False
        result.error = str(e)
        incident.state.status = IncidentStatus.FAILED
        incident.status = IncidentStatus.FAILED

    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))


async def _execute_code_fix(incident: Incident, gh_exec: Any, sandbox: Any, settings: Any) -> ExecutionResult:
    """4-phase code fix: branch → build → sandbox → PR."""
    fp = incident.state.fix_proposal

    # Phase 1: prepare branch
    log.info("Phase 1: Preparing fix branch...")
    branch_info = await gh_exec.prepare_branch(incident)
    if not branch_info.get("success"):
        return _failed_result(fp, f"branch_preparation_failed: {branch_info.get('error')}")

    if branch_info.get("files_changed", 0) == 0:
        log.warning("No files in fix proposal — LLM may have returned truncated JSON")
        return _failed_result(fp, "no_files_in_fix_proposal (LLM response may have been truncated)")

    # Phase 2 + 3: build canary + sandbox (only if Docker Hub creds available)
    sandbox_verified = False
    sandbox_summary = "skipped (no Docker Hub credentials)"

    if settings.docker_hub_token:
        log.info("Phase 2: Building canary image with Kaniko...")
        try:
            from kaniko import KanikoBuilder
            from kubernetes import client as k8s_client

            kaniko = KanikoBuilder(
                settings,
                k8s_client.AppsV1Api(),
                k8s_client.CoreV1Api(),
            )

            # Determine the image name + tag from the prod deployment
            from .k8s_compat import get_prod_image  # local import to avoid cycle

            prod_image = await get_prod_image(fp.target_service, fp.target_namespace)
            if prod_image:
                image_name = prod_image.split(":")[0]
                image_tag = f"sentinel-{str(fp.fix_id)[:8]}"

                fixed_files = {
                    fc["path"]: fc["new_content"] for fc in fp.files_to_change if "path" in fc and "new_content" in fc
                }

                canary_image = await kaniko.build(
                    repo=branch_info["repo"],
                    branch=branch_info["branch_name"],
                    fixed_files=fixed_files,
                    image_name=image_name,
                    image_tag=image_tag,
                    dockerfile_path=fp.dockerfile_path or "Dockerfile",
                )
                log.info("Canary image built: %s", canary_image)

                # Phase 3: sandbox
                log.info("Phase 3: Running sandbox verification...")
                fp.canary_image = canary_image
                sb = await sandbox.run(incident)
                sandbox_verified = sb.verified
                sandbox_summary = sb.summary
                log.info("Sandbox result: verified=%s (%s)", sandbox_verified, sandbox_summary)
            else:
                sandbox_summary = "skipped (could not find prod image)"
                log.warning("Skipping sandbox: could not find prod image for %s", fp.target_service)
        except Exception as e:
            sandbox_summary = f"sandbox_error: {e}"
            log.exception("Sandbox build/verification failed: %s", e)
    else:
        log.info("Skipping sandbox (no Docker Hub credentials set)")

    # Phase 4: open PR
    log.info("Phase 4: Opening PR...")
    result = await gh_exec.open_pr(incident, branch_info, sandbox_verified, sandbox_summary)
    return result


async def _execute_k8s_action(
    incident: Incident,
    k8s_exec: Any,
    sandbox: Any,
    settings: Any,
    result: ExecutionResult,
) -> ExecutionResult:
    """For restart/scale: sandbox first (test config change), then apply."""
    fp = incident.state.fix_proposal
    if fp.requires_sandbox and settings.enable_traffic_mirroring:
        sb = await sandbox.run(incident)
        result.sandbox_verified = sb.verified
        result.sandbox_summary = sb.summary
        if not sb.verified:
            result.success = False
            result.error = f"sandbox_failed: {sb.summary}"
            return result
    return await k8s_exec.execute(incident, result)


def _failed_result(fp: Any, error: str) -> ExecutionResult:
    """Build a failed ExecutionResult with the given error."""
    return ExecutionResult(
        fix_id=fp.fix_id,
        fix_type=fp.fix_type,
        success=False,
        error=error,
        started_at=datetime.now(UTC),
        finished_at=datetime.now(UTC),
    )

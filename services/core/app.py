"""HTTP API routes for Sentinel Core.

Mounted on the FastAPI app in main.py. Routes:
  - GET  /healthz           — liveness
  - GET  /readyz            — readiness (DB initialized + loops running)
  - GET  /metrics           — Prometheus metrics
  - GET  /incidents         — list recent incidents
  - GET  /incidents/{id}    — single incident
  - GET  /metrics-summary   — aggregated dashboard metrics
  - GET  /settings          — runtime settings (secrets masked)
  - POST /settings          — update runtime settings (persisted to K8s)
  - POST /admin/investigate — manually trigger an investigation
"""
from __future__ import annotations

import logging
from typing import Any
from uuid import UUID, uuid4

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from sentinel_common import Anomaly, AnomalyCluster, Severity
from .runtime_settings import SettingsUpdate

log = logging.getLogger(__name__)

router = APIRouter()


def _get_app_state() -> Any:
    """Late import to avoid circular dependency."""
    from .main import app_state

    return app_state


@router.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "core", "mode": "single-process"}


@router.get("/readyz")
async def readyz() -> dict[str, Any]:
    """Readiness — true after DB initialized AND all loops started."""
    state = _get_app_state()
    return {
        "status": "ready" if state.get("ready") else "not_ready",
        "db_initialized": state.get("db_initialized", False),
        "loops_started": state.get("loops_started", False),
        "weaviate_available": state.get("weaviate_available", False),
    }


@router.get("/metrics")
async def metrics() -> Any:
    from sentinel_common.metrics import metrics_endpoint

    return metrics_endpoint()


@router.get("/incidents")
async def list_incidents(limit: int = 50) -> list[dict[str, Any]]:
    state = _get_app_state()
    if state.get("incident_store") is None:
        raise HTTPException(503, "incident store not initialized")
    return await state["incident_store"].list_recent(limit)


@router.get("/incidents/{incident_id}")
async def get_incident(incident_id: UUID) -> dict[str, Any]:
    state = _get_app_state()
    if state.get("incident_store") is None:
        raise HTTPException(503, "incident store not initialized")
    inc = await state["incident_store"].get(incident_id)
    if inc is None:
        raise HTTPException(404, "incident not found")
    return inc


@router.get("/metrics-summary")
async def metrics_summary() -> dict[str, Any]:
    """Aggregated metrics for the dashboard."""
    state = _get_app_state()
    if state.get("incident_store") is None:
        return {"active": 0, "resolved_24h": 0, "failed": 0, "total": 0}
    incidents = await state["incident_store"].list_recent(100)
    active = [i for i in incidents if i["status"] in ("detected", "investigating", "root_caused", "fix_proposed", "governance_review", "approved", "executing")]
    resolved = [i for i in incidents if i["status"] in ("verified", "resolved")]
    failed = [i for i in incidents if i["status"] in ("failed", "rejected", "escalated")]
    return {
        "active": len(active),
        "resolved_24h": len(resolved),
        "failed": len(failed),
        "total": len(incidents),
    }


@router.get("/settings")
async def get_runtime_settings() -> dict[str, Any]:
    """Return current runtime settings (with secrets masked)."""
    state = _get_app_state()
    return state["runtime_settings"].to_masked_dict()


@router.post("/settings")
async def update_runtime_settings(update: SettingsUpdate) -> dict[str, Any]:
    """Update runtime settings. Changes apply immediately and persist to K8s."""
    state = _get_app_state()
    runtime = state["runtime_settings"]
    settings_obj = state["settings"]
    settings_store = state["settings_store"]

    update_dict = update.model_dump(exclude_unset=True)
    changed = runtime.apply_update(update_dict)

    # Sync runtime → Settings object so future code picks up the new values
    from .runtime_settings import apply_runtime_to_settings

    apply_runtime_to_settings(runtime, settings_obj)

    # Re-init GitHub executor if token changed
    if "github_token" in changed and state.get("gh_exec"):
        from github import Github

        state["gh_exec"].gh = Github(runtime.github_token) if runtime.github_token else None

    # Re-create the LLM router if any LLM setting changed
    if any(f in changed for f in ("llm_provider", "llm_api_key", "llm_fast_model", "llm_frontier_model")):
        from llm import LLMRouter

        state["llm_router"] = LLMRouter(settings_obj)
        log.info(
            "LLM router re-created with provider=%s, model=%s",
            settings_obj.llm_provider, settings_obj.llm_frontier_model,
        )

    # Persist to K8s ConfigMap + Secret
    if settings_store is not None and settings_store.available:
        try:
            await settings_store.save(runtime.model_dump())
            log.info("Settings persisted to K8s: %s", changed)
        except Exception as e:
            log.warning("Settings persist failed: %s", e)
    else:
        log.info("Settings updated (in-memory only): %s", changed)

    return {"status": "updated", "changed": changed}


@router.post("/admin/investigate")
async def trigger_investigation(spec: dict[str, Any]) -> dict[str, str]:
    """Manually trigger an investigation for a service."""
    state = _get_app_state()
    bus = state.get("bus")
    settings = state["settings"]
    if bus is None:
        raise HTTPException(503, "bus not initialized")

    service = spec.get("service", "unknown")
    namespace = spec.get("namespace", "default")
    reason = spec.get("reason", "manual trigger")

    anomaly = Anomaly(
        signal_id=uuid4(),
        service=service,
        namespace=namespace,
        detector="manual",
        score=0.8,
        dimensions={"manual": 1.0},
        explanation=f"Manual trigger: {reason}",
    )
    cluster = AnomalyCluster(
        service=service,
        namespace=namespace,
        anomalies=[anomaly],
        summary=f"Manual investigation: {reason}",
        severity=Severity.WARNING,
    )
    await bus.publish_model(settings.kafka_topic_anomalies, cluster, key=service)
    return {"status": "triggered", "service": service, "cluster_id": str(cluster.cluster_id)}

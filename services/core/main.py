"""
Sentinel Core — the single-process entrypoint that runs all 6 layers.

This is the "plug and play" mode. One pod, one process, one event loop.
No Kafka, no Zookeeper, no Strimzi. All layers communicate via an in-process
asyncio.Queue.

Architecture:
  K8s API events ──> ingestion task ──> queue ──> detection task ──> queue ──>
  reasoning task ──> queue ──> safety task ──> queue ──> execution task ──> queue ──>
  feedback task

The FastAPI app also serves:
  - /healthz        — liveness
  - /metrics        — Prometheus
  - /incidents      — list/get incidents (used by the UI)
  - /incidents/:id  — single incident
  - /api/settings   — get/set runtime settings
  - /admin/investigate — manually trigger an investigation

If you need horizontal scaling or Kafka durability, use mode=distributed
in the Helm chart instead.
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from typing import Any
from uuid import UUID, uuid4

# Make every service importable
_ROOT = os.path.dirname(os.path.abspath(__file__))
for svc in ["common", "ingestion", "detection", "reasoning", "safety", "execution", "feedback"]:
    sys.path.insert(0, os.path.join(_ROOT, "..", svc))

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel
import uvicorn

from sentinel_common import (
    AnomalyCluster, Incident, IncidentStatus, AgentState, Signal,
)
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, _InMemoryBus
from sentinel_common.db import IncidentStore, EventStore, BetaStore, init_db
from sentinel_common.metrics import metrics_endpoint, INCIDENTS_TOTAL
from sqlalchemy.ext.asyncio import create_async_engine

# Import the core logic from each service (not the FastAPI apps — those have
# their own lifespans and would conflict)
from normalizer import Normalizer
from detectors.isolation_forest import IsolationForestDetector
from detectors.cusum import CUSUMDetector
from detectors.rules import RulesDetector
from detectors.clusterer import AnomalyClusterer
from graph import build_graph
from context import ContextFetcher
from llm import LLMRouter
from executors.kubernetes import K8sExecutor
from executors.github import GitHubExecutor
from executors.terraform import TerraformExecutor
from sandbox import SandboxRunner
from weaviate_client import WeaviateStore
from beta_tracker import BetaTracker
from postmortem import PostmortemGenerator
from settings_store import SettingsStore

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
log = logging.getLogger("sentinel.core")

settings = get_settings()

# ─── Global state ───────────────────────────────────────────────────────────
bus: KafkaBus | None = None
incident_store: IncidentStore | None = None
event_store: EventStore | None = None
beta_store: BetaStore | None = None
engine: Any = None

# Detectors
if_detector: IsolationForestDetector | None = None
cusum_detector: CUSUMDetector | None = None
rules_detector: RulesDetector | None = None
clusterer: AnomalyClusterer | None = None
normalizer: Normalizer | None = None

# Reasoning
graph: Any = None
context_fetcher: ContextFetcher | None = None
llm_router: LLMRouter | None = None

# Execution
k8s_exec: K8sExecutor | None = None
gh_exec: GitHubExecutor | None = None
tf_exec: TerraformExecutor | None = None
sandbox: SandboxRunner | None = None

# Feedback
weaviate: WeaviateStore | None = None
beta_tracker: BetaTracker | None = None
postmortem_gen: PostmortemGenerator | None = None

# Settings persistence
settings_store: SettingsStore | None = None

# Background tasks
tasks: list[asyncio.Task] = []


# ─── Settings ───────────────────────────────────────────────────────────────
# Runtime-configurable settings persisted to a ConfigMap (via the Settings API)
RUNTIME_SETTINGS: dict[str, Any] = {
    "github_token": "",
    "llm_provider": "gemini",
    "llm_api_key": "",
    "llm_fast_model": "gemini-2.0-flash",
    "llm_frontier_model": "gemini-2.5-pro",
    "prometheus_url": "",
    "loki_url": "",
    "jaeger_url": "",
    "production_namespaces": "prod,production,payments",
    "write_namespaces": "",
    "confidence_threshold_autonomous": 0.92,
    "confidence_threshold_human_review": 0.70,
    "panic_cpu_threshold_pct": 95.0,
    "panic_connections_threshold": 9000,
}


def _load_settings_from_env() -> None:
    """Load initial settings from env vars (set by the Helm chart)."""
    RUNTIME_SETTINGS["github_token"] = os.getenv("SENTINEL_GITHUB_TOKEN", "")
    RUNTIME_SETTINGS["llm_provider"] = os.getenv("SENTINEL_LLM_PROVIDER", "gemini")
    RUNTIME_SETTINGS["llm_api_key"] = os.getenv("SENTINEL_LLM_API_KEY", "")
    RUNTIME_SETTINGS["llm_fast_model"] = os.getenv("SENTINEL_LLM_FAST_MODEL", "gemini-2.0-flash")
    RUNTIME_SETTINGS["llm_frontier_model"] = os.getenv("SENTINEL_LLM_FRONTIER_MODEL", "gemini-2.5-pro")
    RUNTIME_SETTINGS["prometheus_url"] = os.getenv("PROMETHEUS_URL", "")
    RUNTIME_SETTINGS["loki_url"] = os.getenv("LOKI_URL", "")
    RUNTIME_SETTINGS["jaeger_url"] = os.getenv("JAEGER_URL", "")
    RUNTIME_SETTINGS["production_namespaces"] = os.getenv("SENTINEL_PRODUCTION_NAMESPACES", "prod,production,payments")
    RUNTIME_SETTINGS["write_namespaces"] = os.getenv("SENTINEL_WRITE_NAMESPACES", "")
    RUNTIME_SETTINGS["confidence_threshold_autonomous"] = float(os.getenv("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS", "0.92"))
    RUNTIME_SETTINGS["confidence_threshold_human_review"] = float(os.getenv("SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW", "0.70"))
    RUNTIME_SETTINGS["panic_cpu_threshold_pct"] = float(os.getenv("SENTINEL_PANIC_CPU_THRESHOLD_PCT", "95.0"))
    RUNTIME_SETTINGS["panic_connections_threshold"] = int(os.getenv("SENTINEL_PANIC_CONNECTIONS_THRESHOLD", "9000"))


# ─── Lifespan: start all the things ─────────────────────────────────────────
@asynccontextmanager
async def lifespan(app: FastAPI):
    global bus, incident_store, event_store, beta_store, engine
    global if_detector, cusum_detector, rules_detector, clusterer, normalizer
    global graph, context_fetcher, llm_router
    global k8s_exec, gh_exec, tf_exec, sandbox
    global weaviate, beta_tracker, postmortem_gen

    log.info("Starting Sentinel Core (single-process mode)")
    _load_settings_from_env()

    # Load persisted settings from K8s ConfigMap + Secret (overrides env vars)
    global settings_store
    namespace = os.getenv("SENTINEL_NAMESPACE", "sentinel")
    release_name = os.getenv("SENTINEL_RELEASE_NAME", "sentinel")
    settings_store = SettingsStore(namespace=namespace, name_prefix=release_name)
    if settings_store.available:
        persisted = await settings_store.load()
        if persisted:
            log.info("Applying %d persisted settings from K8s", len(persisted))
            RUNTIME_SETTINGS.update(persisted)
            # Apply to the Settings object
            if "production_namespaces" in persisted:
                settings.production_namespaces = persisted["production_namespaces"]
            if "write_namespaces" in persisted:
                settings.write_namespaces = persisted["write_namespaces"]
            if "github_token" in persisted:
                settings.github_token = persisted["github_token"]
            if "llm_api_key" in persisted:
                settings.llm_api_key = persisted["llm_api_key"]
            if "llm_provider" in persisted:
                settings.llm_provider = persisted["llm_provider"]
            if "llm_fast_model" in persisted:
                settings.llm_fast_model = persisted["llm_fast_model"]
            if "llm_frontier_model" in persisted:
                settings.llm_frontier_model = persisted["llm_frontier_model"]

    # 1. In-memory message bus (replaces Kafka)
    bus = KafkaBus(brokers="", schema_registry=None)
    bus._mem = _InMemoryBus()
    bus._producer = None
    log.info("In-memory bus initialized")

    # 2. Database
    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, echo=False, pool_size=5, max_overflow=10)
    await init_db(engine)
    incident_store = IncidentStore(engine)
    event_store = EventStore(engine)
    beta_store = BetaStore(engine)
    log.info("Database initialized")

    # 3. Detectors
    if_detector = IsolationForestDetector(contamination=0.05, min_samples=50)
    cusum_detector = CUSUMDetector(threshold=5.0, drift=0.02)
    rules_detector = RulesDetector(
        cpu_critical_pct=settings.panic_cpu_threshold_pct,
        conn_critical=settings.panic_connections_threshold,
    )
    clusterer = AnomalyClusterer(window_seconds=60)
    normalizer = Normalizer()
    log.info("Detectors initialized")

    # 4. Reasoning
    llm_router = LLMRouter(settings)
    context_fetcher = ContextFetcher(settings, llm_router)
    graph = build_graph(
        llm_router=llm_router,
        context_fetcher=context_fetcher,
        incident_store=incident_store,
        event_store=event_store,
        beta_store=beta_store,
    )
    log.info("Reasoning graph initialized")

    # 5. Execution
    k8s_exec = K8sExecutor(settings)
    gh_exec = GitHubExecutor(settings)
    tf_exec = TerraformExecutor(settings)
    sandbox = SandboxRunner(settings, k8s_exec)
    log.info("Executors initialized")

    # 6. Feedback
    weaviate = WeaviateStore(settings)
    await weaviate.ensure_schema()
    beta_tracker = BetaTracker(beta_store)
    postmortem_gen = PostmortemGenerator(settings)
    log.info("Feedback layer initialized")

    # 7. Start background tasks — one per layer
    tasks.append(asyncio.create_task(_ingestion_loop(), name="ingestion"))
    tasks.append(asyncio.create_task(_detection_loop(), name="detection"))
    tasks.append(asyncio.create_task(_reasoning_loop(), name="reasoning"))
    tasks.append(asyncio.create_task(_safety_loop(), name="safety"))
    tasks.append(asyncio.create_task(_execution_loop(), name="execution"))
    tasks.append(asyncio.create_task(_feedback_loop(), name="feedback"))
    log.info("All 6 background tasks started")

    log.info("Sentinel Core ready — single-process mode active")
    yield

    # Shutdown
    for t in tasks:
        t.cancel()
    await asyncio.gather(*tasks, return_exceptions=True)
    if engine:
        await engine.dispose()
    log.info("Sentinel Core stopped")


app = FastAPI(title="Sentinel Core", version="2.0.0", lifespan=lifespan)
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


# ─── Background loops ───────────────────────────────────────────────────────

async def _ingestion_loop() -> None:
    """Watch K8s events + (optionally) poll Prometheus/Loki, normalize, publish signals."""
    assert bus is not None and normalizer is not None
    from collectors.k8s_events import K8sEventsCollector
    k8s_collector = K8sEventsCollector()
    last_run = 0.0
    while True:
        try:
            now = datetime.now(timezone.utc).timestamp()
            if now - last_run >= 15:  # poll every 15s
                last_run = now
                raw_batch = []
                # K8s events
                if k8s_collector._available:
                    raw_batch.extend(await k8s_collector.poll())
                # Prometheus (if configured)
                if RUNTIME_SETTINGS["prometheus_url"]:
                    # Use the prometheus collector inline
                    from collectors.prometheus import PrometheusCollector
                    pc = PrometheusCollector(RUNTIME_SETTINGS["prometheus_url"])
                    raw_batch.extend(await pc.poll())
                    await pc.close()
                for raw in raw_batch:
                    try:
                        sig = normalizer.normalize(raw)
                        await bus.publish_model(settings.kafka_topic_signals, sig, key=sig.service)
                    except Exception as e:
                        log.warning("Signal normalize failed: %s", e)
            await asyncio.sleep(5)
        except asyncio.CancelledError:
            break
        except Exception as e:
            log.exception("Ingestion loop error: %s", e)
            await asyncio.sleep(10)


async def _detection_loop() -> None:
    """Consume signals, run detectors, publish anomaly clusters."""
    assert bus is not None and if_detector is not None and cusum_detector is not None
    assert rules_detector is not None and clusterer is not None
    consumer = bus.consumer(settings.kafka_topic_signals, "sentinel-core-detection")
    try:
        async for raw in consumer:
            try:
                sig = Signal.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                anomalies = []
                af = await if_detector.observe(sig)
                if af:
                    anomalies.append(af)
                for cf in await cusum_detector.observe(sig):
                    anomalies.append(cf)
                for rf in await rules_detector.observe(sig):
                    anomalies.append(rf)
                for anomaly in anomalies:
                    cluster = clusterer.add(anomaly)
                    if cluster is not None:
                        await bus.publish_model(settings.kafka_topic_anomalies, cluster, key=cluster.service)
                        log.info("Cluster published: %s (%d anomalies)", cluster.service, len(cluster.anomalies))
            except Exception as e:
                log.warning("Detection error: %s", e)
    except asyncio.CancelledError:
        log.info("Detection loop cancelled")


async def _reasoning_loop() -> None:
    """Consume anomaly clusters, run the LangGraph, publish incidents."""
    assert bus is not None and graph is not None and incident_store is not None
    assert event_store is not None
    consumer = bus.consumer(settings.kafka_topic_anomalies, "sentinel-core-reasoning")
    try:
        async for raw in consumer:
            try:
                cluster = AnomalyCluster.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                asyncio.create_task(_process_cluster(cluster))
            except Exception as e:
                log.warning("Reasoning consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Reasoning loop cancelled")


async def _process_cluster(cluster: AnomalyCluster) -> None:
    """Run the full reasoning graph for one cluster."""
    assert incident_store is not None and event_store is not None and bus is not None
    incident_id = uuid4()
    state = AgentState(incident_id=incident_id, cluster=cluster, status=IncidentStatus.INVESTIGATING)
    incident = Incident(incident_id=incident_id, cluster=cluster, state=state, status=IncidentStatus.INVESTIGATING)
    await event_store.append(incident_id, "reasoning", "investigation_started",
                              payload={"cluster_summary": cluster.summary})
    await incident_store.upsert(incident.model_dump(mode="json"))

    try:
        timeout = int(os.getenv("SENTINEL_INVESTIGATION_TIMEOUT", "300"))
        final_state = await asyncio.wait_for(
            graph.ainvoke(state.model_dump(mode="json")),
            timeout=timeout,
        )
        state = AgentState.model_validate(final_state)
        incident.state = state
        incident.status = state.status
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        await event_store.append(incident_id, "reasoning", "investigation_completed",
                                  agent="orchestrator", payload={"status": state.status})
        # Forward to safety
        await bus.publish_model(settings.kafka_topic_incidents, incident, key=cluster.service)
        INCIDENTS_TOTAL.labels(status=state.status, service=cluster.service).inc()
        log.info("Incident %s published (status=%s)", incident_id, state.status)
    except asyncio.TimeoutError:
        log.error("Investigation %s timed out", incident_id)
        incident.status = IncidentStatus.FAILED
        incident.state.error = f"timeout: {timeout}s"
        await incident_store.upsert(incident.model_dump(mode="json"))
    except Exception as e:
        log.exception("Reasoning failed: %s", e)
        incident.status = IncidentStatus.FAILED
        await incident_store.upsert(incident.model_dump(mode="json"))


async def _safety_loop() -> None:
    """Consume incidents, evaluate OPA policies, forward approved fixes to execution."""
    assert bus is not None and incident_store is not None and event_store is not None
    from sentinel_common.opa import OPAClient
    opa = OPAClient(settings.opa_url)
    consumer = bus.consumer(settings.kafka_topic_incidents, "sentinel-core-safety")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.state.fix_proposal is None:
                    continue
                asyncio.create_task(_safety_evaluate(incident, opa))
            except Exception as e:
                log.warning("Safety consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Safety loop cancelled")


async def _safety_evaluate(incident: Incident, opa: Any) -> None:
    """Run OPA decision + route to execution or hold for human approval."""
    assert bus is not None and incident_store is not None and event_store is not None
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
        result = await opa.decide("sentinel/decision", opa_input)
    except Exception as e:
        log.warning("OPA failed: %s — treating as require_human_approval", e)
        result = {"verdict": "require_human_approval", "rules_evaluated": [], "confidence_at_decision": fp.proposed_confidence, "rollback_blocked": False}

    from sentinel_common import GovernanceDecision, GovernanceVerdict
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
    await event_store.append(incident.incident_id, "safety", "governance_decision",
                              agent="safety", payload=decision.model_dump())


async def _execution_loop() -> None:
    """Consume approved incidents, run the fix (sandbox + executor)."""
    assert bus is not None and incident_store is not None and event_store is not None
    consumer = bus.consumer(settings.kafka_topic_fixes, "sentinel-core-execution")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                asyncio.create_task(_execute_incident(incident))
            except Exception as e:
                log.warning("Execution consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Execution loop cancelled")


async def _execute_incident(incident: Incident) -> None:
    """Apply the approved fix through the right executor."""
    assert incident_store is not None and event_store is not None
    assert k8s_exec is not None and gh_exec is not None and sandbox is not None
    from sentinel_common import ExecutionResult, FixType

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

    result = ExecutionResult(fix_id=fp.fix_id, fix_type=fp.fix_type, success=False,
                              started_at=datetime.now(timezone.utc))
    try:
        # Sandbox first
        if fp.requires_sandbox and settings.enable_traffic_mirroring:
            sb = await sandbox.run(incident)
            result.sandbox_verified = sb.verified
            result.sandbox_summary = sb.summary
            if not sb.verified:
                result.success = False
                result.error = f"sandbox_failed: {sb.summary}"
                incident.state.execution_result = result
                incident.state.status = IncidentStatus.FAILED
                incident.status = IncidentStatus.FAILED
                incident.touch()
                await incident_store.upsert(incident.model_dump(mode="json"))
                return
        # Execute
        if fp.fix_type in (FixType.CODE_FIX, FixType.ROLL_FORWARD):
            result = await gh_exec.execute(incident, result)
        elif fp.fix_type in (FixType.RESTART, FixType.SCALE):
            result = await k8s_exec.execute(incident, result)
        elif fp.fix_type == FixType.ROLLBACK:
            result = await k8s_exec.rollback(incident, result)
        elif fp.fix_type == FixType.INFRASTRUCTURE:
            result = await tf_exec.execute(incident, result)

        result.finished_at = datetime.now(timezone.utc)
        incident.state.execution_result = result
        incident.state.status = IncidentStatus.VERIFIED if result.success else IncidentStatus.FAILED
        incident.status = incident.state.status
        log.info("Incident %s execution %s", incident.incident_id,
                 "succeeded" if result.success else "failed")
    except Exception as e:
        log.exception("Execution failed: %s", e)
        result.success = False
        result.error = str(e)
        incident.state.status = IncidentStatus.FAILED
        incident.status = IncidentStatus.FAILED

    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))


async def _feedback_loop() -> None:
    """Consume completed incidents, update Beta tracker, store in Weaviate, write postmortem."""
    assert bus is not None and incident_store is not None and event_store is not None
    assert weaviate is not None and beta_tracker is not None and postmortem_gen is not None
    consumer = bus.consumer(settings.kafka_topic_incidents, "sentinel-core-feedback")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.status in (IncidentStatus.VERIFIED, IncidentStatus.FAILED, IncidentStatus.RESOLVED):
                    asyncio.create_task(_feedback_process(incident))
            except Exception as e:
                log.warning("Feedback consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Feedback loop cancelled")


async def _feedback_process(incident: Incident) -> None:
    """Update beta tracker, store in Weaviate, write postmortem."""
    assert beta_tracker is not None and weaviate is not None and postmortem_gen is not None
    assert incident_store is not None and event_store is not None
    fp = incident.state.fix_proposal
    rc = incident.state.root_cause
    if fp is None or rc is None:
        return

    success = incident.status == IncidentStatus.VERIFIED
    alpha, beta = await beta_tracker.record(fp.target_service, fp.fix_type.value, success)
    incident.beta_alpha = alpha
    incident.beta_beta = beta

    weaviate_uuid = await weaviate.store_incident(incident)
    incident.weaviate_uuid = weaviate_uuid

    pm = await postmortem_gen.generate(incident)
    incident.postmortem = pm

    incident.status = IncidentStatus.RESOLVED
    incident.state.status = IncidentStatus.RESOLVED
    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))
    log.info("Incident %s resolved (alpha=%.0f, beta=%.0f)", incident.incident_id, alpha, beta)


# ─── HTTP API ───────────────────────────────────────────────────────────────

@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "core", "mode": "single-process"}


@app.get("/metrics")
async def _metrics():
    return metrics_endpoint()


@app.get("/incidents")
async def list_incidents(limit: int = 50) -> list[dict[str, Any]]:
    assert incident_store is not None
    return await incident_store.list_recent(limit)


@app.get("/incidents/{incident_id}")
async def get_incident(incident_id: UUID) -> dict[str, Any]:
    assert incident_store is not None
    inc = await incident_store.get(incident_id)
    if inc is None:
        raise HTTPException(404, "incident not found")
    return inc


@app.get("/metrics-summary")
async def metrics_summary() -> dict[str, Any]:
    """Aggregated metrics for the dashboard."""
    assert incident_store is not None
    incidents = await incident_store.list_recent(100)
    active = [i for i in incidents if i["status"] in ("detected", "investigating", "root_caused", "fix_proposed", "governance_review", "approved", "executing")]
    resolved = [i for i in incidents if i["status"] in ("verified", "resolved")]
    failed = [i for i in incidents if i["status"] in ("failed", "rejected", "escalated")]
    return {
        "active": len(active),
        "resolved_24h": len(resolved),
        "failed": len(failed),
        "total": len(incidents),
    }


# ─── Settings API ───────────────────────────────────────────────────────────

class SettingsUpdate(BaseModel):
    github_token: str | None = None
    llm_provider: str | None = None
    llm_api_key: str | None = None
    llm_fast_model: str | None = None
    llm_frontier_model: str | None = None
    prometheus_url: str | None = None
    loki_url: str | None = None
    jaeger_url: str | None = None
    production_namespaces: str | None = None
    write_namespaces: str | None = None
    confidence_threshold_autonomous: float | None = None
    confidence_threshold_human_review: float | None = None
    panic_cpu_threshold_pct: float | None = None
    panic_connections_threshold: int | None = None


@app.get("/settings")
async def get_runtime_settings() -> dict[str, Any]:
    """Return current runtime settings (with secrets masked)."""
    out = dict(RUNTIME_SETTINGS)
    # Mask secrets
    if out.get("github_token"):
        out["github_token"] = out["github_token"][:4] + "***" if len(out["github_token"]) > 4 else "***"
    if out.get("llm_api_key"):
        out["llm_api_key"] = out["llm_api_key"][:4] + "***" if len(out["llm_api_key"]) > 4 else "***"
    out["github_token_set"] = bool(RUNTIME_SETTINGS.get("github_token"))
    out["llm_api_key_set"] = bool(RUNTIME_SETTINGS.get("llm_api_key"))
    return out


@app.post("/settings")
async def update_runtime_settings(update: SettingsUpdate) -> dict[str, Any]:
    """Update runtime settings. Changes apply immediately (no restart needed
    for most settings; LLM provider changes take effect on the next LLM call).
    """
    changed = []
    for field, value in update.model_dump(exclude_unset=True).items():
        if value is None:
            continue
        # Don't overwrite secrets with masked values
        if field in ("github_token", "llm_api_key") and value.endswith("***"):
            continue
        if RUNTIME_SETTINGS.get(field) != value:
            RUNTIME_SETTINGS[field] = value
            changed.append(field)
            # Update the Settings object too so future code picks it up
            if field == "production_namespaces":
                settings.production_namespaces = value
            elif field == "write_namespaces":
                settings.write_namespaces = value
            elif field == "confidence_threshold_autonomous":
                settings.confidence_threshold_autonomous = value
            elif field == "confidence_threshold_human_review":
                settings.confidence_threshold_human_review = value
            elif field == "panic_cpu_threshold_pct":
                settings.panic_cpu_threshold_pct = value
            elif field == "panic_connections_threshold":
                settings.panic_connections_threshold = value
            elif field == "github_token":
                settings.github_token = value
                # Re-init GitHub executor
                global gh_exec
                if gh_exec:
                    from github import Github
                    gh_exec.gh = Github(value) if value else None
            elif field == "llm_provider":
                settings.llm_provider = value
            elif field == "llm_api_key":
                settings.llm_api_key = value
            elif field == "llm_fast_model":
                settings.llm_fast_model = value
            elif field == "llm_frontier_model":
                settings.llm_frontier_model = value
    # Persist to K8s ConfigMap + Secret (survives pod restarts)
    if settings_store is not None and settings_store.available:
        try:
            await settings_store.save(RUNTIME_SETTINGS)
            log.info("Settings persisted to K8s: %s", changed)
        except Exception as e:
            log.warning("Settings persist failed: %s", e)
    else:
        log.info("Settings updated (in-memory only): %s", changed)
    return {"status": "updated", "changed": changed}


@app.post("/admin/investigate")
async def trigger_investigation(spec: dict[str, Any]) -> dict[str, str]:
    """Manually trigger an investigation for a service."""
    assert bus is not None and normalizer is not None
    service = spec.get("service", "unknown")
    namespace = spec.get("namespace", "default")
    reason = spec.get("reason", "manual trigger")
    # Build a synthetic anomaly cluster and publish it
    from sentinel_common import Anomaly, Severity
    from datetime import datetime as dt
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


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=int(os.getenv("PORT", "8000")),
        log_level="info",
    )

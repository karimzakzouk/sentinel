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
# IsolationForestDetector requires scikit-learn (150 MB). In core mode we
# don't need it — the CRD watcher + rules detector handle incidents. Make
# the import conditional so the core image can be smaller.
try:
    from detectors.isolation_forest import IsolationForestDetector
    HAS_SKLEARN = True
except ImportError:
    IsolationForestDetector = None  # type: ignore
    HAS_SKLEARN = False
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
    if HAS_SKLEARN and IsolationForestDetector is not None:
        if_detector = IsolationForestDetector(contamination=0.05, min_samples=50)
        log.info("IsolationForest detector initialized")
    else:
        if_detector = None
        log.info("IsolationForest detector skipped (scikit-learn not installed)")
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
    tasks.append(asyncio.create_task(_crd_watcher_loop(), name="crd_watcher"))
    log.info("All 7 background tasks started (including CRD watcher)")

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

# Annotation keys Sentinel recognizes on workloads
_SENTINEL_ANNOTATION_KEYS = (
    "sentinel.io/repo",
    "sentinel.io/runbook",
    "sentinel.io/team",
    "sentinel.io/ignore",
    "sentinel.io/owner",
    "sentinel.io/criticality",
)


async def _lookup_workload_annotations(service: str, namespace: str) -> dict[str, str]:
    """Look up sentinel.io/* annotations from the workload matching `service`.

    Tries (in order):
      1. Deployment named exactly `service` or `service-sentinel-sim` (Helm convention)
      2. Deployment with label app.kubernetes.io/name=service
      3. StatefulSet named `service`
      4. Any Pod with label app.kubernetes.io/name=service (and walks up to its owner)

    Returns {} if K8s API is unavailable or no matching workload is found.
    Logs at debug level so it doesn't spam — this runs for every CR.
    """
    from kubernetes import client
    try:
        apps = client.AppsV1Api()
        core = client.CoreV1Api()
    except Exception:
        return {}

    # Try a few likely Deployment names
    candidate_names = {service, f"{service}-sentinel-sim"}
    annotations: dict[str, str] = {}

    # 1. Try by name (most common — Helm chart names deployments predictably)
    for name in candidate_names:
        try:
            dep = await asyncio.to_thread(apps.read_namespaced_deployment, name, namespace)
            ann = dep.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            # Also check pod template annotations
            tmpl_ann = dep.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
        except Exception:
            pass  # not found, try next

    # 2. Try by label selector
    try:
        deps = await asyncio.to_thread(
            apps.list_namespaced_deployment, namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        for dep in (deps.items or [])[:1]:  # just take the first match
            ann = dep.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            tmpl_ann = dep.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
    except Exception:
        pass

    # 3. Try StatefulSet by name
    for name in candidate_names:
        try:
            sts = await asyncio.to_thread(apps.read_namespaced_stateful_set, name, namespace)
            ann = sts.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            tmpl_ann = sts.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
        except Exception:
            pass

    # 4. Try Pods (last resort)
    try:
        pods = await asyncio.to_thread(
            core.list_namespaced_pod, namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        for pod in (pods.items or [])[:1]:
            ann = pod.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in _SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
    except Exception:
        pass

    return annotations


async def _fetch_crashing_pod_logs(service: str, namespace: str) -> str:
    """Fetch logs from the most recent crashing pod for a service.

    Finds pods matching the service label, picks the one with the most
    restarts (or in CrashLoopBackOff), and fetches its previous container
    logs (the logs from before the last crash).

    Returns the last 4KB of log text, or empty string if no pods found.
    """
    from kubernetes import client
    try:
        core = client.CoreV1Api()
        # Find pods by label app.kubernetes.io/name=service
        pods = await asyncio.to_thread(
            core.list_namespaced_pod,
            namespace=namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        if not pods.items:
            # Try app=service label
            pods = await asyncio.to_thread(
                core.list_namespaced_pod,
                namespace=namespace,
                label_selector=f"app={service}",
            )
        if not pods.items:
            return ""

        # Pick the pod with the most restarts or in CrashLoopBackOff
        worst_pod = None
        worst_restarts = -1
        for pod in pods.items:
            restarts = sum(cs.restart_count for cs in (pod.status.container_statuses or []))
            phase = pod.status.phase
            if phase == "Pending" or "CrashLoopBackOff" in str(pod.status.container_statuses or ""):
                # Prefer CrashLoopBackOff pods
                worst_pod = pod
                break
            if restarts > worst_restarts:
                worst_restarts = restarts
                worst_pod = pod

        if worst_pod is None:
            return ""

        pod_name = worst_pod.metadata.name
        log.info("CRD watcher: fetching logs from pod %s (restarts=%d)", pod_name, worst_restarts)

        # Fetch previous container logs (the crash output)
        try:
            logs = await asyncio.to_thread(
                core.read_namespaced_pod_log,
                name=pod_name,
                namespace=namespace,
                previous=True,  # Get logs from before the last crash
                limit_bytes=4096,  # Last 4KB
            )
            return logs or ""
        except Exception:
            # If --previous fails (pod might not have previous logs), try current
            try:
                logs = await asyncio.to_thread(
                    core.read_namespaced_pod_log,
                    name=pod_name,
                    namespace=namespace,
                    limit_bytes=4096,
                )
                return logs or ""
            except Exception as e:
                log.warning("CRD watcher: could not fetch logs from %s: %s", pod_name, e)
                return ""
    except Exception as e:
        log.warning("CRD watcher: error fetching pod logs: %s", e)
        return ""


async def _crd_watcher_loop() -> None:
    """Watch for SentinelIncident CRs and trigger investigations automatically.

    This replaces the kopf operator in core mode. Every 10 seconds, it lists
    SentinelIncident CRs and triggers investigations for any that haven't
    been processed yet (no status.phase set).
    """
    from kubernetes import client
    try:
        coa = client.CustomObjectsApi()
    except Exception:
        log.warning("CRD watcher: K8s API not available")
        return

    seen: set[str] = set()
    while True:
        try:
            # List SentinelIncident CRs across all namespaces
            crs = await asyncio.to_thread(
                coa.list_cluster_custom_object,
                group="sentinel.io",
                version="v1alpha1",
                plural="sentinelincidents",
            )
            for item in crs.get("items", []):
                name = item["metadata"]["name"]
                ns = item["metadata"]["namespace"]
                key = f"{ns}/{name}"
                if key in seen:
                    continue
                # Check if already processed (has status.phase)
                status = item.get("status", {})
                if status.get("phase"):
                    seen.add(key)
                    continue
                # New incident — trigger investigation
                spec = item.get("spec", {})
                service = spec.get("service", "unknown")
                svc_ns = spec.get("namespace", ns)
                reason = spec.get("reason", "triggered by CRD")
                repo_from_spec = spec.get("repo")  # CR allows caller to specify the repo directly
                log.info("CRD watcher: new SentinelIncident %s (service=%s, reason=%s)", key, service, reason)

                # Discover sentinel.io/* annotations from the affected workload
                # so the Fix Proposer can find the GitHub repo. Walks:
                #   1. spec.repo (if set on the CR — highest priority)
                #   2. sentinel.io/repo annotation on the Deployment/StatefulSet
                #      matching `service` in namespace `svc_ns`
                #   3. sentinel.io/repo annotation on any Pod owned by that Deployment
                workload_annotations = await _lookup_workload_annotations(service, svc_ns)
                if repo_from_spec:
                    workload_annotations["sentinel.io/repo"] = repo_from_spec
                if workload_annotations:
                    log.info("CRD watcher: discovered annotations for %s: %s",
                             service, list(workload_annotations.keys()))

                # Fetch REAL pod logs from the crashing pods.
                # This is critical — without actual error messages, the LLM
                # has to guess what file to fix. With real logs (including
                # stack traces), the Investigator can parse file paths and
                # fetch the actual source code from GitHub.
                pod_logs = await _fetch_crashing_pod_logs(service, svc_ns)
                if pod_logs:
                    log.info("CRD watcher: fetched %d chars of pod logs for %s", len(pod_logs), service)

                # Build a synthetic anomaly cluster with REAL pod log data
                from sentinel_common import Anomaly, AnomalyCluster, Severity
                # Include the pod logs in the explanation so the Investigator
                # can parse stack traces and identify source files.
                full_explanation = f"SentinelIncident CR: {reason}"
                if pod_logs:
                    full_explanation += f"\n\n## Pod Logs (last 4KB)\n{pod_logs}"

                anomaly = Anomaly(
                    signal_id=uuid4(),
                    service=service,
                    namespace=svc_ns,
                    detector="crd_watcher",
                    score=0.8,
                    dimensions={"manual": 1.0},
                    explanation=full_explanation,
                    annotations=workload_annotations,
                )
                cluster = AnomalyCluster(
                    service=service,
                    namespace=svc_ns,
                    anomalies=[anomaly],
                    summary=f"SentinelIncident: {reason}",
                    severity=Severity.WARNING,
                    annotations=workload_annotations,
                )
                if bus is not None:
                    await bus.publish_model(settings.kafka_topic_anomalies, cluster, key=service)
                seen.add(key)
            await asyncio.sleep(10)
        except asyncio.CancelledError:
            break
        except Exception as e:
            log.warning("CRD watcher error: %s", e)
            await asyncio.sleep(15)


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
    assert bus is not None and cusum_detector is not None
    assert rules_detector is not None and clusterer is not None
    consumer = bus.consumer(settings.kafka_topic_signals, "sentinel-core-detection")
    try:
        async for raw in consumer:
            try:
                sig = Signal.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                anomalies = []
                # Isolation Forest is optional (requires scikit-learn)
                if if_detector is not None:
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
        # Query the verdict directly — the 'decision' object rule doesn't
        # always evaluate correctly via POST /v1/data/sentinel/decision.
        # Querying sentinel/verdict returns the string directly.
        verdict_result = await opa.decide("sentinel/verdict", opa_input)
        # verdict_result is {"result": "allow"} or {"result": "deny"} etc.
        # The OPAClient.decide already extracts .get("result", {}),
        # so verdict_result is the string "allow" / "deny" / etc.
        verdict_str = verdict_result if isinstance(verdict_result, str) else (
            verdict_result.get("verdict") if isinstance(verdict_result, dict) else "deny"
        )
        # Also get the rules_evaluated for audit
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
                              agent="safety", payload=decision.model_dump(mode="json"))


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
        if fp.fix_type in (FixType.CODE_FIX, FixType.ROLL_FORWARD):
            # ── Code fix flow: branch → build → sandbox → PR ──
            # 1. Create branch + push the fix
            log.info("Phase 1: Preparing fix branch...")
            branch_info = await gh_exec.prepare_branch(incident)
            if not branch_info.get("success"):
                result.error = f"branch_preparation_failed: {branch_info.get('error')}"
                incident.state.execution_result = result
                incident.state.status = IncidentStatus.FAILED
                incident.status = IncidentStatus.FAILED
                incident.touch()
                await incident_store.upsert(incident.model_dump(mode="json"))
                return

            # Guard: if no files were pushed, the PR will fail with "No commits"
            if branch_info.get("files_changed", 0) == 0:
                log.warning("No files in fix proposal — LLM may have returned truncated JSON")
                result.error = "no_files_in_fix_proposal (LLM response may have been truncated)"
                incident.state.execution_result = result
                incident.state.status = IncidentStatus.FAILED
                incident.status = IncidentStatus.FAILED
                incident.touch()
                await incident_store.upsert(incident.model_dump(mode="json"))
                return

            # 2. Build the canary image with Kaniko (if Docker Hub creds available)
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

                    # Determine the image name + tag
                    # Image name comes from the prod deployment's image
                    # (e.g. "karimzakzouk/sentinel-sim")
                    prod_image = await _get_prod_image(fp.target_service, fp.target_namespace)
                    if prod_image:
                        image_name = prod_image.split(":")[0]
                        image_tag = f"sentinel-{str(fp.fix_id)[:8]}"

                        # Build fixed files dict from the fix proposal
                        fixed_files = {
                            fc["path"]: fc["new_content"]
                            for fc in fp.files_to_change
                            if "path" in fc and "new_content" in fc
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

                        # 3. Run sandbox with the canary image
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

            # 4. Open the PR (regardless of sandbox outcome)
            log.info("Phase 4: Opening PR...")
            result = await gh_exec.open_pr(incident, branch_info, sandbox_verified, sandbox_summary)

        elif fp.fix_type in (FixType.RESTART, FixType.SCALE):
            # For restart/scale: sandbox first (test config change), then apply
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


async def _get_prod_image(service: str, namespace: str) -> str | None:
    """Get the current production image for a service by looking up its Deployment."""
    from kubernetes import client
    try:
        apps = client.AppsV1Api()
        # Try by label app.kubernetes.io/name=service (Helm standard)
        deps = await asyncio.to_thread(
            apps.list_namespaced_deployment,
            namespace=namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        if deps.items:
            return deps.items[0].spec.template.spec.containers[0].image
        # Try by exact name
        try:
            dep = await asyncio.to_thread(apps.read_namespaced_deployment, name=service, namespace=namespace)
            return dep.spec.template.spec.containers[0].image
        except Exception:
            pass
    except Exception as e:
        log.warning("Could not find prod image for %s: %s", service, e)
    return None


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
    # Re-create the LLM router ONCE after all field updates (was previously
    # inside the per-field loop — caused up to 4 re-creations if all LLM
    # settings changed at once, and the `global llm_router` declaration was
    # being re-evaluated per iteration which is a Python smell).
    if any(f in changed for f in ("llm_provider", "llm_api_key", "llm_fast_model", "llm_frontier_model")):
        global llm_router
        llm_router = LLMRouter(settings)
        log.info("LLM router re-created with provider=%s, model=%s",
                 settings.llm_provider, settings.llm_frontier_model)
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

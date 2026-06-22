"""Sentinel Core — the single-process entrypoint that runs all 6 layers.

This is the "plug and play" mode. One pod, one process, one event loop.
No Kafka, no Zookeeper, no Strimzi. All layers communicate via an in-process
asyncio.Queue (the _InMemoryBus).

Architecture:
  K8s API events ──> ingestion ──> queue ──> detection ──> queue ──>
  reasoning ──> queue ──> safety ──> queue ──> execution ──> queue ──> feedback

The FastAPI app also serves the HTTP routes defined in app.py:
  - /healthz, /readyz, /metrics
  - /incidents, /incidents/:id, /metrics-summary
  - /api/settings (GET/POST)
  - /admin/investigate
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
from contextlib import asynccontextmanager
from typing import Any

# Make every service importable
_ROOT = os.path.dirname(os.path.abspath(__file__))
for svc in ["common", "ingestion", "detection", "reasoning", "safety", "execution", "feedback"]:
    sys.path.insert(0, os.path.join(_ROOT, "..", svc))

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
import uvicorn

from sentinel_common import IncidentStatus  # noqa: F401 — re-exported for tests
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, _InMemoryBus
from sentinel_common.db import IncidentStore, EventStore, BetaStore, init_db
from sentinel_common.logging import configure_logging, get_logger
from sqlalchemy.ext.asyncio import create_async_engine

# Layer imports
from normalizer import Normalizer
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
from sandbox import SandboxRunner
from weaviate_client import WeaviateStore
from beta_tracker import BetaTracker
from postmortem import PostmortemGenerator
from settings_store import SettingsStore

# Local module imports
from .runtime_settings import RuntimeSettings, apply_runtime_to_settings, load_from_env
from .app import router as api_router
from .loops import (
    crd_watcher_loop,
    detection_loop,
    execution_loop,
    feedback_loop,
    ingestion_loop,
    reasoning_loop,
    safety_loop,
)

configure_logging()
log = get_logger("sentinel.core")

settings = get_settings()

# ─── Global app state (shared with app.py via _get_app_state) ───────────────
# This is a dict instead of module-level globals so the HTTP routes can
# access it without circular imports.
app_state: dict[str, Any] = {
    "ready": False,
    "db_initialized": False,
    "loops_started": False,
    "weaviate_available": False,
    "settings": settings,
    "runtime_settings": load_from_env(),
    "bus": None,
    "incident_store": None,
    "event_store": None,
    "beta_store": None,
    "engine": None,
    "if_detector": None,
    "cusum_detector": None,
    "rules_detector": None,
    "clusterer": None,
    "normalizer": None,
    "graph": None,
    "context_fetcher": None,
    "llm_router": None,
    "k8s_exec": None,
    "gh_exec": None,
    "sandbox": None,
    "weaviate": None,
    "beta_tracker": None,
    "postmortem_gen": None,
    "settings_store": None,
}

# Background tasks
_tasks: list[asyncio.Task] = []


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Start all the things."""
    runtime = app_state["runtime_settings"]

    log.info("Starting Sentinel Core v2.0.0 (single-process mode)")

    # Load persisted settings from K8s ConfigMap + Secret (overrides env vars)
    namespace = os.getenv("SENTINEL_NAMESPACE", "sentinel")
    release_name = os.getenv("SENTINEL_RELEASE_NAME", "sentinel")
    settings_store = SettingsStore(namespace=namespace, name_prefix=release_name)
    app_state["settings_store"] = settings_store
    if settings_store.available:
        persisted = await settings_store.load()
        if persisted:
            log.info("Applying %d persisted settings from K8s", len(persisted))
            # Update runtime settings with persisted values
            for key, value in persisted.items():
                if hasattr(runtime, key):
                    try:
                        setattr(runtime, key, value)
                    except Exception:
                        pass

    # Sync runtime → Settings object
    apply_runtime_to_settings(runtime, settings)

    # 1. In-memory message bus (replaces Kafka)
    bus = KafkaBus(brokers="", schema_registry=None)
    bus._mem = _InMemoryBus()
    bus._producer = None
    app_state["bus"] = bus
    log.info("In-memory bus initialized")

    # 2. Database
    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, echo=False, pool_size=5, max_overflow=10)
    await init_db(engine)
    app_state["engine"] = engine
    app_state["incident_store"] = IncidentStore(engine)
    app_state["event_store"] = EventStore(engine)
    app_state["beta_store"] = BetaStore(engine)
    app_state["db_initialized"] = True
    log.info("Database initialized")

    # 3. Detectors
    if HAS_SKLEARN and IsolationForestDetector is not None:
        app_state["if_detector"] = IsolationForestDetector(contamination=0.05, min_samples=50)
        log.info("IsolationForest detector initialized")
    else:
        log.info("IsolationForest detector skipped (scikit-learn not installed)")

    app_state["cusum_detector"] = CUSUMDetector(threshold=5.0, drift=0.02)
    app_state["rules_detector"] = RulesDetector(
        cpu_critical_pct=runtime.panic_cpu_threshold_pct,
        conn_critical=runtime.panic_connections_threshold,
    )
    app_state["clusterer"] = AnomalyClusterer(window_seconds=60)
    app_state["normalizer"] = Normalizer()
    log.info("Detectors initialized")

    # 4. Reasoning
    app_state["llm_router"] = LLMRouter(settings)
    app_state["context_fetcher"] = ContextFetcher(settings, app_state["llm_router"])
    app_state["graph"] = build_graph(
        llm_router=app_state["llm_router"],
        context_fetcher=app_state["context_fetcher"],
        incident_store=app_state["incident_store"],
        event_store=app_state["event_store"],
        beta_store=app_state["beta_store"],
    )
    log.info("Reasoning graph initialized")

    # 5. Execution
    app_state["k8s_exec"] = K8sExecutor(settings)
    app_state["gh_exec"] = GitHubExecutor(settings)
    app_state["sandbox"] = SandboxRunner(settings, app_state["k8s_exec"])
    log.info("Executors initialized")

    # 6. Feedback
    weaviate = WeaviateStore(settings)
    await weaviate.ensure_schema()
    app_state["weaviate"] = weaviate
    app_state["beta_tracker"] = BetaTracker(app_state["beta_store"])
    app_state["postmortem_gen"] = PostmortemGenerator(settings)
    app_state["weaviate_available"] = True
    log.info("Feedback layer initialized")

    # 7. Start background tasks — one per layer
    _tasks.append(asyncio.create_task(
        ingestion_loop(bus, app_state["normalizer"], runtime, settings), name="ingestion",
    ))
    _tasks.append(asyncio.create_task(
        detection_loop(bus, app_state["cusum_detector"], app_state["rules_detector"],
                       app_state["clusterer"], app_state["if_detector"], settings),
        name="detection",
    ))
    _tasks.append(asyncio.create_task(
        reasoning_loop(bus, app_state["graph"], app_state["incident_store"],
                       app_state["event_store"], settings),
        name="reasoning",
    ))
    _tasks.append(asyncio.create_task(
        safety_loop(bus, app_state["incident_store"], app_state["event_store"], settings),
        name="safety",
    ))
    _tasks.append(asyncio.create_task(
        execution_loop(bus, app_state["incident_store"], app_state["event_store"],
                       app_state["k8s_exec"], app_state["gh_exec"], app_state["sandbox"], settings),
        name="execution",
    ))
    _tasks.append(asyncio.create_task(
        feedback_loop(bus, app_state["incident_store"], app_state["event_store"],
                      app_state["weaviate"], app_state["beta_tracker"],
                      app_state["postmortem_gen"], settings),
        name="feedback",
    ))
    _tasks.append(asyncio.create_task(
        crd_watcher_loop(bus, settings, runtime), name="crd_watcher",
    ))
    app_state["loops_started"] = True
    app_state["ready"] = True
    log.info("All 7 background tasks started (including CRD watcher)")

    log.info("Sentinel Core ready — single-process mode active")
    yield

    # Shutdown
    app_state["ready"] = False
    for t in _tasks:
        t.cancel()
    await asyncio.gather(*_tasks, return_exceptions=True)
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
app.include_router(api_router)


if __name__ == "__main__":
    uvicorn.run(
        "sentinel_core.main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

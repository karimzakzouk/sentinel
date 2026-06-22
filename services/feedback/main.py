"""
Sentinel Layer 6 — Feedback Loop

Turns every incident into a lesson. Three jobs:

1. Vector Storage (Weaviate)
   Convert each resolved incident into a vector capturing service topology,
   anomaly signature, root cause type, and fix applied. Store with the
   `git_commit_hash` so future retrievals can apply code-churn decay.

2. Confidence Calibration (Beta distribution)
   Track each fix pattern (restart / scale / code_fix / rollback / etc.)
   per service. After every resolution, update alpha (success) or beta
   (failure). The posterior mean alpha/(alpha+beta) becomes the prior for
   the next incident's confidence computation.

3. Postmortem generation
   After resolution, ask the LLM to write a markdown postmortem and store
   it on the Incident record.
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

import uvicorn
from beta_tracker import BetaTracker
from fastapi import FastAPI
from postmortem import PostmortemGenerator
from sqlalchemy.ext.asyncio import create_async_engine
from weaviate_client import WeaviateStore

from sentinel_common import Incident, IncidentStatus
from sentinel_common.config import get_settings
from sentinel_common.db import BetaStore, EventStore, IncidentStore, init_db
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.feedback")

settings = get_settings()

app = FastAPI(title="Sentinel Feedback", version="1.0.0")

bus: KafkaBus | None = None
incident_store: IncidentStore | None = None
event_store: EventStore | None = None
beta_store: BetaStore | None = None
weaviate: WeaviateStore | None = None
beta_tracker: BetaTracker | None = None
postmortem_gen: PostmortemGenerator | None = None
engine: Any = None
consumer_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, incident_store, event_store, beta_store, weaviate, beta_tracker
    global postmortem_gen, engine, consumer_task
    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()

    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, pool_size=5)
    await init_db(engine)
    incident_store = IncidentStore(engine)
    event_store = EventStore(engine)
    beta_store = BetaStore(engine)

    weaviate = WeaviateStore(settings)
    await weaviate.ensure_schema()

    beta_tracker = BetaTracker(beta_store)
    postmortem_gen = PostmortemGenerator(settings)

    # Subscribe to the incidents topic — we get every incident after the
    # execution layer updates it. We only act on terminal states.
    consumer_task = asyncio.create_task(_consumer_loop())
    log.info("Feedback service started")


@app.on_event("shutdown")
async def _shutdown() -> None:
    if consumer_task:
        consumer_task.cancel()
    if bus:
        await bus.stop()
    if engine:
        await engine.dispose()


async def _consumer_loop() -> None:
    assert bus is not None
    consumer = bus.consumer(settings.kafka_topic_incidents, "sentinel-feedback")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.status in (IncidentStatus.VERIFIED, IncidentStatus.FAILED, IncidentStatus.RESOLVED):
                    asyncio.create_task(_process_incident(incident))
            except Exception as e:
                log.warning("Failed to process incident: %s", e)
    except asyncio.CancelledError:
        log.info("Consumer cancelled")


async def _process_incident(incident: Incident) -> None:
    """Update Beta tracker, store in Weaviate, generate postmortem."""
    assert incident_store is not None and event_store is not None
    assert beta_tracker is not None and weaviate is not None
    assert postmortem_gen is not None

    fp = incident.state.fix_proposal
    rc = incident.state.root_cause
    if fp is None or rc is None:
        return

    fix_pattern = fp.fix_type.value
    service = fp.target_service

    # 1. Update Beta tracker — was the fix successful?
    success = incident.status == IncidentStatus.VERIFIED
    alpha, beta = await beta_tracker.record(service, fix_pattern, success)
    incident.beta_alpha = alpha
    incident.beta_beta = beta
    log.info("Beta updated for %s/%s: alpha=%.1f, beta=%.1f, success=%s", service, fix_pattern, alpha, beta, success)

    await event_store.append(
        incident.incident_id, "feedback", "beta_updated", payload={"alpha": alpha, "beta": beta, "success": success}
    )

    # 2. Store in Weaviate for future RAG retrieval (with git_commit_at_resolution
    # so future retrievals can apply code-churn decay)
    weaviate_uuid = await weaviate.store_incident(incident)
    incident.weaviate_uuid = weaviate_uuid

    # 3. Generate postmortem
    pm = await postmortem_gen.generate(incident)
    incident.postmortem = pm

    # 4. Mark as RESOLVED (terminal state)
    incident.status = IncidentStatus.RESOLVED
    incident.state.status = IncidentStatus.RESOLVED
    incident.touch()
    await incident_store.upsert(incident.model_dump(mode="json"))
    await event_store.append(
        incident.incident_id,
        "feedback",
        "incident_resolved",
        payload={"weaviate_uuid": weaviate_uuid, "postmortem_length": len(pm) if pm else 0},
    )


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "feedback"}


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

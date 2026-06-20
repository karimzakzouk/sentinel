"""
Sentinel Layer 3 — Reasoning Engine

Consumes AnomalyCluster messages from Kafka, runs the multi-agent LangGraph
state machine, and publishes the resulting Incident (with RootCause +
FixProposal) to the `sentinel.incidents` topic for the safety layer.

The graph:
  Investigator -> Evidence Gatherer -> Analyst -> Fix Proposer
        ^               |                |
        |               |                |
        +--- Rewind <---+----------------+   (recursive escape hatch)

State is a Pydantic model — every node reads from and writes to it. A
malformed agent output is caught at the node boundary rather than poisoning
downstream nodes.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import sys
from typing import Any
from uuid import UUID, uuid4

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

from fastapi import FastAPI
from pydantic import ValidationError
import uvicorn

from sentinel_common import (
    AnomalyCluster, Incident, IncidentStatus, AgentState,
)
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry
from sentinel_common.db import IncidentStore, EventStore, init_db, BetaStore
from sqlalchemy.ext.asyncio import create_async_engine

from graph import build_graph
from context import ContextFetcher
from llm import LLMRouter

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.reasoning")

settings = get_settings()

app = FastAPI(title="Sentinel Reasoning", version="1.0.0")

bus: KafkaBus | None = None
schema_reg: SchemaRegistry | None = None
incident_store: IncidentStore | None = None
event_store: EventStore | None = None
beta_store: BetaStore | None = None
graph: Any = None
context_fetcher: ContextFetcher | None = None
llm_router: LLMRouter | None = None
consumer_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, schema_reg, incident_store, event_store, beta_store
    global graph, context_fetcher, llm_router, consumer_task

    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()

    # Postgres
    async_url = settings.postgres_url.replace("postgresql://", "postgresql+asyncpg://")
    engine = create_async_engine(async_url, echo=False, pool_size=10, max_overflow=20)
    await init_db(engine)
    incident_store = IncidentStore(engine)
    event_store = EventStore(engine)
    beta_store = BetaStore(engine)

    # LLM router + context fetcher + graph
    llm_router = LLMRouter(settings)
    context_fetcher = ContextFetcher(settings, llm_router)
    graph = build_graph(
        llm_router=llm_router,
        context_fetcher=context_fetcher,
        incident_store=incident_store,
        event_store=event_store,
        beta_store=beta_store,
    )

    consumer_task = asyncio.create_task(_consumer_loop())
    log.info("Reasoning engine started")


@app.on_event("shutdown")
async def _shutdown() -> None:
    if consumer_task:
        consumer_task.cancel()
    if bus:
        await bus.stop()


async def _consumer_loop() -> None:
    assert bus is not None
    consumer = bus.consumer(settings.kafka_topic_anomalies, "sentinel-reasoning")
    try:
        async for raw in consumer:
            try:
                cluster = AnomalyCluster.model_validate_json(
                    raw if isinstance(raw, bytes) else raw.value
                )
                asyncio.create_task(_process_cluster(cluster))
            except Exception as e:  # noqa: BLE001
                log.warning("Failed to parse cluster: %s", e)
    except asyncio.CancelledError:
        log.info("Consumer cancelled")


async def _process_cluster(cluster: AnomalyCluster) -> None:
    """Run the full reasoning graph for one anomaly cluster."""
    assert incident_store is not None and event_store is not None
    assert bus is not None and graph is not None

    incident_id = uuid4()
    state = AgentState(
        incident_id=incident_id,
        cluster=cluster,
        status=IncidentStatus.INVESTIGATING,
    )
    incident = Incident(
        incident_id=incident_id,
        cluster=cluster,
        state=state,
        status=IncidentStatus.INVESTIGATING,
    )

    await event_store.append(incident_id, "reasoning", "investigation_started",
                             payload={"cluster_summary": cluster.summary})

    # Persist initial incident
    await incident_store.upsert(incident.model_dump(mode="json"))

    try:
        # Run the graph with a hard timeout. If the LLM hangs or the rewind
        # loop spins forever, we kill the investigation and mark it failed.
        # Default: 5 minutes. Configurable via SENTINEL_INVESTIGATION_TIMEOUT.
        timeout = int(os.getenv("SENTINEL_INVESTIGATION_TIMEOUT", "300"))
        final_state = await asyncio.wait_for(
            graph.ainvoke(state.model_dump(mode="json")),
            timeout=timeout,
        )
        # Validate the final state with Pydantic
        state = AgentState.model_validate(final_state)
        incident.state = state
        incident.status = state.status
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        await event_store.append(incident_id, "reasoning", "investigation_completed",
                                 agent="orchestrator",
                                 payload={"status": state.status})

        # Publish to incidents topic for the safety layer
        await bus.publish_model(settings.kafka_topic_incidents, incident, key=cluster.service)
        log.info("Incident %s published (status=%s, service=%s)",
                 incident_id, state.status, cluster.service)
    except ValidationError as ve:
        log.error("State validation failed: %s", ve)
        incident.status = IncidentStatus.FAILED
        incident.state.error = f"validation_error: {ve}"
        await incident_store.upsert(incident.model_dump(mode="json"))
    except asyncio.TimeoutError:
        log.error("Investigation timed out after %ss", timeout)
        incident.status = IncidentStatus.FAILED
        incident.state.status = IncidentStatus.FAILED
        incident.state.error = f"investigation_timeout: exceeded {timeout}s"
        incident.state.agent_trace.append({
            "agent": "orchestrator",
            "action": "timeout",
            "timeout_seconds": timeout,
        })
        incident.touch()
        await incident_store.upsert(incident.model_dump(mode="json"))
        await event_store.append(incident_id, "reasoning", "investigation_timeout",
                                  payload={"timeout_seconds": timeout})
    except Exception as e:  # noqa: BLE001
        log.exception("Reasoning graph failed: %s", e)
        incident.status = IncidentStatus.FAILED
        await incident_store.upsert(incident.model_dump(mode="json"))


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "reasoning"}


@app.post("/admin/reprocess/{incident_id}")
async def reprocess(incident_id: UUID) -> dict[str, str]:
    """Re-run the graph for an existing incident. Useful for debugging."""
    assert incident_store is not None
    inc = await incident_store.get(incident_id)
    if inc is None:
        return {"status": "not_found"}
    cluster = AnomalyCluster.model_validate(inc["cluster"])
    asyncio.create_task(_process_cluster(cluster))
    return {"status": "reprocessing_started"}


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

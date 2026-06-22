"""Reasoning loop — consumes anomaly clusters, runs the LangGraph, publishes incidents."""
from __future__ import annotations

import asyncio
import logging
import os
from typing import Any
from uuid import uuid4

from sentinel_common import AgentState, AnomalyCluster, Incident, IncidentStatus
from sentinel_common.metrics import INCIDENTS_TOTAL

log = logging.getLogger(__name__)


async def reasoning_loop(
    bus: Any,
    graph: Any,
    incident_store: Any,
    event_store: Any,
    settings: Any,
) -> None:
    """Consume anomaly clusters, run the LangGraph, publish incidents."""
    consumer = bus.consumer(settings.kafka_topic_anomalies, "sentinel-core-reasoning")
    try:
        async for raw in consumer:
            try:
                cluster = AnomalyCluster.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                asyncio.create_task(_process_cluster(cluster, bus, graph, incident_store, event_store, settings))
            except Exception as e:
                log.warning("Reasoning consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Reasoning loop cancelled")


async def _process_cluster(
    cluster: AnomalyCluster,
    bus: Any,
    graph: Any,
    incident_store: Any,
    event_store: Any,
    settings: Any,
) -> None:
    """Run the full reasoning graph for one cluster."""
    incident_id = uuid4()
    state = AgentState(incident_id=incident_id, cluster=cluster, status=IncidentStatus.INVESTIGATING)
    incident = Incident(
        incident_id=incident_id,
        cluster=cluster,
        state=state,
        status=IncidentStatus.INVESTIGATING,
    )
    await event_store.append(
        incident_id, "reasoning", "investigation_started",
        payload={"cluster_summary": cluster.summary},
    )
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
        await event_store.append(
            incident_id, "reasoning", "investigation_completed",
            agent="orchestrator", payload={"status": state.status},
        )
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

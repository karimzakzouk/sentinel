"""Feedback loop — consumes completed incidents, updates Beta tracker, stores in
Weaviate, generates postmortem.

This is the loop that closes the feedback cycle: every resolved incident
becomes a permanent data point in the system's memory, improving future
diagnoses via Beta-distribution priors and Weaviate RAG retrieval.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from sentinel_common import Incident, IncidentStatus

log = logging.getLogger(__name__)


async def feedback_loop(
    bus: Any,
    incident_store: Any,
    event_store: Any,
    weaviate: Any,
    beta_tracker: Any,
    postmortem_gen: Any,
    settings: Any,
) -> None:
    """Consume completed incidents, update Beta tracker, store in Weaviate, write postmortem."""
    consumer = bus.consumer(settings.kafka_topic_resolved, "sentinel-core-feedback")
    try:
        async for raw in consumer:
            try:
                incident = Incident.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                if incident.status in (IncidentStatus.VERIFIED, IncidentStatus.FAILED, IncidentStatus.RESOLVED):
                    asyncio.create_task(
                        _feedback_process(
                            incident,
                            incident_store,
                            event_store,
                            weaviate,
                            beta_tracker,
                            postmortem_gen,
                        )
                    )
            except Exception as e:
                log.warning("Feedback consumer error: %s", e)
    except asyncio.CancelledError:
        log.info("Feedback loop cancelled")


async def _feedback_process(
    incident: Incident,
    incident_store: Any,
    event_store: Any,
    weaviate: Any,
    beta_tracker: Any,
    postmortem_gen: Any,
) -> None:
    """Update beta tracker, store in Weaviate, write postmortem."""
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

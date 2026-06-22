"""Detection loop — consumes signals, runs detectors, publishes anomaly clusters."""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from sentinel_common import Signal

log = logging.getLogger(__name__)


async def detection_loop(
    bus: Any,
    cusum_detector: Any,
    rules_detector: Any,
    clusterer: Any,
    if_detector: Any | None,
    settings: Any,
) -> None:
    """Consume signals, run detectors, publish anomaly clusters."""
    consumer = bus.consumer(settings.kafka_topic_signals, "sentinel-core-detection")
    try:
        async for raw in consumer:
            try:
                sig = Signal.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                anomalies = []
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

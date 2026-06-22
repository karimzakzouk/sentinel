"""
Sentinel Layer 2 — Anomaly Detection

Consumes Signal messages from Kafka, runs them through Isolation Forest
(multi-dimensional) + CUSUM (univariate drift) + rules-based detectors,
clusters anomalies that fire together, and publishes AnomalyCluster messages
to the `sentinel.anomalies` topic.

This is *just math* — no LLM, no AI reasoning. It needs to be fast because
it runs on every metric, every minute, for every service.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import sys
from collections import defaultdict, deque
from datetime import datetime, timezone
from typing import Any
from uuid import UUID

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

from fastapi import FastAPI
import uvicorn

from sentinel_common import Anomaly, AnomalyCluster, Signal, Severity
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry

from detectors.isolation_forest import IsolationForestDetector
from detectors.cusum import CUSUMDetector
from detectors.rules import RulesDetector
from detectors.clusterer import AnomalyClusterer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.detection")

settings = get_settings()

app = FastAPI(title="Sentinel Detection", version="1.0.0")

bus: KafkaBus | None = None
schema_reg: SchemaRegistry | None = None
if_detector: IsolationForestDetector | None = None
cusum_detector: CUSUMDetector | None = None
rules_detector: RulesDetector | None = None
clusterer: AnomalyClusterer | None = None
consumer_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, schema_reg, if_detector, cusum_detector, rules_detector, clusterer, consumer_task
    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()

    if_detector = IsolationForestDetector(
        contamination=0.05,
        min_samples=50,
    )
    cusum_detector = CUSUMDetector(threshold=5.0, drift=0.02)
    rules_detector = RulesDetector(
        cpu_critical_pct=settings.panic_cpu_threshold_pct,
        conn_critical=settings.panic_connections_threshold,
    )
    clusterer = AnomalyClusterer(window_seconds=60)

    consumer_task = asyncio.create_task(_consumer_loop())
    log.info("Detection started")


@app.on_event("shutdown")
async def _shutdown() -> None:
    if consumer_task:
        consumer_task.cancel()
    if bus:
        await bus.stop()


async def _consumer_loop() -> None:
    """Consume signals, run detectors, publish anomaly clusters."""
    assert bus is not None
    consumer = bus.consumer(settings.kafka_topic_signals, "sentinel-detection")
    try:
        async for raw in consumer:
            try:
                sig = Signal.model_validate_json(raw if isinstance(raw, bytes) else raw.value)
                await _process_signal(sig)
            except Exception as e:  # noqa: BLE001
                log.warning("Failed to process signal: %s", e)
    except asyncio.CancelledError:
        log.info("Consumer cancelled")
    except Exception as e:  # noqa: BLE001
        log.exception("Consumer loop crashed: %s", e)


async def _process_signal(sig: Signal) -> None:
    """Run all detectors on a single signal and cluster the results."""
    assert if_detector is not None and cusum_detector is not None
    assert rules_detector is not None and clusterer is not None
    assert bus is not None

    anomalies: list[Anomaly] = []

    # Multi-dim Isolation Forest — operates on a sliding window per service
    af = await if_detector.observe(sig)
    if af is not None:
        anomalies.append(af)

    # CUSUM — operates per-metric, detects drift
    for cf in await cusum_detector.observe(sig):
        anomalies.append(cf)

    # Rules — explicit panic-button conditions (Layer 6 edge case #6)
    for rf in await rules_detector.observe(sig):
        anomalies.append(rf)

    # Hand all anomalies to the clusterer — it groups by service+window
    # and emits an AnomalyCluster when it has enough signal
    for anomaly in anomalies:
        cluster = clusterer.add(anomaly)
        if cluster is not None:
            await bus.publish_model(
                settings.kafka_topic_anomalies, cluster, key=cluster.service
            )
            log.info(
                "Published anomaly cluster %s for %s (severity=%s, %d anomalies)",
                cluster.cluster_id, cluster.service, cluster.severity, len(cluster.anomalies),
            )


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "detection"}


@app.get("/metrics")
async def metrics() -> dict[str, Any]:
    return {
        "if_model_size": len(if_detector._window) if if_detector else 0,
        "cusum_streams": len(cusum_detector._streams) if cusum_detector else 0,
        "clusterer_pending": clusterer.pending_count() if clusterer else 0,
    }


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

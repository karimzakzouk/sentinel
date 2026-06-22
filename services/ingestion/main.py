"""
Sentinel Layer 1 — Data Ingestion

This service is a FastAPI app + a Kafka producer loop. It exposes:
  POST /ingest                — push a raw signal (used by external collectors)
  POST /ingest/prometheus     — webhook receiver for Prometheus Alertmanager
  POST /ingest/loki           — webhook receiver for Loki rules
  POST /ingest/k8s            — webhook receiver for Kubernetes events
  POST /ingest/jaeger         — webhook receiver for Jaeger traces

And it runs background pollers that scrape Prometheus / CloudWatch / K8s API
on a schedule, normalize everything into `Signal` objects, validate them
through Schema Registry, and publish them to the `sentinel.signals` Kafka
topic.

In production we deploy Strimzi Kafka + Confluent Schema Registry as
sidecars/subcharts (see chart/templates/kafka.yaml).
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
from datetime import datetime, timezone
from typing import Any
from uuid import uuid4

# Make sentinel_common importable in dev/test runs without installing.
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
import uvicorn

from sentinel_common import Signal, SignalSource, Severity
from sentinel_common.config import get_settings
from sentinel_common.kafka_client import KafkaBus, SchemaRegistry

from collectors.prometheus import PrometheusCollector
from collectors.loki import LokiCollector
from collectors.jaeger import JaegerCollector
from collectors.k8s_events import K8sEventsCollector
from collectors.cd_history import CDHistoryCollector
from normalizer import Normalizer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.ingestion")

settings = get_settings()

app = FastAPI(title="Sentinel Ingestion", version="1.0.0")

# Global state — initialized in startup
bus: KafkaBus | None = None
schema_reg: SchemaRegistry | None = None
normalizer: Normalizer | None = None
collectors: list[Any] = []
poller_task: asyncio.Task | None = None


@app.on_event("startup")
async def _startup() -> None:
    global bus, schema_reg, normalizer, collectors, poller_task
    schema_reg = SchemaRegistry(settings.schema_registry_url)
    bus = KafkaBus(settings.kafka_brokers, schema_reg)
    await bus.start()
    normalizer = Normalizer()

    # Real collectors — only enabled if their respective env vars are set.
    collectors = []
    if os.getenv("PROMETHEUS_URL"):
        collectors.append(PrometheusCollector(os.getenv("PROMETHEUS_URL")))
    if os.getenv("LOKI_URL"):
        collectors.append(LokiCollector(os.getenv("LOKI_URL")))
    if os.getenv("JAEGER_URL"):
        collectors.append(JaegerCollector(os.getenv("JAEGER_URL")))
    if os.getenv("K8S_IN_CLUSTER_COLLECTOR", "true").lower() == "true":
        collectors.append(K8sEventsCollector())
    if os.getenv("GITHUB_TOKEN") and os.getenv("CD_REPO"):
        collectors.append(CDHistoryCollector(
            os.getenv("CD_REPO"), os.getenv("GITHUB_TOKEN", settings.github_token)
        ))

    poller_task = asyncio.create_task(_poller_loop())
    log.info("Ingestion started with %d collectors", len(collectors))


@app.on_event("shutdown")
async def _shutdown() -> None:
    if poller_task:
        poller_task.cancel()
    if bus:
        await bus.stop()
    log.info("Ingestion stopped")


async def _poller_loop() -> None:
    """Round-robin poll every collector on its configured interval."""
    while True:
        try:
            for c in collectors:
                try:
                    raw_batch = await c.poll()
                    for raw in raw_batch:
                        await _publish_raw(raw)
                except Exception as e:  # noqa: BLE001
                    log.warning("Collector %s failed: %s", c.__class__.__name__, e)
            await asyncio.sleep(15)
        except asyncio.CancelledError:
            break
        except Exception as e:  # noqa: BLE001
            log.exception("Poller loop error: %s", e)
            await asyncio.sleep(5)


async def _publish_raw(raw: dict[str, Any]) -> None:
    """Normalize a raw payload into a Signal and publish to Kafka.

    Schema-registry failures / Pydantic validation failures are routed to the
    dead-letter topic so we can inspect them later.
    """
    assert normalizer is not None and bus is not None
    try:
        sig = normalizer.normalize(raw)
        await bus.publish_model(settings.kafka_topic_signals, sig, key=sig.service)
    except Exception as e:  # noqa: BLE001
        log.warning("Failed to normalize/publish signal: %s — sending to DLQ", e)
        try:
            dlq_payload = {
                "ts": datetime.now(timezone.utc).isoformat(),
                "error": str(e),
                "raw": raw,
            }
            await bus.publish_raw(
                settings.kafka_topic_deadletter,
                str(dlq_payload).encode("utf-8"),
            )
        except Exception:
            log.exception("DLQ publish also failed")


# ---------- HTTP API ----------

@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok", "service": "ingestion"}


@app.post("/ingest")
async def ingest_signal(raw: dict[str, Any]) -> dict[str, str]:
    """Generic push endpoint — accepts anything the normalizer understands."""
    await _publish_raw(raw)
    return {"status": "accepted"}


@app.post("/ingest/prometheus")
async def ingest_prometheus(payload: dict[str, Any]) -> dict[str, str]:
    """Alertmanager webhook format."""
    for alert in payload.get("alerts", [payload]):
        await _publish_raw({"source": "prometheus", "alert": alert})
    return {"status": "accepted"}


@app.post("/ingest/loki")
async def ingest_loki(payload: dict[str, Any]) -> dict[str, str]:
    await _publish_raw({"source": "loki", "log": payload})
    return {"status": "accepted"}


@app.post("/ingest/k8s")
async def ingest_k8s(payload: dict[str, Any]) -> dict[str, str]:
    await _publish_raw({"source": "k8s_events", "event": payload})
    return {"status": "accepted"}


@app.post("/ingest/jaeger")
async def ingest_jaeger(payload: dict[str, Any]) -> dict[str, str]:
    await _publish_raw({"source": "jaeger", "trace": payload})
    return {"status": "accepted"}


@app.get("/metrics")
async def metrics() -> dict[str, Any]:
    """Lightweight metrics endpoint — Prometheus can scrape this too."""
    return {
        "collectors": [c.__class__.__name__ for c in collectors],
        "bus_started": bus is not None and bus._producer is not None,
    }


if __name__ == "__main__":
    uvicorn.run(
        "main:app",
        host=settings.api_host,
        port=int(os.getenv("PORT", settings.api_port)),
        log_level="info",
    )

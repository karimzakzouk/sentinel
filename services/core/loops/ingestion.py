"""Ingestion loop — watches K8s events + (optionally) polls Prometheus/Loki.

Normalizes raw telemetry into Signal objects and publishes them to the
anomalies... wait, to the signals topic.
"""
from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timezone

log = logging.getLogger(__name__)


async def ingestion_loop(bus: Any, normalizer: Any, runtime_settings: Any, settings: Any) -> None:  # noqa: ANN401
    """Watch K8s events + (optionally) poll Prometheus/Loki, normalize, publish signals."""
    from collectors.k8s_events import K8sEventsCollector

    k8s_collector = K8sEventsCollector()
    last_run = 0.0
    while True:
        try:
            now = datetime.now(timezone.utc).timestamp()
            if now - last_run >= 15:
                last_run = now
                raw_batch = []
                if k8s_collector._available:
                    raw_batch.extend(await k8s_collector.poll())
                if runtime_settings.prometheus_url:
                    from collectors.prometheus import PrometheusCollector

                    pc = PrometheusCollector(runtime_settings.prometheus_url)
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


# Avoid circular import
from typing import Any  # noqa: E402

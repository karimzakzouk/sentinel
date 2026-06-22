"""Prometheus collector — scrapes /api/v1/query on a schedule."""
from __future__ import annotations

import logging
from datetime import datetime, timezone
from typing import Any

import httpx

log = logging.getLogger(__name__)


# These queries target the most common failure-mode signals. They're
# deliberately generic so the collector works against any Prometheus that has
# the standard kube-state-metrics + node-exporter + app metrics.
QUERIES = {
    "pod_cpu_pct": 'sum by(pod, namespace, service) (rate(container_cpu_usage_seconds_total{container!="POD"}[5m])) * 100',
    "pod_mem_bytes": 'sum by(pod, namespace, service) (container_memory_working_set_bytes{container!="POD"})',
    "pod_restart_count": 'sum by(pod, namespace, service) (kube_pod_container_status_restarts_total)',
    "pod_not_ready": 'sum by(namespace, service) (kube_pod_status_phase{phase!="Running"} == 0)',
    "http_5xx_rate": 'sum by(service, namespace) (rate(http_requests_total{status=~"5.."}[5m]))',
    "http_p99_ms": 'histogram_quantile(0.99, sum by(le, service, namespace) (rate(http_request_duration_seconds_bucket[5m]))) * 1000',
}


class PrometheusCollector:
    """Polls Prometheus on a fixed interval and emits one raw signal per row."""

    def __init__(self, base_url: str, timeout: float = 10.0):
        self.base_url = base_url.rstrip("/")
        self.client = httpx.AsyncClient(timeout=timeout)

    async def poll(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        for name, query in QUERIES.items():
            try:
                r = await self.client.get(
                    f"{self.base_url}/api/v1/query",
                    params={"query": query},
                )
                r.raise_for_status()
                data = r.json()
                if data.get("status") != "success":
                    continue
                for result in data.get("data", {}).get("result", []):
                    metric = result.get("metric", {})
                    value = float(result.get("value", [0, "0"])[1])
                    out.append({
                        "source": "prometheus",
                        "alert": {
                            "alertname": name,
                            "status": "firing" if value > 0 else "resolved",
                            "labels": metric,
                            "annotations": {"value": str(value), "query": name},
                            "value": value,
                            "startsAt": datetime.now(timezone.utc).isoformat(),
                        },
                    })
            except Exception as e:  # noqa: BLE001
                log.warning("Prometheus query %s failed: %s", name, e)
        return out

    async def close(self) -> None:
        await self.client.aclose()

"""Jaeger collector — pulls recent traces that contain error spans."""
from __future__ import annotations

import logging
from typing import Any

import httpx

log = logging.getLogger(__name__)


class JaegerCollector:
    """Polls Jaeger for traces that have at least one error span."""

    def __init__(self, base_url: str, timeout: float = 10.0, lookback_seconds: int = 300):
        self.base_url = base_url.rstrip("/")
        self.lookback = lookback_seconds
        self.client = httpx.AsyncClient(timeout=timeout)

    async def poll(self) -> list[dict[str, Any]]:
        try:
            # Jaeger /api/traces?tags={"error":true}&lookback=1h&limit=20
            r = await self.client.get(
                f"{self.base_url}/api/traces",
                params={
                    "tags": '{"error":true}',
                    "lookback": f"{self.lookback}s",
                    "limit": 20,
                },
            )
            r.raise_for_status()
            data = r.json()
            out: list[dict[str, Any]] = []
            for trace in data.get("data", []):
                out.append({"source": "jaeger", "trace": trace})
            return out
        except Exception as e:  # noqa: BLE001
            log.warning("Jaeger poll failed: %s", e)
            return []

    async def close(self) -> None:
        await self.client.aclose()

"""Loki collector — pulls recent error-level log lines for services."""

from __future__ import annotations

import logging
from typing import Any

import httpx

log = logging.getLogger(__name__)


class LokiCollector:
    """Polls Loki for recent error logs.

    The query is intentionally narrow — we only want lines that look like
    errors (level=error or contain 'ERROR'/'panic'/'FATAL'). Sentinel's
    reasoning layer will issue targeted follow-up queries via the Evidence
    Gatherer agent.
    """

    def __init__(self, base_url: str, timeout: float = 10.0, lookback_seconds: int = 60):
        self.base_url = base_url.rstrip("/")
        self.lookback = lookback_seconds
        self.client = httpx.AsyncClient(timeout=timeout)

    async def poll(self) -> list[dict[str, Any]]:
        # Limit to last Ns, only lines with level=error OR matching common
        # error patterns. Loki query language:
        query = '{level="error"} |= "ERROR" or {level="error"} |= "panic" or {level="error"} |= "FATAL"'
        try:
            r = await self.client.get(
                f"{self.base_url}/loki/api/v1/query_range",
                params={
                    "query": query,
                    "limit": 100,
                    "start": f"now-{self.lookback}s",
                    "end": "now",
                },
            )
            r.raise_for_status()
            data = r.json()
            out: list[dict[str, Any]] = []
            for stream in data.get("data", {}).get("result", []):
                labels = stream.get("stream", {})
                for ts, line in stream.get("values", []):
                    out.append(
                        {
                            "source": "loki",
                            "log": {
                                "labels": labels,
                                "line": line,
                                "ts": ts,
                            },
                        }
                    )
            return out
        except Exception as e:
            log.warning("Loki poll failed: %s", e)
            return []

    async def close(self) -> None:
        await self.client.aclose()

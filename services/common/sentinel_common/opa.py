"""Rego policy bundle helpers — the safety service uses these to talk to OPA."""

from __future__ import annotations

import logging
from typing import Any

import httpx

logger = logging.getLogger(__name__)


class OPAClient:
    """Asynchronous Open Policy Agent REST client.

    Sentinel compiles the .rego files under services/safety/policies into a
    bundle and serves them via OPA. The decision endpoint is
    `POST /v1/data/<package>/<rule>`.
    """

    def __init__(self, base_url: str, timeout: float = 5.0):
        self.base_url = base_url.rstrip("/")
        self._client = httpx.AsyncClient(timeout=timeout)

    async def decide(self, decision_path: str, input_data: dict[str, Any]) -> dict[str, Any]:
        """Evaluate the policy at `decision_path` with `input_data`.

        decision_path example: 'sentinel/setection_allow_fix'
        """
        url = f"{self.base_url}/v1/data/{decision_path}"
        r = await self._client.post(url, json={"input": input_data})
        r.raise_for_status()
        result: dict[str, Any] = r.json().get("result", {})
        return result

    async def close(self) -> None:
        await self._client.aclose()

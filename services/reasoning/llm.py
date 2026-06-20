"""LLM router — picks fast vs frontier model and centralizes the API client.

We use the OpenAI-compatible chat completions endpoint. The default base URL
points at z.ai's GLM endpoint; users can override with any OpenAI-compatible
endpoint (Anthropic, OpenAI, Ollama, vLLM) by setting env vars.

Fast model (Qwen 8B equivalent) — used for: log parsing, evidence gathering,
hypothesis generation when the context is small.

Frontier model — used for: complex root-cause synthesis, fix proposals that
require reasoning about code patches.

The router also implements the JSON-mode + retry logic that every agent needs.
"""
from __future__ import annotations

import json
import logging
import re
from typing import Any

from openai import AsyncOpenAI
from tenacity import retry, stop_after_attempt, wait_exponential

log = logging.getLogger(__name__)


class LLMRouter:
    def __init__(self, settings: Any):
        self.settings = settings
        self.client = AsyncOpenAI(
            base_url=settings.llm_base_url,
            api_key=settings.llm_api_key or "missing",
        )
        self.fast_model = settings.llm_fast_model
        self.frontier_model = settings.llm_frontier_model

    @retry(stop=stop_after_attempt(3), wait=wait_exponential(min=1, max=10))
    async def complete(self, model: str, system: str, user: str,
                       temperature: float = 0.2, max_tokens: int = 2000) -> str:
        """Plain text completion."""
        try:
            r = await self.client.chat.completions.create(
                model=model,
                messages=[
                    {"role": "system", "content": system},
                    {"role": "user", "content": user},
                ],
                temperature=temperature,
                max_tokens=max_tokens,
            )
            return r.choices[0].message.content or ""
        except Exception as e:  # noqa: BLE001
            log.warning("LLM complete failed (%s): %s", model, e)
            raise

    @retry(stop=stop_after_attempt(3), wait=wait_exponential(min=1, max=10))
    async def complete_json(self, model: str, system: str, user: str,
                            temperature: float = 0.1, max_tokens: int = 2000) -> dict[str, Any]:
        """JSON-mode completion. Strips markdown fences and parses.

        We don't rely on the API's response_format=json_object because not
        every backend supports it. Instead we ask for JSON in the prompt and
        parse defensively.
        """
        prompt = user + "\n\nRespond with a single valid JSON object. No prose, no markdown fences."
        try:
            r = await self.client.chat.completions.create(
                model=model,
                messages=[
                    {"role": "system", "content": system},
                    {"role": "user", "content": prompt},
                ],
                temperature=temperature,
                max_tokens=max_tokens,
            )
            text = r.choices[0].message.content or "{}"
            return _extract_json(text)
        except Exception as e:  # noqa: BLE001
            log.warning("LLM complete_json failed (%s): %s", model, e)
            raise

    async def fast_json(self, system: str, user: str, **kw: Any) -> dict[str, Any]:
        return await self.complete_json(self.fast_model, system, user, **kw)

    async def frontier_json(self, system: str, user: str, **kw: Any) -> dict[str, Any]:
        return await self.complete_json(self.frontier_model, system, user, **kw)

    async def fast_text(self, system: str, user: str, **kw: Any) -> str:
        return await self.complete(self.fast_model, system, user, **kw)

    async def frontier_text(self, system: str, user: str, **kw: Any) -> str:
        return await self.complete(self.frontier_model, system, user, **kw)


_JSON_BLOCK_RE = re.compile(r"```(?:json)?\s*(\{.*?\}|\[.*?\])\s*```", re.DOTALL)
_BARE_JSON_RE = re.compile(r"(\{.*\}|\[.*\])", re.DOTALL)


def _extract_json(text: str) -> dict[str, Any]:
    """Defensive JSON extractor — handles markdown fences, prose around JSON."""
    m = _JSON_BLOCK_RE.search(text)
    if m:
        return json.loads(m.group(1))
    m = _BARE_JSON_RE.search(text)
    if m:
        return json.loads(m.group(1))
    return json.loads(text)  # let it raise

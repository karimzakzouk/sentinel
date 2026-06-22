"""Kafka producer/consumer helpers with schema-registry validation.

This module is deliberately lightweight: it wraps aiokafka so the rest of the
codebase doesn't import Kafka directly. It also implements a *minimal* schema
registry check — every Signal must carry a valid `schema_version` envelope
before it goes on the wire. In production you'd run Confluent Schema Registry
as a separate pod (the Helm chart deploys it); this client just talks to it
over HTTP.
"""
from __future__ import annotations

import asyncio
import json
import logging
from typing import Any, AsyncIterator, Optional

import httpx
from pydantic import BaseModel

logger = logging.getLogger(__name__)


class SchemaRegistryError(Exception):
    pass


class SchemaRegistry:
    """Minimal Confluent Schema Registry compatible client.

    Only the bits Sentinel actually needs: register a subject, get latest
    version, validate a payload against the latest schema. If the registry
    is unreachable, we fail *open* with a warning — the dead-letter queue
    still catches malformed Pydantic models locally.
    """

    def __init__(self, base_url: str, timeout: float = 5.0):
        self.base_url = base_url.rstrip("/")
        self._client = httpx.AsyncClient(timeout=timeout)
        self._cache: dict[str, dict[str, Any]] = {}

    async def latest(self, subject: str) -> dict[str, Any]:
        if subject in self._cache:
            return self._cache[subject]
        url = f"{self.base_url}/subjects/{subject}/versions/latest"
        try:
            r = await self._client.get(url)
            r.raise_for_status()
            schema = r.json()
            self._cache[subject] = schema
            return schema
        except Exception as e:  # noqa: BLE001
            logger.warning("Schema registry unreachable for %s: %s", subject, e)
            return {}

    async def validate(self, subject: str, payload: dict[str, Any]) -> bool:
        """Best-effort validation. Real enforcement is via Pydantic + DLQ."""
        schema = await self.latest(subject)
        if not schema:
            return True
        # Confluent returns {"schema": "<json string>"}
        try:
            required = json.loads(schema.get("schema", "{}")).get("required", [])
        except json.JSONDecodeError:
            return True
        return all(k in payload for k in required)

    async def close(self) -> None:
        await self._client.aclose()


# We use aiokafka for real Kafka. The Helm chart deploys Strimzi Kafka.
try:
    from aiokafka import AIOKafkaProducer, AIOKafkaConsumer  # type: ignore
    _HAS_AIOKAFKA = True
except ImportError:  # pragma: no cover - aiokafka optional in unit tests
    _HAS_AIOKAFKA = False
    logger.warning("aiokafka not installed — KafkaBus will run in in-memory mode")


class _InMemoryTopic:
    def __init__(self) -> None:
        self.queue: asyncio.Queue[bytes] = asyncio.Queue()
        self.subscribers: list[asyncio.Queue[bytes]] = []

    async def publish(self, value: bytes) -> None:
        await self.queue.put(value)
        for sub in list(self.subscribers):
            await sub.put(value)


class _InMemoryBus:
    """Fallback when aiokafka isn't installed (tests / local dev)."""

    def __init__(self) -> None:
        self.topics: dict[str, _InMemoryTopic] = {}

    def topic(self, name: str) -> _InMemoryTopic:
        if name not in self.topics:
            self.topics[name] = _InMemoryTopic()
        return self.topics[name]

    async def publish(self, topic: str, value: bytes, key: Optional[bytes] = None) -> None:
        await self.topic(topic).publish(value)

    def consumer(self, topic: str, group: str) -> "_InMemoryConsumer":
        q: asyncio.Queue[bytes] = asyncio.Queue()
        t = self.topic(topic)
        t.subscribers.append(q)
        return _InMemoryConsumer(q)


class _InMemoryConsumer:
    def __init__(self, q: asyncio.Queue[bytes]) -> None:
        self._q = q

    async def __aiter__(self) -> AsyncIterator[bytes]:
        while True:
            v = await self._q.get()
            yield v

    async def stop(self) -> None:
        pass


class KafkaBus:
    """Publish/subscribe bus used by every service.

    Production: real Kafka via aiokafka.
    Tests / local: in-memory asyncio queue.
    """

    def __init__(self, brokers: str, schema_registry: Optional[SchemaRegistry] = None):
        self.brokers = brokers
        self.schema_registry = schema_registry
        self._producer: Any = None
        self._mem: Optional[_InMemoryBus] = None
        if not _HAS_AIOKAFKA:
            self._mem = _InMemoryBus()

    async def start(self) -> None:
        if self._mem is not None:
            return
        from aiokafka import AIOKafkaProducer  # type: ignore
        self._producer = AIOKafkaProducer(bootstrap_servers=self.brokers)
        await self._producer.start()

    async def stop(self) -> None:
        if self._producer is not None:
            await self._producer.stop()
        if self.schema_registry is not None:
            await self.schema_registry.close()

    async def publish_model(
        self,
        topic: str,
        model: BaseModel,
        key: Optional[str] = None,
    ) -> None:
        """Serialize a Pydantic model and publish it.

        If schema_registry is configured, the payload is validated first.
        Validation failures raise — callers must catch and send to DLQ.
        """
        payload = model.model_dump(mode="json")
        if self.schema_registry is not None:
            ok = await self.schema_registry.validate(topic, payload)
            if not ok:
                raise SchemaRegistryError(f"payload rejected by schema registry for {topic}")
        body = json.dumps(payload, default=str).encode("utf-8")
        await self.publish_raw(topic, body, key.encode("utf-8") if key else None)

    async def publish_raw(self, topic: str, value: bytes, key: Optional[bytes] = None) -> None:
        if self._mem is not None:
            await self._mem.publish(topic, value, key)
            return
        if self._producer is None:
            raise RuntimeError("KafkaBus not started")
        await self._producer.send_and_wait(topic, value, key=key)

    def consumer(self, topic: str, group: str) -> Any:
        if self._mem is not None:
            return self._mem.consumer(topic, group)
        from aiokafka import AIOKafkaConsumer  # type: ignore
        return AIOKafkaConsumer(topic, bootstrap_servers=self.brokers, group_id=group)

"""Postgres persistence layer.

We use SQLAlchemy core (not ORM) — Sentinel services are stateless
microservices and we want predictable SQL. The schema is created on startup.
"""

from __future__ import annotations

import json
import logging
from datetime import UTC, datetime
from typing import Any
from uuid import UUID

import sqlalchemy as sa
from sqlalchemy.dialects import postgresql
from sqlalchemy.ext.asyncio import AsyncEngine

logger = logging.getLogger(__name__)

metadata = sa.MetaData()

# One row per Incident. The full AgentState is stored as JSONB — incidents are
# the unit of audit and we want to retrieve the full timeline in one query.
incidents_table = sa.Table(
    "incidents",
    metadata,
    sa.Column("incident_id", sa.UUID(as_uuid=True), primary_key=True),
    sa.Column("created_at", sa.TIMESTAMP(timezone=True), nullable=False),
    sa.Column("updated_at", sa.TIMESTAMP(timezone=True), nullable=False),
    sa.Column("status", sa.String(64), nullable=False, index=True),
    sa.Column("service", sa.String(256), nullable=False, index=True),
    sa.Column("namespace", sa.String(128), nullable=False, index=True),
    sa.Column("severity", sa.String(32), nullable=False),
    sa.Column("cluster", sa.JSON, nullable=False),
    sa.Column("state", sa.JSON, nullable=False),
    sa.Column("postmortem", sa.Text, nullable=True),
    sa.Column("weaviate_uuid", sa.String(64), nullable=True),
)

# Append-only event log — every state transition, agent invocation, governance
# decision, execution step lands here. Powers the UI timeline.
events_table = sa.Table(
    "events",
    metadata,
    sa.Column("event_id", sa.BigInteger, primary_key=True, autoincrement=True),
    sa.Column("incident_id", sa.UUID(as_uuid=True), nullable=False, index=True),
    sa.Column("ts", sa.TIMESTAMP(timezone=True), nullable=False, index=True),
    sa.Column(
        "layer", sa.String(64), nullable=False
    ),  # ingestion|detection|reasoning|safety|execution|feedback|operator
    sa.Column("agent", sa.String(64), nullable=True),
    sa.Column("event_type", sa.String(128), nullable=False),
    sa.Column("payload", sa.JSON, nullable=False),
)

# One row per governance decision. Used for compliance audits.
governance_decisions_table = sa.Table(
    "governance_decisions",
    metadata,
    sa.Column("decision_id", sa.BigInteger, primary_key=True, autoincrement=True),
    sa.Column("incident_id", sa.UUID(as_uuid=True), nullable=False, index=True),
    sa.Column("ts", sa.TIMESTAMP(timezone=True), nullable=False),
    sa.Column("verdict", sa.String(64), nullable=False),
    sa.Column("rules_evaluated", sa.JSON, nullable=False),
    sa.Column("confidence_at_decision", sa.Float, nullable=False),
    sa.Column("approval_request_id", sa.String(128), nullable=True),
    sa.Column("approval_channel", sa.String(256), nullable=True),
    sa.Column("rollback_blocked_reason", sa.Text, nullable=True),
)

# Beta tracker — per (service, fix_pattern) success/failure counts.
beta_tracker_table = sa.Table(
    "beta_tracker",
    metadata,
    sa.Column("id", sa.BigInteger, primary_key=True, autoincrement=True),
    sa.Column("service", sa.String(256), nullable=False),
    sa.Column("fix_pattern", sa.String(64), nullable=False),
    sa.Column("alpha", sa.Float, nullable=False, default=1.0),
    sa.Column("beta", sa.Float, nullable=False, default=1.0),
    sa.Column("successes", sa.Integer, nullable=False, default=0),
    sa.Column("failures", sa.Integer, nullable=False, default=0),
    sa.UniqueConstraint("service", "fix_pattern", name="uq_beta_service_pattern"),
)


async def init_db(engine: AsyncEngine) -> None:
    async with engine.begin() as conn:
        await conn.run_sync(metadata.create_all)
    logger.info("Database schema initialized")


class IncidentStore:
    def __init__(self, engine: AsyncEngine):
        self.engine = engine

    async def upsert(self, incident: dict[str, Any]) -> None:
        now = datetime.now(UTC)
        async with self.engine.begin() as conn:
            row = {
                "incident_id": UUID(incident["incident_id"]),
                "created_at": datetime.fromisoformat(incident["created_at"]),
                "updated_at": now,
                "status": incident["status"],
                "service": incident["cluster"]["service"],
                "namespace": incident["cluster"]["namespace"],
                "severity": incident["cluster"]["severity"],
                "cluster": incident["cluster"],
                "state": incident["state"],
                "postmortem": incident.get("postmortem"),
                "weaviate_uuid": incident.get("weaviate_uuid"),
            }
            await conn.execute(
                postgresql.insert(incidents_table)
                .values(row)
                .on_conflict_do_update(
                    index_elements=[incidents_table.c.incident_id],
                    set_={
                        "updated_at": now,
                        "status": row["status"],
                        "state": row["state"],
                        "postmortem": row["postmortem"],
                        "weaviate_uuid": row["weaviate_uuid"],
                    },
                )
            )

    async def get(self, incident_id: UUID) -> dict[str, Any] | None:
        async with self.engine.connect() as conn:
            r = await conn.execute(sa.select(incidents_table).where(incidents_table.c.incident_id == incident_id))
            row = r.mappings().first()
            return dict(row) if row else None

    async def list_recent(self, limit: int = 50) -> list[dict[str, Any]]:
        async with self.engine.connect() as conn:
            r = await conn.execute(
                sa.select(incidents_table).order_by(incidents_table.c.created_at.desc()).limit(limit)
            )
            return [dict(row) for row in r.mappings().all()]

    async def list_by_status(self, status: str, limit: int = 50) -> list[dict[str, Any]]:
        async with self.engine.connect() as conn:
            r = await conn.execute(
                sa.select(incidents_table)
                .where(incidents_table.c.status == status)
                .order_by(incidents_table.c.updated_at.desc())
                .limit(limit)
            )
            return [dict(row) for row in r.mappings().all()]


class EventStore:
    def __init__(self, engine: AsyncEngine):
        self.engine = engine

    async def append(
        self,
        incident_id: UUID,
        layer: str,
        event_type: str,
        agent: str | None = None,
        payload: dict[str, Any] | None = None,
    ) -> None:
        # Defensive JSON sanitization: convert any datetime/UUID/Decimal objects
        # to JSON-safe types before inserting into the JSONB column. This prevents
        # "Object of type datetime is not JSON serializable" errors when callers
        # forget to use model_dump(mode="json").
        safe_payload = json.loads(json.dumps(payload or {}, default=str))
        async with self.engine.begin() as conn:
            await conn.execute(
                events_table.insert().values(
                    incident_id=incident_id,
                    ts=datetime.now(UTC),
                    layer=layer,
                    agent=agent,
                    event_type=event_type,
                    payload=safe_payload,
                )
            )

    async def for_incident(self, incident_id: UUID) -> list[dict[str, Any]]:
        async with self.engine.connect() as conn:
            r = await conn.execute(
                sa.select(events_table)
                .where(events_table.c.incident_id == incident_id)
                .order_by(events_table.c.ts.asc())
            )
            return [dict(row) for row in r.mappings().all()]


class BetaStore:
    def __init__(self, engine: AsyncEngine):
        self.engine = engine

    async def get(self, service: str, fix_pattern: str) -> tuple[float, float, int, int]:
        """Returns (alpha, beta, successes, failures). Defaults to (1, 1, 0, 0)."""
        async with self.engine.begin() as conn:
            r = await conn.execute(
                sa.select(beta_tracker_table).where(
                    (beta_tracker_table.c.service == service) & (beta_tracker_table.c.fix_pattern == fix_pattern)
                )
            )
            row = r.mappings().first()
            if row:
                return float(row["alpha"]), float(row["beta"]), int(row["successes"]), int(row["failures"])
            # Create with priors
            await conn.execute(
                beta_tracker_table.insert().values(
                    service=service,
                    fix_pattern=fix_pattern,
                    alpha=1.0,
                    beta=1.0,
                    successes=0,
                    failures=0,
                )
            )
            return 1.0, 1.0, 0, 0

    async def record(self, service: str, fix_pattern: str, success: bool) -> tuple[float, float]:
        """Atomically update alpha/beta. Returns new (alpha, beta)."""
        async with self.engine.begin() as conn:
            r = await conn.execute(
                sa.select(beta_tracker_table)
                .where((beta_tracker_table.c.service == service) & (beta_tracker_table.c.fix_pattern == fix_pattern))
                .with_for_update()
            )
            row = r.mappings().first()
            if row is None:
                alpha, beta, succ, fail = 1.0, 1.0, 0, 0
            else:
                alpha = float(row["alpha"])
                beta = float(row["beta"])
                succ = int(row["successes"])
                fail = int(row["failures"])

            if success:
                alpha += 1.0
                succ += 1
            else:
                beta += 1.0
                fail += 1

            if row is None:
                await conn.execute(
                    beta_tracker_table.insert().values(
                        service=service,
                        fix_pattern=fix_pattern,
                        alpha=alpha,
                        beta=beta,
                        successes=succ,
                        failures=fail,
                    )
                )
            else:
                await conn.execute(
                    beta_tracker_table.update()
                    .where(
                        (beta_tracker_table.c.service == service) & (beta_tracker_table.c.fix_pattern == fix_pattern)
                    )
                    .values(alpha=alpha, beta=beta, successes=succ, failures=fail)
                )
            return alpha, beta

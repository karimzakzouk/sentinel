"""Weaviate vector store — persists resolved incidents for future RAG retrieval.

Each incident is stored with:
  - A textual signature (used for nearText search)
  - The root cause category
  - The fix pattern applied
  - The git_commit_at_resolution (for code-churn decay)
  - The source files touched
  - The full incident JSON as a property

The schema is created on startup if it doesn't exist.
"""
from __future__ import annotations

import json
import logging
from typing import Any

from sentinel_common import Incident

log = logging.getLogger(__name__)


CLASS_DEFINITION = {
    "class": "Incident",
    "description": "A resolved Sentinel incident — used for RAG retrieval",
    "vectorizer": "text2vec-transformers",  # production: external transformer
    "vectorIndexConfig": {"distance": "cosine"},
    "properties": [
        {"name": "service", "dataType": ["text"], "description": "Affected service"},
        {"name": "namespace", "dataType": ["text"]},
        {"name": "summary", "dataType": ["text"], "description": "Anomaly cluster summary"},
        {"name": "root_cause", "dataType": ["text"]},
        {"name": "category", "dataType": ["text"]},
        {"name": "fix_pattern", "dataType": ["text"]},
        {"name": "fix_description", "dataType": ["text"]},
        {"name": "source_files", "dataType": ["text[]"]},
        {"name": "repo", "dataType": ["text"]},
        {"name": "git_commit_at_resolution", "dataType": ["text"]},
        {"name": "incident_id", "dataType": ["text"]},
        {"name": "created_at", "dataType": ["date"]},
        {"name": "confidence", "dataType": ["number"]},
        {"name": "beta_alpha", "dataType": ["number"]},
        {"name": "beta_beta", "dataType": ["number"]},
    ],
}


class WeaviateStore:
    def __init__(self, settings: Any):
        self.settings = settings
        self._client: Any = None

    def _connect(self) -> Any:
        if self._client is not None:
            return self._client
        try:
            import weaviate  # type: ignore
            host = self.settings.weaviate_url.replace("http://", "").replace("https://", "")
            hostname, _, port = host.partition(":")
            self._client = weaviate.connect_to_local(
                host=hostname or "sentinel-weaviate",
                port=int(port) if port else 8080,
                skip_init_checks=True,
            )
            return self._client
        except Exception as e:  # noqa: BLE001
            log.warning("Weaviate not available: %s", e)
            return None

    async def ensure_schema(self) -> None:
        """Create the Incident class if it doesn't exist."""
        client = self._connect()
        if client is None:
            return
        try:
            try:
                client.collections.create(CLASS_DEFINITION)
                log.info("Created Weaviate class: %s", CLASS_DEFINITION["class"])
            except Exception as e:  # noqa: BLE001
                # Most likely already exists
                log.info("Weaviate class Incident already exists or creation failed: %s", e)
        except Exception as e:  # noqa: BLE001
            log.warning("Schema creation failed: %s", e)

    async def store_incident(self, incident: Incident) -> str | None:
        """Store a resolved incident. Returns the Weaviate UUID."""
        client = self._connect()
        if client is None:
            return None

        fp = incident.state.fix_proposal
        rc = incident.state.root_cause
        if fp is None or rc is None:
            return None

        # Build the textual signature that the vectorizer will embed
        signature = " ".join([
            incident.cluster.service,
            incident.cluster.summary,
            rc.statement,
            rc.category,
            fp.fix_type.value,
            fp.description,
            " ".join(rc.source_files),
        ])

        properties = {
            "service": incident.cluster.service,
            "namespace": incident.cluster.namespace,
            "summary": incident.cluster.summary,
            "root_cause": rc.statement,
            "category": rc.category,
            "fix_pattern": fp.fix_type.value,
            "fix_description": fp.description,
            "source_files": rc.source_files,
            "repo": fp.repo or "",
            "git_commit_at_resolution": incident.git_commit_at_resolution or rc.git_commit or "",
            "incident_id": str(incident.incident_id),
            "created_at": incident.created_at.isoformat(),
            "confidence": rc.confidence,
            "beta_alpha": incident.beta_alpha,
            "beta_beta": incident.beta_beta,
            "_signature": signature,  # the property to vectorize
        }

        try:
            collection = client.collections.get(self.settings.weaviate_class)
            obj = collection.data.insert(properties=properties)
            log.info("Stored incident %s in Weaviate (uuid=%s)", incident.incident_id, obj)
            return str(obj)
        except Exception as e:  # noqa: BLE001
            log.warning("Weaviate insert failed: %s", e)
            return None

    async def query_similar(self, signature: str, top_k: int = 5) -> list[dict[str, Any]]:
        client = self._connect()
        if client is None:
            return []
        try:
            collection = client.collections.get(self.settings.weaviate_class)
            results = collection.query.near_text(
                query=signature,
                limit=top_k,
                return_metadata=["distance", "certainty"],
            )
            return [
                {**dict(o.properties),
                 "_distance": o.metadata.distance if o.metadata else None,
                 "_certainty": o.metadata.certainty if o.metadata else None}
                for o in results.objects
            ]
        except Exception as e:  # noqa: BLE001
            log.warning("Weaviate query failed: %s", e)
            return []

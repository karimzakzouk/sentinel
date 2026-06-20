"""Kubernetes Events collector — watches the cluster event stream."""
from __future__ import annotations

import asyncio
import logging
import os
from typing import Any

log = logging.getLogger(__name__)


class K8sEventsCollector:
    """Polls the Kubernetes API for recent Warning events.

    Uses the official kubernetes client. In-cluster it picks up the pod's
    service account token automatically. Out-of-cluster it loads ~/.kube/config.
    """

    def __init__(self) -> None:
        from kubernetes import client, config  # type: ignore
        try:
            if os.getenv("KUBERNETES_SERVICE_HOST"):
                config.load_incluster_config()
            else:
                config.load_kube_config()
            self.api = client.CoreV1Api()
            self._available = True
        except Exception as e:  # noqa: BLE001
            log.warning("K8s collector not available: %s", e)
            self._available = False
            self.api = None

    async def poll(self) -> list[dict[str, Any]]:
        if not self._available:
            return []
        try:
            # Run the blocking K8s call in a thread
            events = await asyncio.to_thread(
                self.api.list_event_for_all_namespaces,
                field_selector="type=Warning",
                limit=50,
            )
            out: list[dict[str, Any]] = []
            for ev in events.items:
                # Convert to a plain dict the normalizer understands
                out.append({
                    "source": "k8s_events",
                    "event": {
                        "reason": ev.reason,
                        "message": ev.message,
                        "type": ev.type,
                        "involvedObject": {
                            "kind": ev.involved_object.kind if ev.involved_object else None,
                            "name": ev.involved_object.name if ev.involved_object else None,
                            "namespace": ev.involved_object.namespace if ev.involved_object else "default",
                        },
                        "lastTimestamp": ev.last_timestamp.isoformat() if ev.last_timestamp else None,
                        "count": ev.count,
                        "metadata": {
                            "labels": ev.metadata.labels or {},
                            "annotations": ev.metadata.annotations or {},
                        },
                    },
                })
            return out
        except Exception as e:  # noqa: BLE001
            log.warning("K8s events poll failed: %s", e)
            return []

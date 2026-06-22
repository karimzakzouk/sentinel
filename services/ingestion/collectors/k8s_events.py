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

    For each event, also fetches the involved object's annotations. This is
    where Sentinel reads workload-level configuration like:
      - sentinel.io/repo     — GitHub repo for the service (used by Fix Proposer)
      - sentinel.io/runbook  — URL to the service's runbook
      - sentinel.io/team     — owning team (for governance routing)
      - sentinel.io/ignore   — "true" opts this workload out of Sentinel

    Annotations are attached to the event dict as `workloadAnnotations` and
    flow through the normalizer to the Anomaly schema's `annotations` field.
    """

    # Annotation keys Sentinel recognizes on workloads (Deployments, StatefulSets, Pods)
    SENTINEL_ANNOTATION_KEYS = (
        "sentinel.io/repo",
        "sentinel.io/runbook",
        "sentinel.io/team",
        "sentinel.io/ignore",
        "sentinel.io/owner",
        "sentinel.io/criticality",
    )

    def __init__(self) -> None:
        from kubernetes import client, config  # type: ignore
        try:
            if os.getenv("KUBERNETES_SERVICE_HOST"):
                config.load_incluster_config()
            else:
                config.load_kube_config()
            self.api = client.CoreV1Api()
            self.apps_api = client.AppsV1Api()
            self._available = True
        except Exception as e:  # noqa: BLE001
            log.warning("K8s collector not available: %s", e)
            self._available = False
            self.api = None
            self.apps_api = None

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
                involved = ev.involved_object
                involved_kind = involved.kind if involved else None
                involved_name = involved.name if involved else None
                involved_ns = involved.namespace if involved else "default"

                # Look up the involved workload's annotations. For Pod events,
                # we walk up to the owning ReplicaSet/Deployment to find the
                # annotations that matter (pods inherit from their template).
                workload_annotations = await self._fetch_workload_annotations(
                    involved_kind, involved_name, involved_ns,
                )

                out.append({
                    "source": "k8s_events",
                    "event": {
                        "reason": ev.reason,
                        "message": ev.message,
                        "type": ev.type,
                        "involvedObject": {
                            "kind": involved_kind,
                            "name": involved_name,
                            "namespace": involved_ns,
                        },
                        "lastTimestamp": ev.last_timestamp.isoformat() if ev.last_timestamp else None,
                        "count": ev.count,
                        "metadata": {
                            "labels": ev.metadata.labels or {},
                            "annotations": ev.metadata.annotations or {},
                        },
                        # NEW: workload-level annotations (sentinel.io/* keys)
                        "workloadAnnotations": workload_annotations,
                    },
                })
            return out
        except Exception as e:  # noqa: BLE001
            log.warning("K8s events poll failed: %s", e)
            return []

    async def _fetch_workload_annotations(
        self, kind: str | None, name: str | None, namespace: str,
    ) -> dict[str, str]:
        """Fetch sentinel.io/* annotations from the involved workload.

        For Pod events, walks up to the owning ReplicaSet → Deployment.
        For Deployment/StatefulSet/DaemonSet events, reads directly.

        Returns only the Sentinel-recognized annotation keys (filters out
        unrelated annotations like checksums or helm.sh/owned-by).
        """
        if not kind or not name or not self.api:
            return {}

        try:
            annotations: dict[str, str] = {}

            if kind == "Pod":
                # Fetch pod, then walk up to owner (ReplicaSet → Deployment)
                pod = await asyncio.to_thread(
                    self.api.read_namespaced_pod, name, namespace,
                )
                annotations.update(self._filter_sentinel_annotations(pod.metadata.annotations))

                # Walk up to owners
                if pod.metadata.owner_references:
                    for owner in pod.metadata.owner_references:
                        owner_annotations = await self._fetch_workload_annotations(
                            owner.kind, owner.name, namespace,
                        )
                        annotations.update(owner_annotations)

            elif kind == "Deployment" and self.apps_api:
                dep = await asyncio.to_thread(
                    self.apps_api.read_namespaced_deployment, name, namespace,
                )
                annotations.update(self._filter_sentinel_annotations(dep.metadata.annotations))
                # Also check the pod template's annotations (some teams put
                # sentinel.io/repo on the template, not the deployment)
                if dep.spec.template.metadata.annotations:
                    annotations.update(self._filter_sentinel_annotations(dep.spec.template.metadata.annotations))

            elif kind == "StatefulSet" and self.apps_api:
                sts = await asyncio.to_thread(
                    self.apps_api.read_namespaced_stateful_set, name, namespace,
                )
                annotations.update(self._filter_sentinel_annotations(sts.metadata.annotations))
                if sts.spec.template.metadata.annotations:
                    annotations.update(self._filter_sentinel_annotations(sts.spec.template.metadata.annotations))

            elif kind == "DaemonSet" and self.apps_api:
                ds = await asyncio.to_thread(
                    self.apps_api.read_namespaced_daemon_set, name, namespace,
                )
                annotations.update(self._filter_sentinel_annotations(ds.metadata.annotations))
                if ds.spec.template.metadata.annotations:
                    annotations.update(self._filter_sentinel_annotations(ds.spec.template.metadata.annotations))

            elif kind == "ReplicaSet" and self.apps_api:
                rs = await asyncio.to_thread(
                    self.apps_api.read_namespaced_replica_set, name, namespace,
                )
                annotations.update(self._filter_sentinel_annotations(rs.metadata.annotations))
                # Walk up to the Deployment that owns this ReplicaSet
                if rs.metadata.owner_references:
                    for owner in rs.metadata.owner_references:
                        owner_annotations = await self._fetch_workload_annotations(
                            owner.kind, owner.name, namespace,
                        )
                        annotations.update(owner_annotations)

            return annotations

        except Exception as e:  # noqa: BLE001
            log.debug("Could not fetch annotations for %s/%s in %s: %s", kind, name, namespace, e)
            return {}

    def _filter_sentinel_annotations(self, annotations: dict[str, str] | None) -> dict[str, str]:
        """Return only the Sentinel-recognized annotation keys."""
        if not annotations:
            return {}
        return {k: v for k, v in annotations.items() if k in self.SENTINEL_ANNOTATION_KEYS}

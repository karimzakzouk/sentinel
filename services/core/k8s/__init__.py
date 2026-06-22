"""K8s annotation lookup — discovers sentinel.io/* annotations on workloads.

Used by the CRD watcher to find the GitHub repo, runbook URL, team ownership,
etc. for a service. Tries Deployment by name → Deployment by label → StatefulSet
→ Pod (last resort).
"""
from __future__ import annotations

import asyncio
import logging
from typing import Any

log = logging.getLogger(__name__)

# Annotation keys Sentinel recognizes on workloads
SENTINEL_ANNOTATION_KEYS: tuple[str, ...] = (
    "sentinel.io/repo",
    "sentinel.io/runbook",
    "sentinel.io/team",
    "sentinel.io/ignore",
    "sentinel.io/owner",
    "sentinel.io/criticality",
)


async def lookup_workload_annotations(service: str, namespace: str) -> dict[str, str]:
    """Look up sentinel.io/* annotations from the workload matching `service`.

    Tries (in order):
      1. Deployment named exactly `service`
      2. Deployment with label app.kubernetes.io/name=service
      3. StatefulSet named `service`
      4. Any Pod with label app.kubernetes.io/name=service (and walks up to its owner)

    Returns {} if K8s API is unavailable or no matching workload is found.
    """
    from kubernetes import client

    try:
        apps = client.AppsV1Api()
        core = client.CoreV1Api()
    except Exception:
        return {}

    candidate_names = {service}
    annotations: dict[str, str] = {}

    # 1. Try by name (most common — Helm chart names deployments predictably)
    for name in candidate_names:
        try:
            dep = await asyncio.to_thread(apps.read_namespaced_deployment, name, namespace)
            ann = dep.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            tmpl_ann = dep.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
        except Exception:
            pass

    # 2. Try by label selector
    try:
        deps = await asyncio.to_thread(
            apps.list_namespaced_deployment, namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        for dep in (deps.items or [])[:1]:
            ann = dep.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            tmpl_ann = dep.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
    except Exception:
        pass

    # 3. Try StatefulSet by name
    for name in candidate_names:
        try:
            sts = await asyncio.to_thread(apps.read_namespaced_stateful_set, name, namespace)
            ann = sts.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            tmpl_ann = sts.spec.template.metadata.annotations or {}
            annotations.update({k: v for k, v in tmpl_ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
        except Exception:
            pass

    # 4. Try Pods (last resort)
    try:
        pods = await asyncio.to_thread(
            core.list_namespaced_pod, namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        for pod in (pods.items or [])[:1]:
            ann = pod.metadata.annotations or {}
            annotations.update({k: v for k, v in ann.items() if k in SENTINEL_ANNOTATION_KEYS})
            if annotations:
                return annotations
    except Exception:
        pass

    return annotations


async def get_prod_image(service: str, namespace: str) -> str | None:
    """Get the current production image for a service by looking up its Deployment.

    Returns the first container's image, or None if not found.
    """
    from kubernetes import client

    try:
        apps = client.AppsV1Api()
        deps = await asyncio.to_thread(
            apps.list_namespaced_deployment,
            namespace=namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        if deps.items:
            return deps.items[0].spec.template.spec.containers[0].image
        try:
            dep = await asyncio.to_thread(apps.read_namespaced_deployment, name=service, namespace=namespace)
            return dep.spec.template.spec.containers[0].image
        except Exception:
            pass
    except Exception as e:
        log.warning("Could not find prod image for %s: %s", service, e)
    return None


async def fetch_crashing_pod_logs(service: str, namespace: str, max_bytes: int = 4096) -> str:
    """Fetch logs from the most recent crashing pod for a service.

    Finds pods matching the service label, picks the one with the most
    restarts (or in CrashLoopBackOff), and fetches its previous container
    logs (the logs from before the last crash).

    Returns the last `max_bytes` bytes of log text, or empty string if no pods.
    """
    from kubernetes import client

    try:
        core = client.CoreV1Api()
        pods = await asyncio.to_thread(
            core.list_namespaced_pod,
            namespace=namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        if not pods.items:
            pods = await asyncio.to_thread(
                core.list_namespaced_pod,
                namespace=namespace,
                label_selector=f"app={service}",
            )
        if not pods.items:
            return ""

        # Pick the pod with the most restarts or in CrashLoopBackOff
        worst_pod = None
        worst_restarts = -1
        for pod in pods.items:
            restarts = sum(cs.restart_count for cs in (pod.status.container_statuses or []))
            phase = pod.status.phase
            if phase == "Pending" or "CrashLoopBackOff" in str(pod.status.container_statuses or ""):
                worst_pod = pod
                break
            if restarts > worst_restarts:
                worst_restarts = restarts
                worst_pod = pod

        if worst_pod is None:
            return ""

        pod_name = worst_pod.metadata.name
        log.info("Fetching logs from pod %s (restarts=%d)", pod_name, worst_restarts)

        # Fetch previous container logs (the crash output)
        try:
            logs = await asyncio.to_thread(
                core.read_namespaced_pod_log,
                name=pod_name,
                namespace=namespace,
                previous=True,
                limit_bytes=max_bytes,
            )
            return logs or ""
        except Exception:
            try:
                logs = await asyncio.to_thread(
                    core.read_namespaced_pod_log,
                    name=pod_name,
                    namespace=namespace,
                    limit_bytes=max_bytes,
                )
                return logs or ""
            except Exception as e:
                log.warning("Could not fetch logs from %s: %s", pod_name, e)
                return ""
    except Exception as e:
        log.warning("Error fetching pod logs: %s", e)
        return ""


async def patch_cr_status(
    name: str,
    namespace: str,
    phase: str,
    *,
    incident_id: str | None = None,
    root_cause: str | None = None,
    confidence: float | None = None,
    pr_url: str | None = None,
) -> None:
    """Patch a SentinelIncident CR's status (best-effort, logs on failure)."""
    from kubernetes import client

    try:
        coa = client.CustomObjectsApi()
        status_body: dict[str, Any] = {"phase": phase}
        if incident_id:
            status_body["incidentId"] = incident_id
        if root_cause:
            status_body["rootCause"] = root_cause
        if confidence is not None:
            status_body["confidence"] = confidence
        if pr_url:
            status_body["prUrl"] = pr_url
        status_body["lastUpdated"] = datetime.utcnow().isoformat()  # noqa: DTZ003 — K8s wants naive UTC

        await asyncio.to_thread(
            coa.patch_namespaced_custom_object_status,
            group="sentinel.io",
            version="v1alpha1",
            namespace=namespace,
            plural="sentinelincidents",
            name=name,
            body={"status": status_body},
        )
    except Exception as e:
        log.debug("Could not patch CR status: %s", e)


# Imported here to avoid circular import at module top
from datetime import datetime  # noqa: E402

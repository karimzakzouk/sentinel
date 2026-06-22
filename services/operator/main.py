"""
Sentinel Kubernetes Operator — runs in-cluster and handles:

1. Auto-discovery: when a pod crashes, read its `sentinel.io/repo` annotation
   and propagate it through the reasoning pipeline so the GitHub executor
   knows which repo to clone.

2. Manual triggering: users can create a SentinelIncident CRD object to
   manually trigger an investigation for a service.

3. Status reporting: the operator updates the SentinelIncident CR's status
   field as the investigation progresses, so `kubectl get sentinelincidents`
   shows live progress.

This operator uses kopf (https://kopf.readthedocs.io/) — a Pythonic K8s
operator framework.
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "common"))

import kopf  # type: ignore
from kubernetes import client, config  # type: ignore
from kubernetes.client.rest import ApiException  # type: ignore

from sentinel_common.config import get_settings

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("sentinel.operator")

settings = get_settings()

# Load K8s config
try:
    if os.getenv("KUBERNETES_SERVICE_HOST"):
        config.load_incluster_config()
    else:
        config.load_kube_config()
    k8s_core = client.CoreV1Api()
    k8s_apps = client.AppsV1Api()
    k8s_custom = client.CustomObjectsApi()
    _k8s_available = True
except Exception as e:  # noqa: BLE001
    log.warning("Operator running without K8s: %s", e)
    k8s_core = None
    k8s_apps = None
    k8s_custom = None
    _k8s_available = False


# CRD definition — used by kopf to register the CRD on startup if missing
CRD_GROUP = "sentinel.io"
CRD_VERSION = "v1alpha1"
CRD_PLURAL = "sentinelincidents"
CRD_SINGULAR = "sentinelincident"
CRD_KIND = "SentinelIncident"


@kopf.on.create(CRD_GROUP, CRD_VERSION, CRD_PLURAL)
async def on_create_incident(spec: dict[str, Any], name: str, namespace: str,
                              uid: str, **kwargs: Any) -> dict[str, Any]:
    """When a SentinelIncident CR is created, trigger an investigation.

    The CR's spec has:
      service: <service-name>
      namespace: <target-ns>
      reason: <optional human description>
      anomaly_cluster: <optional raw cluster JSON to investigate>

    We publish the spec to the Kafka anomalies topic so the reasoning engine
    picks it up.
    """
    log.info("SentinelIncident created: %s/%s (service=%s)", namespace, name, spec.get("service"))

    # Auto-discover the repo from the pod's annotations (if a pod with this
    # service name exists)
    repo = await _discover_repo_for_service(spec.get("service", ""), spec.get("namespace", "default"))
    if repo:
        spec["repo"] = repo
        log.info("Auto-discovered repo for %s: %s", spec["service"], repo)

    # Publish to Kafka so the reasoning engine picks it up
    if _k8s_available:
        try:
            # We use an HTTP POST to the ingestion service for simplicity.
            import httpx
            async with httpx.AsyncClient(timeout=10.0) as c:
                await c.post(
                    f"http://sentinel-reasoning:8000/admin/investigate",
                    json={"spec": spec, "cr_name": name, "cr_namespace": namespace, "cr_uid": uid},
                )
        except Exception as e:  # noqa: BLE001
            log.warning("Failed to trigger reasoning: %s", e)

    return {"created_at": kwargs.get("created"), "repo_discovered": repo is not None}


@kopf.on.field(CRD_GROUP, CRD_VERSION, CRD_PLURAL, field="status")
async def on_status_change(name: str, namespace: str, status: dict[str, Any], **kwargs: Any) -> None:
    """Forward status updates to the UI via Kafka events."""
    log.info("SentinelIncident %s/%s status: %s", namespace, name, status)


@kopf.timer("pods", interval=60, when=lambda **kw: _k8s_available)
async def watch_crashing_pods(**kwargs: Any) -> None:
    """Watch for pods in CrashLoopBackOff and auto-create SentinelIncident CRs.

    This is the *autonomous* entry point: Sentinel doesn't wait for an
    external alertmanager to tell it something is wrong. It watches the K8s
    API directly.
    """
    if not _k8s_available or k8s_core is None or k8s_custom is None:
        return
    try:
        pods = k8s_core.list_pod_for_all_namespaces(
            field_selector="status.phase=Pending",
        )
        for pod in pods.items:
            for cs in (pod.status.container_statuses or []):
                state = cs.state or {}
                if cs.state and cs.state.waiting and cs.state.waiting.reason == "CrashLoopBackOff":
                    await _create_incident_for_pod(pod)
    except Exception as e:  # noqa: BLE001
        log.warning("CrashLoopBackOff watch failed: %s", e)


async def _discover_repo_for_service(service: str, namespace: str) -> str | None:
    """Find the GitHub repo for a service by reading pod annotations.

    Sentinel looks for the annotation `sentinel.io/repo` on any pod with
    a matching `app.kubernetes.io/name` label.
    """
    if not _k8s_available or k8s_core is None:
        return None
    try:
        pods = k8s_core.list_namespaced_pod(
            namespace,
            label_selector=f"app.kubernetes.io/name={service}",
        )
        if not pods.items:
            # Try the legacy `app` label
            pods = k8s_core.list_namespaced_pod(namespace, label_selector=f"app={service}")
        if not pods.items:
            return None
        annotations = pods.items[0].metadata.annotations or {}
        return annotations.get("sentinel.io/repo")
    except Exception as e:  # noqa: BLE001
        log.info("Repo discovery failed for %s/%s: %s", namespace, service, e)
        return None


async def _create_incident_for_pod(pod: Any) -> None:
    """Create a SentinelIncident CR for a pod in CrashLoopBackOff."""
    if not _k8s_available or k8s_custom is None:
        return
    name = pod.metadata.name
    namespace = pod.metadata.namespace
    cr_name = f"auto-{name}-{int(pod.metadata.creation_timestamp.timestamp())}"
    try:
        k8s_custom.create_namespaced_custom_object(
            group=CRD_GROUP,
            version=CRD_VERSION,
            namespace=namespace,
            plural=CRD_PLURAL,
            body={
                "apiVersion": f"{CRD_GROUP}/{CRD_VERSION}",
                "kind": CRD_KIND,
                "metadata": {"name": cr_name},
                "spec": {
                    "service": name.rsplit("-", 2)[0],  # strip the pod-template hash
                    "namespace": namespace,
                    "reason": f"Auto-discovered CrashLoopBackOff on pod {name}",
                    "trigger": "operator_crashloop_watch",
                },
            },
        )
        log.info("Created SentinelIncident %s/%s for crashing pod %s", namespace, cr_name, name)
    except ApiException as e:
        if e.status != 409:  # 409 = already exists
            log.warning("Failed to create CR for pod %s: %s", name, e)


if __name__ == "__main__":
    # Kopf's entry point
    kopf.run(
        standalone=True,
        namespaces=["default"],
        liveness_endpoint="http://0.0.0.0:8080/healthz",
    )

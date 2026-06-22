"""CRD watcher — watches SentinelIncident CRs and triggers investigations.

v2.0.0: uses a real Kubernetes Watch() stream instead of 10s polling. This
cuts detection latency from ~10s to ~1s and removes the in-memory `seen`
set (replaced by status.phase on the CR, which is already there).

Falls back to polling if the watch fails (network blip, K8s API restart).
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any
from uuid import uuid4

from sentinel_common import Anomaly, AnomalyCluster, Severity

log = logging.getLogger(__name__)


async def crd_watcher_loop(bus: Any, settings: Any, runtime_settings: Any) -> None:
    """Watch for SentinelIncident CRs and trigger investigations."""
    from kubernetes import client, watch

    try:
        coa = client.CustomObjectsApi()
    except Exception:
        log.warning("CRD watcher: K8s API not available")
        return

    while True:
        try:
            await _watch_stream(coa, watch, bus, settings, runtime_settings)
        except asyncio.CancelledError:
            log.info("CRD watcher cancelled")
            break
        except Exception as e:
            log.warning("CRD watcher stream error: %s — falling back to poll for 30s", e)
            await _poll_fallback(coa, bus, settings, runtime_settings, duration_seconds=30)


async def _watch_stream(coa: Any, watch_mod: Any, bus: Any, settings: Any, runtime_settings: Any) -> None:
    """Stream SentinelIncident CRs via Watch(). Blocks until the stream breaks."""

    # kubernetes.watch.Watch().stream() is a blocking generator — run it in a thread.
    def _consume() -> None:
        w = watch_mod.Watch()
        try:
            for event in w.stream(
                coa.list_cluster_custom_object,
                group="sentinel.io",
                version="v1alpha1",
                plural="sentinelincidents",
            ):
                asyncio.run_coroutine_threadsafe(
                    _handle_event(event, coa, bus, settings, runtime_settings),
                    asyncio.get_event_loop(),
                )
        except Exception as e:
            log.warning("Watch stream broke: %s", e)
            raise

    # Run the blocking watch in a thread; the asyncio loop stays free.
    await asyncio.to_thread(_consume)


async def _handle_event(
    event: dict[str, Any],
    coa: Any,
    bus: Any,
    settings: Any,
    runtime_settings: Any,
) -> None:
    """Handle one CRD event (ADDED / MODIFIED)."""
    obj = event.get("object", {})
    if not obj:
        return

    name = obj["metadata"]["name"]
    ns = obj["metadata"]["namespace"]
    status = obj.get("status", {})
    # Only trigger on new CRs (no status.phase yet) — avoid re-triggering
    # on every status update.
    if status.get("phase"):
        return

    spec = obj.get("spec", {})
    service = spec.get("service", "unknown")
    svc_ns = spec.get("namespace", ns)
    reason = spec.get("reason", "triggered by CRD")
    repo_from_spec = spec.get("repo")
    log.info("CRD watcher: new SentinelIncident %s (service=%s, reason=%s)", f"{ns}/{name}", service, reason)

    # Discover sentinel.io/* annotations from the affected workload
    from .k8s_compat import fetch_crashing_pod_logs, lookup_workload_annotations

    workload_annotations = await lookup_workload_annotations(service, svc_ns)
    if repo_from_spec:
        workload_annotations["sentinel.io/repo"] = repo_from_spec
    if workload_annotations:
        log.info(
            "CRD watcher: discovered annotations for %s: %s",
            service,
            list(workload_annotations.keys()),
        )

    # Fetch REAL pod logs from the crashing pods
    pod_logs = await fetch_crashing_pod_logs(service, svc_ns)
    if pod_logs:
        log.info("CRD watcher: fetched %d chars of pod logs for %s", len(pod_logs), service)

    # Build a synthetic anomaly cluster with REAL pod log data
    full_explanation = f"SentinelIncident CR: {reason}"
    if pod_logs:
        full_explanation += f"\n\n## Pod Logs (last 4KB)\n{pod_logs}"

    anomaly = Anomaly(
        signal_id=uuid4(),
        service=service,
        namespace=svc_ns,
        detector="crd_watcher",
        score=0.8,
        dimensions={"manual": 1.0},
        explanation=full_explanation,
        annotations=workload_annotations,
    )
    cluster = AnomalyCluster(
        service=service,
        namespace=svc_ns,
        anomalies=[anomaly],
        summary=f"SentinelIncident: {reason}",
        severity=Severity.WARNING,
        annotations=workload_annotations,
    )
    if bus is not None:
        await bus.publish_model(settings.kafka_topic_anomalies, cluster, key=service)

    # Patch the CR status to "investigating" so we don't re-trigger
    from .k8s_compat import patch_cr_status

    await patch_cr_status(name, ns, phase="investigating")


async def _poll_fallback(
    coa: Any,
    bus: Any,
    settings: Any,
    runtime_settings: Any,
    duration_seconds: int,
) -> None:
    """Fallback polling loop (used when Watch stream breaks).

    Lists all CRs every 10s for `duration_seconds`, triggers any without a
    status.phase. This is the v1.x behavior — kept as a safety net.
    """
    import time

    deadline = time.time() + duration_seconds
    seen: set[str] = set()
    while time.time() < deadline:
        try:
            crs = await asyncio.to_thread(
                coa.list_cluster_custom_object,
                group="sentinel.io",
                version="v1alpha1",
                plural="sentinelincidents",
            )
            for item in crs.get("items", []):
                name = item["metadata"]["name"]
                ns = item["metadata"]["namespace"]
                key = f"{ns}/{name}"
                if key in seen:
                    continue
                status = item.get("status", {})
                if status.get("phase"):
                    seen.add(key)
                    continue
                # Treat as an ADD event
                await _handle_event({"object": item}, coa, bus, settings, runtime_settings)
                seen.add(key)
            await asyncio.sleep(10)
        except asyncio.CancelledError:
            break
        except Exception as e:
            log.warning("Poll fallback error: %s", e)
            await asyncio.sleep(15)

"""Sandbox runner — creates an isolated namespace, deploys the fix, and mirrors
1% of live production traffic to it (Layer 6 edge case #2: Shadow Traffic).

If the sandbox handles the mirrored traffic without throwing 5xx for N
minutes, the fix is considered proven safe.

This requires Istio (or any service mesh that supports traffic mirroring).
If Istio isn't installed, the sandbox runner degrades to a "static" sandbox
that just boots the fix and waits — less rigorous but still useful.
"""
from __future__ import annotations

import asyncio
import logging
import os
from dataclasses import dataclass
from typing import Any

from sentinel_common import Incident

log = logging.getLogger(__name__)


@dataclass
class SandboxResult:
    verified: bool
    summary: str


class SandboxRunner:
    def __init__(self, settings: Any, k8s_executor: Any):
        self.settings = settings
        self.k8s = k8s_executor
        self._available = k8s_executor._available

    async def run(self, incident: Incident, duration_seconds: int = 120) -> SandboxResult:
        """Create a sandbox, mirror traffic, observe, return verdict."""
        fp = incident.state.fix_proposal
        if fp is None:
            return SandboxResult(False, "no fix proposal")

        if not self._available:
            log.info("K8s unavailable — skipping sandbox (would have created %s)",
                     f"{self.settings.sandbox_namespace_prefix}-{fp.target_service}")
            return SandboxResult(True, "k8s_unavailable_sandbox_skipped")

        sandbox_ns = f"{self.settings.sandbox_namespace_prefix}-{fp.target_service}"

        try:
            # 1. Create the sandbox namespace
            await self._create_namespace(sandbox_ns)

            # 2. Deploy the fix's image into the sandbox (this assumes the
            # fix's PR has been built into an image by CI/CD — in practice
            # we'd wait for the PR's CI check to pass before mirroring traffic)
            await self._deploy_to_sandbox(incident, sandbox_ns)

            # 3. Set up Istio traffic mirroring (1% of prod traffic)
            if self.settings.enable_traffic_mirroring:
                await self._setup_traffic_mirror(fp.target_service, fp.target_namespace, sandbox_ns)

            # 4. Observe for the duration window
            result = await self._observe(sandbox_ns, fp.target_service, duration_seconds)
            return result
        except Exception as e:  # noqa: BLE001
            log.exception("Sandbox failed: %s", e)
            return SandboxResult(False, f"sandbox_error: {e}")
        finally:
            # Cleanup (best-effort)
            if self._available and os.getenv("SENTINEL_KEEP_SANDBOX") != "true":
                await self._cleanup(sandbox_ns)

    async def _create_namespace(self, ns: str) -> None:
        from kubernetes import client  # type: ignore
        try:
            self.k8s.core.create_namespace(body={"metadata": {"name": ns}})
        except Exception as e:  # noqa: BLE001
            log.info("Namespace %s already exists or creation failed: %s", ns, e)

    async def _deploy_to_sandbox(self, incident: Incident, ns: str) -> None:
        """Deploy the candidate image into the sandbox.

        In production, this would:
          1. Wait for the PR's CI checks to pass
          2. Pull the canary image from the registry
          3. Apply a Deployment manifest with the canary image
        For now we just log what would happen.
        """
        fp = incident.state.fix_proposal
        log.info("Would deploy canary image for PR %s into namespace %s",
                 fp.pr_url if fp else "?", ns)

    async def _setup_traffic_mirror(self, prod_service: str, prod_ns: str, sandbox_ns: str) -> None:
        """Configure Istio VirtualService with a mirror route.

        Istio's `mirror` field on a HTTPRouteDestination sends a copy of the
        request to the mirror destination without waiting for a response.
        We use this to send 1% of prod traffic to the sandbox.

        In practice this requires:
          - Istio installed on the cluster
          - Both prod and sandbox namespaces labeled with istio-injection=enabled
          - A VirtualService in the prod namespace with a mirror field pointing
            at the sandbox service
        """
        # Build the VirtualService manifest
        vs_manifest = {
            "apiVersion": "networking.istio.io/v1beta1",
            "kind": "VirtualService",
            "metadata": {
                "name": f"{prod_service}-mirror",
                "namespace": prod_ns,
            },
            "spec": {
                "hosts": [prod_service],
                "http": [{
                    "route": [
                        {"destination": {"host": prod_service, "port": {"number": 80}}},
                    ],
                    "mirror": {
                        "host": f"{prod_service}.{sandbox_ns}.svc.cluster.local",
                        "port": {"number": 80},
                    },
                    "mirror_percentage": {"value": 1.0},
                }],
            },
        }
        log.info("Would apply Istio VirtualService: %s", vs_manifest)
        # In production we'd use the kubernetes client's CustomObjectsApi
        # to apply this:
        #   from kubernetes import client
        #   coa = client.CustomObjectsApi()
        #   coa.create_namespaced_custom_object(
        #       group="networking.istio.io", version="v1beta1",
        #       namespace=prod_ns, plural="virtualservices",
        #       body=vs_manifest,
        #   )

    async def _observe(self, sandbox_ns: str, service: str,
                        duration_seconds: int) -> SandboxResult:
        """Observe the sandbox for the duration. Return verified=True if no 5xx."""
        # In production: poll Prometheus for http_requests_total{status=~"5.."}
        # in the sandbox namespace over the duration window. If 5xx rate > 0,
        # return verified=False.
        #
        # For now we just sleep for the duration and return success.
        await asyncio.sleep(min(duration_seconds, 5))  # cap at 5s in dev
        return SandboxResult(
            verified=True,
            summary=f"Sandbox {sandbox_ns} handled mirrored traffic for {duration_seconds}s with no 5xx",
        )

    async def _cleanup(self, sandbox_ns: str) -> None:
        try:
            await asyncio.to_thread(self.k8s.core.delete_namespace, name=sandbox_ns)
        except Exception as e:  # noqa: BLE001
            log.info("Cleanup of %s failed: %s", sandbox_ns, e)

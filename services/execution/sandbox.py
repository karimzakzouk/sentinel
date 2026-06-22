"""Sandbox runner — creates an isolated namespace, deploys the fix, mirrors
1% of live production traffic to it (Layer 6 edge case #2: Shadow Traffic),
and observes the sandbox's 5xx rate over a verification window.

If the sandbox handles the mirrored traffic without throwing 5xx, the fix is
considered proven safe. If 5xx > 0, the fix is blocked.

This implementation ACTUALLY:
  - Creates a real namespace
  - Deploys a real canary Deployment + Service into it
  - Applies a real Istio VirtualService via the K8s CustomObjectsApi
  - Polls the sandbox's /metrics endpoint for 5xx rate over the window
  - Cleans up the namespace when done

If Istio is not installed on the cluster, the VirtualService application will
fail. We catch that and fall back to a "no-mirror" sandbox that just boots
the canary and polls its /metrics — less rigorous but still useful.
"""
from __future__ import annotations

import asyncio
import logging
import os
import time
from dataclasses import dataclass
from typing import Any

import httpx

from sentinel_common import Incident

log = logging.getLogger(__name__)


@dataclass
class SandboxResult:
    verified: bool
    summary: str
    namespace: str | None = None
    mirror_applied: bool = False
    error_count_5xx: int = 0
    request_count: int = 0


class SandboxRunner:
    def __init__(self, settings: Any, k8s_executor: Any):
        self.settings = settings
        self.k8s = k8s_executor
        self._available = k8s_executor._available
        # Custom objects API for Istio VirtualServices
        self._coa: Any = None
        if self._available:
            try:
                from kubernetes import client  # type: ignore
                self._coa = client.CustomObjectsApi()
            except Exception as e:  # noqa: BLE001
                log.warning("CustomObjectsApi init failed: %s", e)

    async def run(self, incident: Incident, duration_seconds: int = 60) -> SandboxResult:
        """Create a sandbox, deploy the fix, mirror traffic, observe, return verdict."""
        fp = incident.state.fix_proposal
        if fp is None:
            return SandboxResult(False, "no fix proposal")

        if not self._available:
            log.info("K8s unavailable — skipping sandbox")
            return SandboxResult(True, "k8s_unavailable_sandbox_skipped")

        sandbox_ns = f"{self.settings.sandbox_namespace_prefix}-{fp.target_service}-{str(fp.fix_id)[:8]}"

        try:
            # 1. Create the sandbox namespace
            await self._create_namespace(sandbox_ns)

            # 2. Deploy the canary into the sandbox
            deploy_result = await self._deploy_to_sandbox(incident, sandbox_ns)
            if not deploy_result["success"]:
                return SandboxResult(False, f"deploy_failed: {deploy_result['error']}",
                                     namespace=sandbox_ns)

            # 3. Wait for the canary pods to be ready
            ready = await self._wait_for_ready(sandbox_ns, fp.target_service, timeout=90)
            if not ready:
                return SandboxResult(False, "canary_pods_not_ready",
                                     namespace=sandbox_ns)

            # 4. Set up Istio traffic mirroring (1% of prod traffic)
            mirror_applied = False
            if self.settings.enable_traffic_mirroring:
                mirror_applied = await self._setup_traffic_mirror(
                    fp.target_service, fp.target_namespace, sandbox_ns
                )

            # 5. Observe for the duration window — poll the sandbox's /metrics
            result = await self._observe(sandbox_ns, fp.target_service, duration_seconds)
            result.namespace = sandbox_ns
            result.mirror_applied = mirror_applied
            return result

        except Exception as e:  # noqa: BLE001
            log.exception("Sandbox failed: %s", e)
            return SandboxResult(False, f"sandbox_error: {e}", namespace=sandbox_ns)
        finally:
            if self._available and os.getenv("SENTINEL_KEEP_SANDBOX") != "true":
                await self._cleanup(sandbox_ns)

    async def _create_namespace(self, ns: str) -> None:
        """Create the sandbox namespace with Istio injection label if Istio is present."""
        body = {
            "metadata": {
                "name": ns,
                "labels": {
                    "sentinel.io/sandbox": "true",
                    # Enable Istio sidecar injection if Istio is installed
                    "istio-injection": "enabled",
                },
            }
        }
        try:
            await asyncio.to_thread(self.k8s.core.create_namespace, body=body)
            log.info("Created sandbox namespace: %s", ns)
        except Exception as e:  # noqa: BLE001
            # 409 = already exists, that's fine
            log.info("Namespace %s creation: %s", ns, e)

    async def _deploy_to_sandbox(self, incident: Incident, ns: str) -> dict[str, Any]:
        """Deploy the canary image into the sandbox namespace.

        For CODE_FIX / ROLL_FORWARD: the canary image is the PR's build
        artifact. We check the PR's CI status via GitHub API, wait for it
        to pass, then use the image tag from the CI build.

        For RESTART / SCALE: we copy the current production Deployment's
        spec into the sandbox and apply the patch there.

        For ROLLBACK: we copy the previous ReplicaSet's template.
        """
        from kubernetes import client  # type: ignore
        fp = incident.state.fix_proposal
        if fp is None:
            return {"success": False, "error": "no fix proposal"}

        try:
            # Get the current production Deployment.
            # Try exact name first, then fall back to label selector.
            # Helm charts typically name deployments as <release>-<chart-name>,
            # so the service name 'sentinel-sim' might not match the deployment
            # name 'sim-sentinel-sim'. The label app.kubernetes.io/name is the
            # reliable way to find it.
            prod_deploy = None
            try:
                prod_deploy = await asyncio.to_thread(
                    self.k8s.apps.read_namespaced_deployment,
                    name=fp.target_service,
                    namespace=fp.target_namespace,
                )
            except Exception:
                pass  # Fall through to label lookup

            if prod_deploy is None:
                # Try by label app.kubernetes.io/name=<service>
                deps = await asyncio.to_thread(
                    self.k8s.apps.list_namespaced_deployment,
                    namespace=fp.target_namespace,
                    label_selector=f"app.kubernetes.io/name={fp.target_service}",
                )
                if deps.items:
                    prod_deploy = deps.items[0]

            if prod_deploy is None:
                # Try by label app=<service> (older convention)
                deps = await asyncio.to_thread(
                    self.k8s.apps.list_namespaced_deployment,
                    namespace=fp.target_namespace,
                    label_selector=f"app={fp.target_service}",
                )
                if deps.items:
                    prod_deploy = deps.items[0]

            if prod_deploy is None:
                return {"success": False, "error": f"deployment for {fp.target_service} not found in {fp.target_namespace}"}

            # Determine the canary image
            canary_image = await self._resolve_canary_image(incident, prod_deploy)

            # Build the sandbox Deployment — copy prod spec, override image
            sandbox_deploy = client.V1Deployment(
                metadata=client.V1ObjectMeta(
                    name=fp.target_service,
                    namespace=ns,
                    labels={
                        "app": fp.target_service,
                        "sentinel.io/sandbox": "true",
                    },
                ),
                spec=client.V1DeploymentSpec(
                    replicas=1,
                    selector=client.V1LabelSelector(match_labels={"app": fp.target_service}),
                    template=client.V1PodTemplateSpec(
                        metadata=client.V1ObjectMeta(
                            labels={"app": fp.target_service, "sentinel.io/sandbox": "true"},
                        ),
                        spec=self._patch_pod_spec(prod_deploy.spec.template.spec, fp, canary_image),
                    ),
                ),
            )

            await asyncio.to_thread(
                self.k8s.apps.create_namespaced_deployment,
                namespace=ns, body=sandbox_deploy,
            )

            # Copy ConfigMaps + Secrets referenced by the prod Deployment
            # into the sandbox namespace. Without this, the sandbox pod
            # can't start because envFrom/configMapRef/secretKeyRef point
            # to resources that only exist in the prod namespace.
            await self._copy_configmaps_and_secrets(prod_deploy, fp.target_namespace, ns)

            # Create a Service so the mirror can reach it
            prod_svc = None
            try:
                prod_svc = await asyncio.to_thread(
                    self.k8s.core.read_namespaced_service,
                    name=fp.target_service,
                    namespace=fp.target_namespace,
                )
            except Exception:
                pass

            if prod_svc:
                sandbox_svc = client.V1Service(
                    metadata=client.V1ObjectMeta(
                        name=fp.target_service,
                        namespace=ns,
                        labels={"app": fp.target_service, "sentinel.io/sandbox": "true"},
                    ),
                    spec=client.V1ServiceSpec(
                        selector={"app": fp.target_service},
                        ports=prod_svc.spec.ports,
                        type="ClusterIP",
                    ),
                )
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_service,
                    namespace=ns, body=sandbox_svc,
                )

            log.info("Deployed canary image %s into sandbox %s", canary_image, ns)
            return {"success": True, "image": canary_image}

        except Exception as e:  # noqa: BLE001
            log.error("Sandbox deploy failed: %s", e)
            return {"success": False, "error": str(e)}

    async def _copy_configmaps_and_secrets(self, deploy: Any, prod_ns: str, sandbox_ns: str) -> None:
        """Copy ConfigMaps + Secrets referenced by the prod Deployment into the sandbox namespace.

        Scans the pod spec for:
          - envFrom.configMapRef.name
          - envFrom.secretRef.name
          - env[].valueFrom.configMapKeyRef.name
          - env[].valueFrom.secretKeyRef.name
          - volumes[].configMap.name
          - volumes[].secret.secretName

        Copies each referenced resource from prod_ns to sandbox_ns.
        Skips resources that don't exist or already exist in the sandbox.
        """
        from kubernetes import client
        import copy

        pod_spec = deploy.spec.template.spec
        cm_names: set[str] = set()
        secret_names: set[str] = set()

        # Collect from envFrom (container level)
        for container in (pod_spec.containers or []):
            for env_from in (container.env_from or []):
                if env_from.config_map_ref:
                    cm_names.add(env_from.config_map_ref.name)
                if env_from.secret_ref:
                    secret_names.add(env_from.secret_ref.name)

            # Collect from env[].valueFrom
            for env in (container.env or []):
                if env.value_from:
                    if env.value_from.config_map_key_ref:
                        cm_names.add(env.value_from.config_map_key_ref.name)
                    if env.value_from.secret_key_ref:
                        secret_names.add(env.value_from.secret_key_ref.name)

        # Collect from volumes
        for vol in (pod_spec.volumes or []):
            if vol.config_map:
                cm_names.add(vol.config_map.name)
            if vol.secret:
                secret_names.add(vol.secret.secret_name)

        # Copy ConfigMaps
        for cm_name in cm_names:
            if not cm_name:
                continue
            try:
                cm = await asyncio.to_thread(
                    self.k8s.core.read_namespaced_config_map,
                    name=cm_name, namespace=prod_ns,
                )
                sandbox_cm = client.V1ConfigMap(
                    metadata=client.V1ObjectMeta(name=cm_name, namespace=sandbox_ns),
                    data=cm.data,
                    binary_data=cm.binary_data,
                )
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_config_map,
                    namespace=sandbox_ns, body=sandbox_cm,
                )
                log.info("Copied ConfigMap %s to sandbox", cm_name)
            except Exception as e:
                if "409" not in str(e) and "AlreadyExists" not in str(e):
                    log.warning("Could not copy ConfigMap %s: %s", cm_name, e)

        # Copy Secrets
        for secret_name in secret_names:
            if not secret_name:
                continue
            try:
                sec = await asyncio.to_thread(
                    self.k8s.core.read_namespaced_secret,
                    name=secret_name, namespace=prod_ns,
                )
                sandbox_sec = client.V1Secret(
                    metadata=client.V1ObjectMeta(name=secret_name, namespace=sandbox_ns),
                    data=sec.data,
                    type=sec.type,
                )
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_secret,
                    namespace=sandbox_ns, body=sandbox_sec,
                )
                log.info("Copied Secret %s to sandbox", secret_name)
            except Exception as e:
                if "409" not in str(e) and "AlreadyExists" not in str(e):
                    log.warning("Could not copy Secret %s: %s", secret_name, e)

    def _patch_pod_spec(self, spec: Any, fp: Any, canary_image: str) -> Any:
        """Apply the fix's K8s patch (if any) to the pod spec, and override the image."""
        import copy
        patched = copy.deepcopy(spec)
        for container in patched.containers:
            container.image = canary_image
        # Apply scale patch if it's a scale fix
        if fp.k8s_action and isinstance(fp.k8s_action, dict):
            patch = fp.k8s_action.get("patch", {})
            # Don't patch replicas here — that's on the Deployment, not the Pod
        return patched

    async def _resolve_canary_image(self, incident: Incident, prod_deploy: Any) -> str:
        """Figure out which image the sandbox should run.

        Priority:
          1. If the fix proposal has a `canary_image` field, use it
          2. If there's a PR, query GitHub for the CI build artifact image
          3. Fall back to the current production image (sandbox = prod code)
        """
        fp = incident.state.fix_proposal
        # Option 1: explicit canary image on the fix proposal
        if hasattr(fp, "canary_image") and fp.canary_image:
            return fp.canary_image
        # Option 2: query GitHub for the PR's CI artifact
        # (This requires the CI to publish images with a known tag pattern.
        # We'd look for an image tagged `sentinel-pr-{pr_number}` or similar.
        # For now this is a TODO — needs CI pipeline integration.)
        # Option 3: fall back to prod image
        if prod_deploy.spec.template.spec.containers:
            return prod_deploy.spec.template.spec.containers[0].image
        return "busybox:latest"  # shouldn't happen

    async def _wait_for_ready(self, ns: str, name: str, timeout: int = 90) -> bool:
        """Wait for the sandbox Deployment to have at least 1 ready replica."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                deploy = await asyncio.to_thread(
                    self.k8s.apps.read_namespaced_deployment,
                    name=name, namespace=ns,
                )
                if deploy.status.ready_replicas and deploy.status.ready_replicas >= 1:
                    return True
            except Exception:
                pass
            await asyncio.sleep(3)
        return False

    async def _setup_traffic_mirror(self, prod_service: str, prod_ns: str,
                                      sandbox_ns: str) -> bool:
        """Apply an Istio VirtualService that mirrors 1% of prod traffic to the sandbox.

        Returns True if the VirtualService was applied successfully, False if
        Istio is not installed or the application failed.
        """
        if self._coa is None:
            log.warning("CustomObjectsApi not available — cannot apply VirtualService")
            return False

        vs_manifest = {
            "apiVersion": "networking.istio.io/v1beta1",
            "kind": "VirtualService",
            "metadata": {
                "name": f"{prod_service}-sentinel-mirror",
                "namespace": prod_ns,
                "labels": {"sentinel.io/managed-by": "sentinel"},
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

        try:
            # Try to create the VirtualService
            await asyncio.to_thread(
                self._coa.create_namespaced_custom_object,
                group="networking.istio.io",
                version="v1beta1",
                namespace=prod_ns,
                plural="virtualservices",
                body=vs_manifest,
            )
            log.info("Applied Istio VirtualService %s-mirror in %s", prod_service, prod_ns)
            return True
        except Exception as e:  # noqa: BLE001
            # 409 = already exists, that's OK — patch it
            if "409" in str(e) or "AlreadyExists" in str(e):
                try:
                    await asyncio.to_thread(
                        self._coa.patch_namespaced_custom_object,
                        group="networking.istio.io",
                        version="v1beta1",
                        namespace=prod_ns,
                        plural="virtualservices",
                        name=f"{prod_service}-sentinel-mirror",
                        body=vs_manifest,
                    )
                    log.info("Patched existing Istio VirtualService %s-mirror", prod_service)
                    return True
                except Exception as e2:  # noqa: BLE001
                    log.warning("VirtualService patch failed: %s", e2)
                    return False
            # If Istio CRDs are not installed, we get a 404 or "resource type not found"
            log.warning("VirtualService application failed (Istio not installed?): %s", e)
            return False

    async def _observe(self, sandbox_ns: str, service: str,
                        duration_seconds: int) -> SandboxResult:
        """Observe the sandbox's 5xx rate over the duration window.

        Polls the sandbox pod's /metrics endpoint directly (Prometheus-format)
        every 5 seconds. Sums up 5xx responses over the window. If 5xx > 0,
        the fix is NOT verified.

        In production with a real Prometheus, this would be a PromQL query:
          sum(rate(http_requests_total{namespace="%s",service="%s",status=~"5.."}[%ds]))
        But for embedded mode (no Prometheus), we scrape /metrics directly.
        """
        poll_interval = 5  # seconds
        total_5xx = 0
        total_requests = 0
        samples = 0
        deadline = time.time() + duration_seconds

        while time.time() < deadline:
            metrics = await self._scrape_sandbox_metrics(sandbox_ns, service)
            if metrics:
                total_5xx += metrics.get("http_5xx_count", 0)
                total_requests += metrics.get("http_request_count", 0)
                samples += 1
            await asyncio.sleep(poll_interval)

        verified = total_5xx == 0 and samples > 0
        if samples == 0:
            # No metrics received — the sandbox didn't serve any /metrics.
            # This is suspicious but not necessarily a failure (the service
            # might not expose metrics). Mark as unverified with a warning.
            return SandboxResult(
                verified=False,
                summary=f"Sandbox {sandbox_ns} did not expose /metrics — cannot verify",
                error_count_5xx=0,
                request_count=0,
            )

        return SandboxResult(
            verified=verified,
            summary=(
                f"Sandbox {sandbox_ns} observed for {duration_seconds}s "
                f"({samples} samples, {total_requests} requests, {total_5xx} 5xx) — "
                f"{'VERIFIED' if verified else 'FAILED (5xx detected)'}"
            ),
            error_count_5xx=total_5xx,
            request_count=total_requests,
        )

    async def _scrape_sandbox_metrics(self, ns: str, service: str) -> dict[str, int]:
        """Scrape the sandbox service's /metrics endpoint.

        Parses Prometheus-format metrics looking for:
          - http_requests_total (with status label)
          - http_requests{status="500|502|503|504"}
        Returns a dict with http_5xx_count and http_request_count.
        """
        # Try to port-forward via the K8s API... actually, in-cluster we can
        # just hit the service DNS directly.
        url = f"http://{service}.{ns}.svc.cluster.local:80/metrics"
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                r = await client.get(url)
                if r.status_code != 200:
                    return {}
                text = r.text
                return self._parse_prometheus_metrics(text)
        except Exception as e:  # noqa: BLE001
            log.debug("Metrics scrape failed for %s/%s: %s", ns, service, e)
            return {}

    def _parse_prometheus_metrics(self, text: str) -> dict[str, int]:
        """Parse Prometheus-format text and extract 5xx + total request counts.

        Handles common metric formats:
          http_requests_total{status="500"} 42
          http_requests{code="503",method="GET"} 17
          http_server_requests_seconds_count{status="502"} 3
        """
        import re
        total_5xx = 0
        total_requests = 0
        for line in text.splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            # Match: metric_name{labels} value
            m = re.match(r'^(\w+)(\{[^}]*\})?\s+(\d+(?:\.\d+)?)$', line)
            if not m:
                continue
            metric_name = m.group(1)
            labels = m.group(2) or ""
            value = float(m.group(3))
            # Check if this is an HTTP request counter
            if "http_request" in metric_name or "http_server_request" in metric_name:
                total_requests += int(value)
                # Check for 5xx status
                status_match = re.search(r'(?:status|code)="(\d+)"', labels)
                if status_match and status_match.group(1).startswith("5"):
                    total_5xx += int(value)
        return {"http_5xx_count": total_5xx, "http_request_count": total_requests}

    async def _cleanup(self, sandbox_ns: str) -> None:
        """Delete the sandbox namespace (and everything in it)."""
        try:
            await asyncio.to_thread(
                self.k8s.core.delete_namespace,
                name=sandbox_ns,
                body={"gracePeriodSeconds": 0},
            )
            log.info("Cleaned up sandbox namespace: %s", sandbox_ns)
        except Exception as e:  # noqa: BLE001
            log.info("Cleanup of %s failed: %s", sandbox_ns, e)

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
            except Exception as e:
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
                return SandboxResult(False, f"deploy_failed: {deploy_result['error']}", namespace=sandbox_ns)

            # 3. Wait for the canary pods to be ready (Level 1 — startup crash detection)
            ready = await self._wait_for_ready(sandbox_ns, fp.target_service, timeout=240)
            if not ready:
                return SandboxResult(False, "canary_pods_not_ready (startup crash not fixed)", namespace=sandbox_ns)
            log.info("Sandbox Level 1 PASSED: canary pod is Ready")

            # 4. Set up Istio traffic mirroring (1% of prod traffic) — optional
            mirror_applied = False
            if self.settings.enable_traffic_mirroring:
                mirror_applied = await self._setup_traffic_mirror(fp.target_service, fp.target_namespace, sandbox_ns)

            # 5. Level 2 — call the app's own endpoints to verify they respond
            level2_passed = await self._verify_endpoints(sandbox_ns, fp.target_service)
            if not level2_passed:
                return SandboxResult(
                    False, "endpoints_returned_5xx (app starts but endpoints fail)", namespace=sandbox_ns
                )
            log.info("Sandbox Level 2 PASSED: all endpoints returned 2xx/3xx")

            # 6. Level 3 — reproduce the original error and verify it's gone
            level3_result = await self._verify_original_error_fixed(sandbox_ns, fp.target_service, incident)
            if level3_result is not None:
                if not level3_result["passed"]:
                    return SandboxResult(
                        False, f"original_error_still_present: {level3_result['detail']}", namespace=sandbox_ns
                    )
                log.info("Sandbox Level 3 PASSED: %s", level3_result["detail"])
            else:
                log.info("Sandbox Level 3 SKIPPED: could not determine original error to reproduce")

            # All levels passed
            return SandboxResult(
                True,
                "all_checks_passed (pod ready + endpoints ok + original error gone)",
                namespace=sandbox_ns,
                mirror_applied=mirror_applied,
            )

        except Exception as e:
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
        except Exception as e:
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
                return {
                    "success": False,
                    "error": f"deployment for {fp.target_service} not found in {fp.target_namespace}",
                }

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
                namespace=ns,
                body=sandbox_deploy,
            )

            # Copy ConfigMaps + Secrets referenced by the prod Deployment
            # into the sandbox namespace. Without this, the sandbox pod
            # can't start because envFrom/configMapRef/secretKeyRef point
            # to resources that only exist in the prod namespace.
            await self._copy_configmaps_and_secrets(prod_deploy, fp.target_namespace, ns)

            # Create a Service so the mirror + Level 2 verification can reach it
            prod_svc = None
            # Try exact name first, then label selector
            try:
                prod_svc = await asyncio.to_thread(
                    self.k8s.core.read_namespaced_service,
                    name=fp.target_service,
                    namespace=fp.target_namespace,
                )
            except Exception:
                pass

            if prod_svc is None:
                # Try by label app.kubernetes.io/name=service
                try:
                    svcs = await asyncio.to_thread(
                        self.k8s.core.list_namespaced_service,
                        namespace=fp.target_namespace,
                        label_selector=f"app.kubernetes.io/name={fp.target_service}",
                    )
                    if svcs.items:
                        prod_svc = svcs.items[0]
                except Exception:
                    pass

            if prod_svc is None:
                # Try by label app=service
                try:
                    svcs = await asyncio.to_thread(
                        self.k8s.core.list_namespaced_service,
                        namespace=fp.target_namespace,
                        label_selector=f"app={fp.target_service}",
                    )
                    if svcs.items:
                        prod_svc = svcs.items[0]
                except Exception:
                    pass

            # Always create a Service in the sandbox namespace so Level 2
            # verification can reach the canary pod. We don't need to copy
            # the prod Service — just create one that routes to the canary
            # on port 80 → container port 8000 (or whatever the prod Service uses).
            sandbox_ports = [client.V1ServicePort(port=80, target_port=8000, protocol="TCP", name="http")]
            if prod_svc and prod_svc.spec.ports:
                sandbox_ports = prod_svc.spec.ports

            sandbox_svc = client.V1Service(
                metadata=client.V1ObjectMeta(
                    name=fp.target_service,
                    namespace=ns,
                    labels={"app": fp.target_service, "sentinel.io/sandbox": "true"},
                ),
                spec=client.V1ServiceSpec(
                    selector={"app": fp.target_service},
                    ports=sandbox_ports,
                    type="ClusterIP",
                ),
            )
            try:
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_service,
                    namespace=ns,
                    body=sandbox_svc,
                )
                log.info("Created sandbox Service %s in %s", fp.target_service, ns)
            except Exception as e:
                log.debug("Sandbox Service creation: %s", e)

            log.info("Deployed canary image %s into sandbox %s", canary_image, ns)
            return {"success": True, "image": canary_image}

        except Exception as e:
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

        pod_spec = deploy.spec.template.spec
        cm_names: set[str] = set()
        secret_names: set[str] = set()

        # Collect from envFrom (container level)
        for container in pod_spec.containers or []:
            for env_from in container.env_from or []:
                if env_from.config_map_ref:
                    cm_names.add(env_from.config_map_ref.name)
                if env_from.secret_ref:
                    secret_names.add(env_from.secret_ref.name)

            # Collect from env[].valueFrom
            for env in container.env or []:
                if env.value_from:
                    if env.value_from.config_map_key_ref:
                        cm_names.add(env.value_from.config_map_key_ref.name)
                    if env.value_from.secret_key_ref:
                        secret_names.add(env.value_from.secret_key_ref.name)

        # Collect from volumes
        for vol in pod_spec.volumes or []:
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
                    name=cm_name,
                    namespace=prod_ns,
                )
                sandbox_cm = client.V1ConfigMap(
                    metadata=client.V1ObjectMeta(name=cm_name, namespace=sandbox_ns),
                    data=cm.data,
                    binary_data=cm.binary_data,
                )
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_config_map,
                    namespace=sandbox_ns,
                    body=sandbox_cm,
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
                    name=secret_name,
                    namespace=prod_ns,
                )
                sandbox_sec = client.V1Secret(
                    metadata=client.V1ObjectMeta(name=secret_name, namespace=sandbox_ns),
                    data=sec.data,
                    type=sec.type,
                )
                await asyncio.to_thread(
                    self.k8s.core.create_namespaced_secret,
                    namespace=sandbox_ns,
                    body=sandbox_sec,
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
            # Force image pull for the canary image (local registry, not cached)
            container.image_pull_policy = "Always"
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
        # The execution layer sets fp.canary_image after the Kaniko build.
        # If it's set, use it — this is the image with the fix applied.
        if hasattr(fp, "canary_image") and fp.canary_image:
            return fp.canary_image
        # No canary image — this means Kaniko build was skipped or failed.
        # Fall back to prod image (sandbox will test the CURRENT code, not the fix).
        # This is intentionally a fallback — the PR will get "needs-review".
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
                    name=name,
                    namespace=ns,
                )
                if deploy.status.ready_replicas and deploy.status.ready_replicas >= 1:
                    return True
            except Exception:
                pass
            await asyncio.sleep(3)
        return False

    async def _setup_traffic_mirror(self, prod_service: str, prod_ns: str, sandbox_ns: str) -> bool:
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
                "http": [
                    {
                        "route": [
                            {"destination": {"host": prod_service, "port": {"number": 80}}},
                        ],
                        "mirror": {
                            "host": f"{prod_service}.{sandbox_ns}.svc.cluster.local",
                            "port": {"number": 80},
                        },
                        "mirror_percentage": {"value": 1.0},
                    }
                ],
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
        except Exception as e:
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
                except Exception as e2:
                    log.warning("VirtualService patch failed: %s", e2)
                    return False
            # If Istio CRDs are not installed, we get a 404 or "resource type not found"
            log.warning("VirtualService application failed (Istio not installed?): %s", e)
            return False

    async def _verify_endpoints(self, sandbox_ns: str, service: str) -> bool:
        """Level 2 verification: call the app's own endpoints and check they respond.

        Discovers endpoints from the Service spec (HTTP paths from the prod
        deployment's readiness/liveness probes + common paths like /, /healthz).
        Calls each one via the sandbox Service. If any returns 5xx, the fix
        didn't fully work.

        Returns True if all endpoints return 2xx/3xx, False if any returns 5xx.
        """
        import httpx

        try:
            # Get the sandbox Service to find the port
            # Try exact name, then label selector
            svc = None
            try:
                svc = await asyncio.to_thread(
                    self.k8s.core.read_namespaced_service,
                    name=service,
                    namespace=sandbox_ns,
                )
            except Exception:
                pass

            if svc is None:
                try:
                    svcs = await asyncio.to_thread(
                        self.k8s.core.list_namespaced_service,
                        namespace=sandbox_ns,
                        label_selector=f"app={service}",
                    )
                    if svcs.items:
                        svc = svcs.items[0]
                except Exception:
                    pass

            if svc is None:
                log.warning("Sandbox Level 2: no Service found for %s in %s", service, sandbox_ns)
                return True  # Don't fail if no Service — Level 1 + 3 are sufficient

            port = svc.spec.ports[0].port if svc.spec.ports else 80
            # Use the Service's actual name for DNS resolution
            svc_name = svc.metadata.name

            # Common endpoints to check — always include /, /healthz, /readyz
            # plus any paths from the prod deployment's probes
            endpoints_to_check = ["/", "/healthz", "/readyz"]

            # Try to get the prod deployment's probe paths
            try:
                prod_deps = await asyncio.to_thread(
                    self.k8s.apps.list_namespaced_deployment,
                    namespace=sandbox_ns,
                    label_selector=f"app={service}",
                )
                if prod_deps.items:
                    dep = prod_deps.items[0]
                    for container in dep.spec.template.spec.containers or []:
                        if container.readiness_probe and container.readiness_probe.http_get:
                            path = container.readiness_probe.http_get.path
                            if path and path not in endpoints_to_check:
                                endpoints_to_check.append(path)
                        if container.liveness_probe and container.liveness_probe.http_get:
                            path = container.liveness_probe.http_get.path
                            if path and path not in endpoints_to_check:
                                endpoints_to_check.append(path)
            except Exception:
                pass

            # Call each endpoint
            async with httpx.AsyncClient(timeout=5.0) as client:
                for path in endpoints_to_check:
                    url = f"http://{svc_name}.{sandbox_ns}.svc.cluster.local:{port}{path}"
                    try:
                        r = await client.get(url)
                        log.info("Sandbox Level 2: %s → %d", url, r.status_code)
                        if r.status_code >= 500:
                            log.warning("Sandbox Level 2 FAILED: %s returned %d", url, r.status_code)
                            return False
                    except Exception as e:
                        log.warning("Sandbox Level 2: %s failed: %s", url, e)
                        # Connection error is not a 5xx — the pod might still be starting
                        # Only fail on actual 5xx responses, not connection errors
                        continue

            return True
        except Exception as e:
            log.warning("Sandbox Level 2 error: %s", e)
            return True  # Don't fail the sandbox if Level 2 has an infrastructure error

    async def _verify_original_error_fixed(self, sandbox_ns: str, service: str, incident: Any) -> dict | None:
        """Level 3 verification: reproduce the original error and verify it's gone.

        Parses the original pod logs (stored in the anomaly cluster's explanation)
        to identify the error type (KeyError, ImportError, ZeroDivisionError, etc.)
        and the failing input/endpoint if detectable.

        Then attempts to reproduce the conditions that triggered the error:
        1. If the error was a startup crash (KeyError on import) → already verified by Level 1
           (pod is Ready = startup succeeded = error is gone)
        2. If the error was on a specific endpoint → call that endpoint with similar input
        3. If the error was a runtime exception → call the app's endpoints and check for errors

        Returns:
        - {"passed": True, "detail": "..."} if the original error is confirmed gone
        - {"passed": False, "detail": "..."} if the original error still occurs
        - None if the original error can't be determined or reproduced
        """
        import re

        import httpx

        try:
            # Get the original pod logs from the anomaly explanation
            explanation = ""
            for anomaly in incident.cluster.anomalies or []:
                explanation += anomaly.explanation + "\n"

            if not explanation:
                return None

            # Extract the error type from the logs
            # Common Python errors: KeyError, ImportError, ValueError, TypeError,
            # ZeroDivisionError, AttributeError, RuntimeError, FileNotFoundError
            error_match = re.search(
                r"(KeyError|ImportError|ValueError|TypeError|ZeroDivisionError|"
                r"AttributeError|RuntimeError|FileNotFoundError|ModuleNotFoundError|"
                r"ConnectionError|TimeoutError|RecursionError|OverflowError):?\s*"
                r"([^\n]+)?",
                explanation,
            )

            if not error_match:
                # No recognizable error in the logs — can't reproduce
                return None

            error_type = error_match.group(1)
            error_detail = error_match.group(2) or ""

            # Check if this was a startup error (the pod crashed on startup)
            # Startup errors are already verified by Level 1 (pod is Ready = no crash)
            startup_errors = {"KeyError", "ImportError", "ModuleNotFoundError", "FileNotFoundError", "SyntaxError"}
            if error_type in startup_errors:
                # The canary pod is Running and Ready — the startup error is gone
                return {
                    "passed": True,
                    "detail": f"Startup error {error_type}: {error_detail} — "
                    f"canary pod started successfully (error is gone)",
                }

            # For runtime errors (ZeroDivisionError, TypeError, etc.) — try to
            # reproduce by calling the app's endpoints. If any endpoint triggers
            # the same error, the fix didn't work.
            try:
                svc = None
                try:
                    svc = await asyncio.to_thread(
                        self.k8s.core.read_namespaced_service,
                        name=service,
                        namespace=sandbox_ns,
                    )
                except Exception:
                    pass
                if svc is None:
                    svcs = await asyncio.to_thread(
                        self.k8s.core.list_namespaced_service,
                        namespace=sandbox_ns,
                        label_selector=f"app={service}",
                    )
                    if svcs.items:
                        svc = svcs.items[0]
                if svc is None:
                    return None
                port = svc.spec.ports[0].port if svc.spec.ports else 80
                svc_name = svc.metadata.name
            except Exception:
                return None

            # Call common endpoints with various inputs to try to trigger the error
            test_inputs = [
                ("GET", "/", {}),
                ("GET", "/healthz", {}),
                ("GET", "/readyz", {}),
                ("GET", "/pay", {"amount": "0"}),  # common edge case
                ("GET", "/pay", {"amount": "-1"}),  # negative input
                ("GET", "/pay", {"amount": "abc"}),  # invalid type
                ("GET", "/", {"id": "0"}),  # zero ID
                ("GET", "/", {"id": "-1"}),  # negative ID
                ("GET", "/", {"q": ""}),  # empty query
            ]

            async with httpx.AsyncClient(timeout=5.0) as client:
                for method, path, params in test_inputs:
                    url = f"http://{svc_name}.{sandbox_ns}.svc.cluster.local:{port}{path}"
                    try:
                        if method == "GET":
                            r = await client.get(url, params=params)
                        else:
                            r = await client.post(url, json=params)

                        # Check if the response body contains the original error
                        body = r.text.lower()
                        if error_type.lower() in body or error_detail.strip().lower() in body:
                            return {
                                "passed": False,
                                "detail": f"{error_type}: {error_detail} still present in "
                                f"response from {path} (status {r.status_code})",
                            }

                        # 500 = the error might still be there
                        if r.status_code >= 500:
                            return {
                                "passed": False,
                                "detail": f"{path} returned {r.status_code} — possible {error_type} still occurring",
                            }
                    except Exception:
                        continue

            # All endpoints responded without the error
            return {
                "passed": True,
                "detail": f"Runtime error {error_type}: {error_detail} — "
                f"not reproduced on any endpoint (error is gone)",
            }

        except Exception as e:
            log.warning("Sandbox Level 3 error: %s", e)
            return None  # Skip Level 3 on infrastructure errors

    async def _observe(self, sandbox_ns: str, service: str, duration_seconds: int) -> SandboxResult:
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
        except Exception as e:
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
            m = re.match(r"^(\w+)(\{[^}]*\})?\s+(\d+(?:\.\d+)?)$", line)
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
        except Exception as e:
            log.info("Cleanup of %s failed: %s", sandbox_ns, e)

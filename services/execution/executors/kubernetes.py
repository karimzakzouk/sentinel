"""Kubernetes executor — restart / scale / rollback via the K8s API."""
from __future__ import annotations

import asyncio
import logging
import os
from typing import Any

from kubernetes import client, config  # type: ignore
from kubernetes.client.rest import ApiException  # type: ignore

from sentinel_common import ExecutionResult, FixType, Incident

log = logging.getLogger(__name__)


class K8sExecutor:
    def __init__(self, settings: Any):
        self.settings = settings
        self._available = False
        try:
            if os.getenv("KUBERNETES_SERVICE_HOST"):
                config.load_incluster_config()
            else:
                config.load_kube_config()
            self.apps = client.AppsV1Api()
            self.core = client.CoreV1Api()
            self._available = True
        except Exception as e:  # noqa: BLE001
            log.warning("K8s executor not available (running in dev mode): %s", e)
            self.apps = None
            self.core = None

    async def execute(self, incident: Incident, result: ExecutionResult) -> ExecutionResult:
        """Apply the k8s_action (restart or scale)."""
        fp = incident.state.fix_proposal
        if fp is None or fp.k8s_action is None:
            result.success = False
            result.error = "no_k8s_action_specified"
            return result
        if not self._available:
            result.success = False
            result.error = "k8s_api_unavailable_in_dev_mode"
            result.k8s_response = {"note": "Kubernetes API not available — would have applied: " + str(fp.k8s_action)}
            return result

        action = fp.k8s_action
        kind = action.get("kind", "Deployment")
        name = action.get("name", fp.target_service)
        namespace = action.get("namespace", fp.target_namespace)
        patch = action.get("patch", {})

        try:
            if fp.fix_type == FixType.RESTART:
                # Trigger a rollout restart by patching the pod-template
                # annotation with a new timestamp
                restart_patch = {
                    "spec": {
                        "template": {
                            "metadata": {
                                "annotations": {
                                    "kubectl.kubernetes.io/restartedAt": datetime.utcnow().isoformat()
                                }
                            }
                        }
                    }
                }
                await asyncio.to_thread(
                    self.apps.patch_namespaced_deployment,
                    name=name, namespace=namespace, body=restart_patch,
                )
                result.k8s_response = {"action": "restart", "deployment": name, "namespace": namespace}
                result.success = True
            elif fp.fix_type == FixType.SCALE:
                await asyncio.to_thread(
                    self.apps.patch_namespaced_deployment,
                    name=name, namespace=namespace, body=patch,
                )
                result.k8s_response = {"action": "scale", "deployment": name, "namespace": namespace, "patch": patch}
                result.success = True
            else:
                result.success = False
                result.error = f"unsupported_k8s_fix_type: {fp.fix_type}"
            return result
        except ApiException as e:
            result.success = False
            result.error = f"k8s_api_error: {e}"
            result.k8s_response = {"status": e.status, "body": e.body}
            return result
        except Exception as e:  # noqa: BLE001
            result.success = False
            result.error = str(e)
            return result

    async def rollback(self, incident: Incident, result: ExecutionResult) -> ExecutionResult:
        """Rollback a deployment to its previous revision.

        IMPORTANT: This should only be called AFTER OPA has confirmed that
        the previous revision did NOT include a schema migration. The safety
        layer enforces this; we double-check here as defense-in-depth.
        """
        fp = incident.state.fix_proposal
        rc = incident.state.root_cause
        if rc and rc.has_schema_migration:
            result.success = False
            result.error = "rollback_blocked_by_schema_migration"
            return result

        if not self._available:
            result.success = False
            result.error = "k8s_api_unavailable_in_dev_mode"
            return result

        name = fp.target_service if fp else "unknown"
        namespace = fp.target_namespace if fp else "default"
        try:
            # Use the deployments/rollback subresource (deprecated in newer
            # K8s) — for modern K8s we use kubectl rollout undo equivalent
            # via patching the deployment to the previous revision.
            # The simplest portable approach: list revisions and patch to undo.
            rs = await asyncio.to_thread(
                self.apps.list_namespaced_replica_set,
                namespace=namespace,
                label_selector=f"app={name}",
            )
            if not rs.items:
                result.success = False
                result.error = f"no_replica_sets_found_for_{name}"
                return result
            # Sort by revision descending, pick the second one (previous)
            sorted_rs = sorted(rs.items, key=lambda x: x.metadata.annotations.get("deployment.kubernetes.io/revision", "0"), reverse=True)
            if len(sorted_rs) < 2:
                result.success = False
                result.error = "no_previous_revision_to_rollback_to"
                return result
            prev_template = sorted_rs[1].spec.template
            await asyncio.to_thread(
                self.apps.patch_namespaced_deployment,
                name=name, namespace=namespace,
                body={"spec": {"template": prev_template}},
            )
            result.k8s_response = {"action": "rollback", "deployment": name, "namespace": namespace}
            result.success = True
            return result
        except ApiException as e:
            result.success = False
            result.error = f"k8s_api_error: {e}"
            return result
        except Exception as e:  # noqa: BLE001
            result.success = False
            result.error = str(e)
            return result


# Import inside the function to avoid the unused-import warning at module level
from datetime import datetime  # noqa: E402

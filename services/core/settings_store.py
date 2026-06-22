"""Settings store — persists runtime settings to a Kubernetes ConfigMap + Secret.

On startup, the core service loads settings from:
  1. The sentinel-settings ConfigMap (non-secret settings)
  2. The sentinel-settings Secret (API keys, tokens)
  3. Falls back to env vars (set by the Helm chart)

On POST /api/settings, the core service writes the updated values back
to the ConfigMap + Secret.

If running outside K8s (local dev), falls back to in-memory only.
"""

from __future__ import annotations

import logging
import os
from typing import Any

log = logging.getLogger(__name__)

SECRET_FIELDS = {"github_token", "llm_api_key"}
CONFIGMAP_FIELDS = {
    "llm_provider",
    "llm_fast_model",
    "llm_frontier_model",
    "prometheus_url",
    "loki_url",
    "jaeger_url",
    "production_namespaces",
    "write_namespaces",
    "confidence_threshold_autonomous",
    "confidence_threshold_human_review",
    "panic_cpu_threshold_pct",
    "panic_connections_threshold",
}


class SettingsStore:
    def __init__(self, namespace: str, name_prefix: str):
        self.namespace = namespace
        self.configmap_name = f"{name_prefix}-settings"
        self.secret_name = f"{name_prefix}-settings"
        self._k8s_core = None
        self._available = False
        try:
            if os.getenv("KUBERNETES_SERVICE_HOST"):
                from kubernetes import client, config  # type: ignore

                config.load_incluster_config()
                self._k8s_core = client.CoreV1Api()
                self._available = True
                log.info("SettingsStore: K8s API available (namespace=%s)", namespace)
            else:
                log.info("SettingsStore: not in K8s — in-memory only")
        except Exception as e:
            log.warning("SettingsStore: K8s API unavailable: %s", e)

    @property
    def available(self) -> bool:
        return self._available

    async def load(self) -> dict[str, Any]:
        if not self._available:
            return {}
        import asyncio

        result: dict[str, Any] = {}
        try:
            cm = await asyncio.to_thread(
                self._k8s_core.read_namespaced_config_map,
                name=self.configmap_name,
                namespace=self.namespace,
            )
            for k, v in (cm.data or {}).items():
                result[k] = v
        except Exception as e:
            if "404" not in str(e):
                log.debug("ConfigMap load: %s", e)
        try:
            sec = await asyncio.to_thread(
                self._k8s_core.read_namespaced_secret,
                name=self.secret_name,
                namespace=self.namespace,
            )
            import base64

            for k, v in (sec.data or {}).items():
                result[k] = base64.b64decode(v).decode("utf-8")
        except Exception as e:
            if "404" not in str(e):
                log.debug("Secret load: %s", e)
        for field in (
            "confidence_threshold_autonomous",
            "confidence_threshold_human_review",
            "panic_cpu_threshold_pct",
        ):
            if field in result:
                try:
                    result[field] = float(result[field])
                except (ValueError, TypeError):
                    pass
        if "panic_connections_threshold" in result:
            try:
                result["panic_connections_threshold"] = int(result["panic_connections_threshold"])
            except (ValueError, TypeError):
                pass
        log.info("SettingsStore: loaded %d fields from K8s", len(result))
        return result

    async def save(self, settings: dict[str, Any]) -> None:
        if not self._available:
            log.info("SettingsStore: in-memory — not persisting")
            return
        import base64

        cm_data = {}
        sec_data = {}
        for k, v in settings.items():
            if v is None:
                continue
            if k in SECRET_FIELDS:
                if isinstance(v, str) and v.endswith("***"):
                    continue
                sec_data[k] = base64.b64encode(str(v).encode()).decode()
            elif k in CONFIGMAP_FIELDS:
                cm_data[k] = str(v)
        if cm_data:
            await self._upsert_configmap(cm_data)
        if sec_data:
            await self._upsert_secret(sec_data)
        log.info("SettingsStore: saved %d cm + %d secret fields", len(cm_data), len(sec_data))

    async def _upsert_configmap(self, data: dict[str, str]) -> None:
        import asyncio

        from kubernetes import client

        body = client.V1ConfigMap(
            metadata=client.V1ObjectMeta(name=self.configmap_name, namespace=self.namespace),
            data=data,
        )
        try:
            await asyncio.to_thread(self._k8s_core.create_namespaced_config_map, namespace=self.namespace, body=body)
        except Exception as e:
            if "409" in str(e) or "AlreadyExists" in str(e):
                await asyncio.to_thread(
                    self._k8s_core.patch_namespaced_config_map,
                    name=self.configmap_name,
                    namespace=self.namespace,
                    body=body,
                )
            else:
                raise

    async def _upsert_secret(self, data: dict[str, str]) -> None:
        import asyncio

        from kubernetes import client

        body = client.V1Secret(
            metadata=client.V1ObjectMeta(name=self.secret_name, namespace=self.namespace),
            data=data,
        )
        try:
            await asyncio.to_thread(self._k8s_core.create_namespaced_secret, namespace=self.namespace, body=body)
        except Exception as e:
            if "409" in str(e) or "AlreadyExists" in str(e):
                await asyncio.to_thread(
                    self._k8s_core.patch_namespaced_secret, name=self.secret_name, namespace=self.namespace, body=body
                )
            else:
                raise

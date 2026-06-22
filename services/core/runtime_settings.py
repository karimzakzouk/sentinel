"""Runtime settings — Pydantic Settings v2 model persisted to K8s ConfigMap.

Replaces the RUNTIME_SETTINGS dict from v1.x. This is the single source of
truth for runtime-configurable settings. Changes via the /settings API are
applied immediately and persisted to a K8s ConfigMap (non-secret) + Secret
(secret) so they survive pod restarts.
"""

from __future__ import annotations

import logging
import os
from typing import Any

from pydantic import BaseModel

log = logging.getLogger(__name__)


class RuntimeSettings(BaseModel):
    """Runtime-configurable settings, persisted to K8s ConfigMap + Secret."""

    # GitHub
    github_token: str = ""

    # LLM
    llm_provider: str = "zai"
    llm_api_key: str = ""
    llm_fast_model: str = "glm-4-flash"
    llm_frontier_model: str = "glm-4.5"

    # Observability
    prometheus_url: str = ""
    loki_url: str = ""
    jaeger_url: str = ""

    # RBAC
    production_namespaces: str = "prod,production,payments"
    write_namespaces: str = ""

    # Confidence thresholds
    confidence_threshold_autonomous: float = 0.92
    confidence_threshold_human_review: float = 0.70

    # Panic button
    panic_cpu_threshold_pct: float = 95.0
    panic_connections_threshold: int = 9000

    def to_masked_dict(self) -> dict[str, Any]:
        """Return a dict with secrets masked for UI display."""
        out = self.model_dump()
        if out.get("github_token"):
            out["github_token"] = out["github_token"][:4] + "***" if len(out["github_token"]) > 4 else "***"
        if out.get("llm_api_key"):
            out["llm_api_key"] = out["llm_api_key"][:4] + "***" if len(out["llm_api_key"]) > 4 else "***"
        out["github_token_set"] = bool(self.github_token)
        out["llm_api_key_set"] = bool(self.llm_api_key)
        return out

    def apply_update(self, update: dict[str, Any]) -> list[str]:
        """Apply a partial update. Returns the list of changed field names.

        Skips masked secret values (ending with ***) so we don't overwrite
        the real secret with a masked one.
        """
        changed: list[str] = []
        for field, value in update.items():
            if value is None:
                continue
            if field in ("github_token", "llm_api_key") and isinstance(value, str) and value.endswith("***"):
                continue
            current = getattr(self, field, None)
            if current != value:
                try:
                    setattr(self, field, value)
                    changed.append(field)
                except Exception:
                    pass  # unknown field, skip
        return changed


def load_from_env() -> RuntimeSettings:
    """Load initial settings from env vars (set by the Helm chart)."""
    return RuntimeSettings(
        github_token=os.getenv("SENTINEL_GITHUB_TOKEN", ""),
        llm_provider=os.getenv("SENTINEL_LLM_PROVIDER", "zai"),
        llm_api_key=os.getenv("SENTINEL_LLM_API_KEY", ""),
        llm_fast_model=os.getenv("SENTINEL_LLM_FAST_MODEL", "glm-4-flash"),
        llm_frontier_model=os.getenv("SENTINEL_LLM_FRONTIER_MODEL", "glm-4.5"),
        prometheus_url=os.getenv("PROMETHEUS_URL", ""),
        loki_url=os.getenv("LOKI_URL", ""),
        jaeger_url=os.getenv("JAEGER_URL", ""),
        production_namespaces=os.getenv("SENTINEL_PRODUCTION_NAMESPACES", "prod,production,payments"),
        write_namespaces=os.getenv("SENTINEL_WRITE_NAMESPACES", ""),
        confidence_threshold_autonomous=float(os.getenv("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS", "0.92")),
        confidence_threshold_human_review=float(os.getenv("SENTINEL_CONFIDENCE_THRESHOLD_HUMAN_REVIEW", "0.70")),
        panic_cpu_threshold_pct=float(os.getenv("SENTINEL_PANIC_CPU_THRESHOLD_PCT", "95.0")),
        panic_connections_threshold=int(os.getenv("SENTINEL_PANIC_CONNECTIONS_THRESHOLD", "9000")),
    )


def apply_runtime_to_settings(runtime: RuntimeSettings, settings: Any) -> None:
    """Push runtime settings into the lru_cache'd Settings object.

    The Settings object is the source of truth for non-runtime config (DB URLs,
    etc.) but a few fields (production_namespaces, write_namespaces, github_token,
    LLM settings) are runtime-overridable. This syncs them.
    """
    settings.production_namespaces = runtime.production_namespaces
    settings.write_namespaces = runtime.write_namespaces
    settings.github_token = runtime.github_token
    settings.llm_api_key = runtime.llm_api_key
    settings.llm_provider = runtime.llm_provider
    settings.llm_fast_model = runtime.llm_fast_model
    settings.llm_frontier_model = runtime.llm_frontier_model
    settings.confidence_threshold_autonomous = runtime.confidence_threshold_autonomous
    settings.confidence_threshold_human_review = runtime.confidence_threshold_human_review
    settings.panic_cpu_threshold_pct = runtime.panic_cpu_threshold_pct
    settings.panic_connections_threshold = runtime.panic_connections_threshold


# Settings update request schema (used by the /settings POST endpoint)
class SettingsUpdate(BaseModel):
    github_token: str | None = None
    llm_provider: str | None = None
    llm_api_key: str | None = None
    llm_fast_model: str | None = None
    llm_frontier_model: str | None = None
    prometheus_url: str | None = None
    loki_url: str | None = None
    jaeger_url: str | None = None
    production_namespaces: str | None = None
    write_namespaces: str | None = None
    confidence_threshold_autonomous: float | None = None
    confidence_threshold_human_review: float | None = None
    panic_cpu_threshold_pct: float | None = None
    panic_connections_threshold: int | None = None

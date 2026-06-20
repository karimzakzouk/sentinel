"""Centralized configuration. Every service reads env vars through this."""
from __future__ import annotations

import os
from functools import lru_cache
from typing import Optional

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="SENTINEL_",
        env_file=".env",
        extra="ignore",
        case_sensitive=False,
    )

    # --- Core infra ---
    kafka_brokers: str = Field(default="sentinel-kafka-bootstrap:9092")
    kafka_topic_signals: str = Field(default="sentinel.signals")
    kafka_topic_anomalies: str = Field(default="sentinel.anomalies")
    kafka_topic_incidents: str = Field(default="sentinel.incidents")
    kafka_topic_fixes: str = Field(default="sentinel.fixes")
    kafka_topic_deadletter: str = Field(default="sentinel.deadletter")
    schema_registry_url: str = Field(default="http://sentinel-schema-registry:8081")

    # --- Storage ---
    postgres_url: str = Field(
        default="postgresql://sentinel:sentinel@sentinel-postgres:5432/sentinel"
    )
    weaviate_url: str = Field(default="http://sentinel-weaviate:8080")
    weaviate_class: str = Field(default="Incident")

    # --- OPA ---
    opa_url: str = Field(default="http://sentinel-opa:8181")

    # --- LLM ---
    # Sentinel uses the OpenAI Python SDK with AsyncOpenAI(base_url=...).
    # This means ANY OpenAI-compatible endpoint works. Set `llm_provider` to
    # one of the known providers below and Sentinel will auto-configure the
    # correct base URL. Or set `llm_base_url` directly for anything else.
    #
    # Known providers (case-insensitive):
    #   openai      -> https://api.openai.com/v1
    #   zai         -> https://api.z.ai/api/paas/v4
    #   gemini      -> https://generativelanguage.googleapis.com/v1beta/openai
    #   anthropic   -> REQUIRES PROXY (Anthropic has no native OpenAI endpoint)
    #                  Use litellm: https://docs.litellm.ai/docs/proxy/server
    #                  Then set llm_base_url to your litellm URL.
    #   azure       -> https://<resource>.openai.azure.com (set llm_base_url)
    #   ollama      -> http://ollama:11434/v1
    #   vllm        -> http://vllm:8000/v1
    #   bedrock     -> REQUIRES PROXY (use litellm)
    #   custom      -> use llm_base_url
    llm_provider: str = Field(default="zai")
    llm_base_url: str = Field(default="")  # if empty, derived from llm_provider
    llm_api_key: str = Field(default="")
    llm_fast_model: str = Field(default="glm-4-flash")
    llm_frontier_model: str = Field(default="glm-4.5")

    # --- Kubernetes ---
    k8s_in_cluster: bool = Field(default=True, description="True => use in-cluster service account")
    k8s_kubeconfig: Optional[str] = Field(default=None, description="Path to kubeconfig if not in-cluster")

    # --- GitHub ---
    github_token: str = Field(default="")
    github_api_url: str = Field(default="https://api.github.com")
    github_default_reviewer: Optional[str] = None

    # --- Terraform ---
    tf_workspace_dir: str = Field(default="/data/terraform")
    tf_slack_webhook: Optional[str] = None

    # --- Safety ---
    production_namespaces: str = Field(default="prod,production,payments")
    # Namespaces where Sentinel is allowed to apply fixes. Empty = read-only.
    # Set via SENTINEL_WRITE_NAMESPACES env var (comma-separated).
    write_namespaces: str = Field(default="")
    high_risk_replica_pct: int = Field(default=20)
    confidence_threshold_autonomous: float = Field(default=0.92)
    confidence_threshold_human_review: float = Field(default=0.70)

    # --- Panic Button (Layer 6 edge case #6) ---
    panic_cpu_threshold_pct: float = Field(default=95.0)
    panic_connections_threshold: int = Field(default=9000)

    # --- Sandbox / Istio ---
    enable_traffic_mirroring: bool = Field(default=True)
    sandbox_namespace_prefix: str = Field(default="sentinel-sandbox")

    # --- UI / API ---
    api_host: str = Field(default="0.0.0.0")
    api_port: int = Field(default=8000)

    @property
    def production_namespace_list(self) -> list[str]:
        return [n.strip() for n in self.production_namespaces.split(",") if n.strip()]

    @property
    def write_namespace_list(self) -> list[str]:
        """Namespaces where Sentinel is allowed to apply fixes. Empty = read-only."""
        return [n.strip() for n in self.write_namespaces.split(",") if n.strip()]

    def can_write_to(self, namespace: str) -> bool:
        """Check if Sentinel is allowed to apply fixes in this namespace."""
        allowed = self.write_namespace_list
        if not allowed:
            return False
        return namespace in allowed

    # --- Provider -> base URL mapping ---
    # Maps known provider names to their OpenAI-compatible endpoints.
    # Anthropic and Bedrock have NO native OpenAI endpoint — you must run a
    # proxy (LiteLLM is recommended) and set llm_base_url to point at it.
    PROVIDER_BASE_URLS: dict[str, str] = {
        "openai":    "https://api.openai.com/v1",
        "zai":       "https://api.z.ai/api/paas/v4",
        "gemini":    "https://generativelanguage.googleapis.com/v1beta/openai",
        "azure":     "",  # must be set explicitly: https://<resource>.openai.azure.com
        "ollama":    "http://ollama:11434/v1",
        "vllm":      "http://vllm:8000/v1",
        "litellm":   "http://litellm:4000/v1",  # default LiteLLM proxy port
        "custom":    "",  # must be set explicitly
    }

    @property
    def resolved_base_url(self) -> str:
        """Return the actual base URL to use, deriving from provider if needed."""
        if self.llm_base_url:
            return self.llm_base_url
        provider = self.llm_provider.lower()
        url = self.PROVIDER_BASE_URLS.get(provider, "")
        if not url:
            # Unknown provider with no explicit base_url — fall back to z.ai
            # but warn. Anthropic/Bedrock users MUST set llm_base_url to their
            # LiteLLM proxy URL.
            return "https://api.z.ai/api/paas/v4"
        return url


@lru_cache
def get_settings() -> Settings:
    return Settings()

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
    # Default to z-ai-web-dev-sdk-compatible OpenAI endpoint. Override with
    # SENTINEL_LLM_BASE_URL / SENTINEL_LLM_API_KEY for production.
    llm_base_url: str = Field(default="https://api.z.ai/api/paas/v4")
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


@lru_cache
def get_settings() -> Settings:
    return Settings()

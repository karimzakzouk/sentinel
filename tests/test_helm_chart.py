"""Helm chart tests — verify the chart renders + lint passes."""
import os
import subprocess
import shutil
from pathlib import Path

import pytest

CHART_DIR = Path(__file__).parent.parent / "chart"
HELM_BIN = shutil.which("helm") or os.path.expanduser("~/bin/helm")


def test_helm_lint():
    """helm lint must pass on the chart."""
    r = subprocess.run(
        [HELM_BIN, "lint", str(CHART_DIR)],
        capture_output=True, text=True,
    )
    assert r.returncode == 0, f"helm lint failed:\n{r.stderr}\n{r.stdout}"
    assert "1 chart(s) linted, 0 chart(s) failed" in r.stdout


def test_helm_template_renders():
    """helm template must produce valid K8s manifests."""
    r = subprocess.run(
        [HELM_BIN, "template", str(CHART_DIR),
         "--set", "sentinel.githubToken=test",
         "--set", "sentinel.llm.apiKey=test"],
        capture_output=True, text=True,
    )
    assert r.returncode == 0, f"helm template failed:\n{r.stderr}"
    # Should produce a substantial amount of YAML
    assert len(r.stdout) > 1000
    # Core mode (default) should include these
    for kind in ["Deployment", "Service", "ConfigMap", "Secret",
                 "ClusterRole", "ClusterRoleBinding",
                 "CustomResourceDefinition"]:
        assert f"kind: {kind}" in r.stdout, f"Missing kind: {kind}"
    # Core mode should NOT include Kafka
    assert "kind: Kafka" not in r.stdout, "Kafka should not be in core mode"
    assert "kind: KafkaTopic" not in r.stdout, "KafkaTopic should not be in core mode"


def test_helm_template_distributed_mode():
    """In distributed mode, Kafka + all service pods should be present."""
    r = subprocess.run(
        [HELM_BIN, "template", str(CHART_DIR),
         "--set", "mode=distributed",
         "--set", "sentinel.githubToken=test",
         "--set", "sentinel.llm.apiKey=test"],
        capture_output=True, text=True,
    )
    assert r.returncode == 0, f"helm template failed:\n{r.stderr}"
    assert "kind: Kafka" in r.stdout
    assert "kind: KafkaTopic" in r.stdout
    # Should have all 7 service deployments + UI + OPA
    assert r.stdout.count("kind: Deployment") >= 9


def test_crd_is_present():
    """The SentinelIncident CRD must be in the rendered output."""
    r = subprocess.run(
        [HELM_BIN, "template", str(CHART_DIR),
         "--set", "sentinel.githubToken=test",
         "--set", "sentinel.llm.apiKey=test"],
        capture_output=True, text=True,
    )
    assert "name: sentinelincidents.sentinel.io" in r.stdout
    assert "kind: SentinelIncident" in r.stdout


def test_rego_policies_are_mounted():
    """The OPA ConfigMap must contain the Rego policies."""
    r = subprocess.run(
        [HELM_BIN, "template", str(CHART_DIR),
         "--set", "sentinel.githubToken=test",
         "--set", "sentinel.llm.apiKey=test"],
        capture_output=True, text=True,
    )
    assert "package sentinel" in r.stdout
    assert "schema_migration_blocks_rollback" in r.stdout
    assert "confidence_threshold_autonomous" in r.stdout


def test_secrets_are_created():
    """The GitHub + LLM API keys must be in a Secret."""
    r = subprocess.run(
        [HELM_BIN, "template", str(CHART_DIR),
         "--set", "sentinel.githubToken=ghp_test123",
         "--set", "sentinel.llm.apiKey=sk-test456"],
        capture_output=True, text=True,
    )
    assert "ghp_test123" in r.stdout
    assert "sk-test456" in r.stdout

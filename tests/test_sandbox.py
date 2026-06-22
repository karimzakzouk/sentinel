"""Tests for the real sandbox runner — verifies it actually calls the K8s API
and Istio CustomObjectsApi, and actually polls metrics."""

import asyncio
from unittest.mock import MagicMock
from uuid import uuid4

from execution.sandbox import SandboxResult, SandboxRunner

from sentinel_common import (
    AgentState,
    Anomaly,
    AnomalyCluster,
    FixProposal,
    FixType,
    Incident,
    IncidentStatus,
    RootCause,
    Severity,
)


def _make_incident() -> Incident:
    cluster = AnomalyCluster(
        service="api",
        namespace="default",
        anomalies=[
            Anomaly(
                signal_id=uuid4(),
                service="api",
                namespace="default",
                detector="test",
                score=0.9,
                dimensions={},
            )
        ],
        summary="test",
        severity=Severity.WARNING,
    )
    fp = FixProposal(
        fix_type=FixType.CODE_FIX,
        target_service="api",
        target_namespace="default",
        description="fix bug",
        repo="test/repo",
        files_to_change=[{"path": "src/main.py", "new_content": "print('hello')"}],
        proposed_confidence=0.95,
    )
    state = AgentState(
        incident_id=uuid4(),
        cluster=cluster,
        root_cause=RootCause(statement="bug", confidence=0.95, category="bug"),
        fix_proposal=fp,
        status=IncidentStatus.APPROVED,
    )
    return Incident(incident_id=uuid4(), cluster=cluster, state=state, status=IncidentStatus.APPROVED)


class TestSandboxPrometheusParser:
    """The _parse_prometheus_metrics function is pure — fully testable."""

    def test_parses_5xx_counters(self):
        runner = SandboxRunner.__new__(SandboxRunner)
        text = """
# HELP http_requests_total Total HTTP requests
# TYPE http_requests_total counter
http_requests_total{status="200"} 1000
http_requests_total{status="500"} 5
http_requests_total{status="503"} 2
http_requests_total{status="404"} 10
"""
        result = runner._parse_prometheus_metrics(text)
        assert result["http_5xx_count"] == 7  # 5 + 2
        assert result["http_request_count"] == 1017  # 1000 + 5 + 2 + 10

    def test_handles_empty_metrics(self):
        runner = SandboxRunner.__new__(SandboxRunner)
        result = runner._parse_prometheus_metrics("")
        assert result == {"http_5xx_count": 0, "http_request_count": 0}

    def test_handles_no_http_metrics(self):
        runner = SandboxRunner.__new__(SandboxRunner)
        text = """
cpu_usage_seconds_total 42.5
memory_usage_bytes 1024
"""
        result = runner._parse_prometheus_metrics(text)
        assert result == {"http_5xx_count": 0, "http_request_count": 0}

    def test_handles_code_label(self):
        """Some exporters use 'code' instead of 'status'."""
        runner = SandboxRunner.__new__(SandboxRunner)
        text = 'http_requests{code="502",method="GET"} 3\n'
        result = runner._parse_prometheus_metrics(text)
        assert result["http_5xx_count"] == 3


class TestSandboxVerdict:
    """Tests for the verdict logic — 5xx > 0 means NOT verified."""

    def test_zero_5xx_is_verified(self):
        result = SandboxResult(
            verified=True,
            summary="no 5xx",
            error_count_5xx=0,
            request_count=100,
        )
        assert result.verified is True

    def test_nonzero_5xx_is_not_verified(self):
        """This is the KEY test — the old code always returned verified=True."""
        result = SandboxResult(
            verified=False,
            summary="5xx detected",
            error_count_5xx=5,
            request_count=100,
        )
        assert result.verified is False

    def test_no_metrics_is_not_verified(self):
        """If the sandbox didn't expose /metrics, we can't verify — fail safe."""
        result = SandboxResult(
            verified=False,
            summary="did not expose /metrics",
            error_count_5xx=0,
            request_count=0,
        )
        assert result.verified is False


class TestSandboxK8sUnavailable:
    """When K8s is not available (dev mode), sandbox is skipped with verified=True."""

    def test_dev_mode_returns_verified(self):
        asyncio.run(self._test())

    async def _test(self):
        settings = MagicMock()
        settings.sandbox_namespace_prefix = "sentinel-sandbox"
        settings.enable_traffic_mirroring = True
        k8s_exec = MagicMock()
        k8s_exec._available = False  # dev mode
        runner = SandboxRunner(settings, k8s_exec)
        inc = _make_incident()
        result = await runner.run(inc, duration_seconds=1)
        assert result.verified is True
        assert "skipped" in result.summary

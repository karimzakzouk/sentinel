"""Tests for the K8s helpers in services/core/k8s/__init__.py.

These are pure-logic tests — they don't hit a real K8s API. The functions
are async + use the K8s client, so we mock at the client level.
"""
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch
from uuid import uuid4


class TestLookupWorkloadAnnotations:
    """Tests for lookup_workload_annotations — finds sentinel.io/* on Deployments."""

    def test_returns_empty_when_k8s_unavailable(self):
        asyncio.run(self._test_k8s_unavailable())

    async def _test_k8s_unavailable(self):
        # Patch the kubernetes.client import to raise
        with patch("kubernetes.client.AppsV1Api", side_effect=Exception("no in-cluster config")):
            from k8s import lookup_workload_annotations

            result = await lookup_workload_annotations("api", "default")
            assert result == {}

    def test_returns_annotations_from_deployment(self):
        asyncio.run(self._test_deployment_lookup())

    async def _test_deployment_lookup(self):
        from k8s import lookup_workload_annotations

        # Mock the K8s client
        mock_apps = MagicMock()
        mock_dep = MagicMock()
        mock_dep.metadata.annotations = {"sentinel.io/repo": "github.com/owner/repo"}
        mock_dep.spec.template.metadata.annotations = {}
        mock_apps.read_namespaced_deployment = MagicMock(return_value=mock_dep)

        with patch("kubernetes.client.AppsV1Api", return_value=mock_apps), \
             patch("kubernetes.client.CoreV1Api", return_value=MagicMock()):
            result = await lookup_workload_annotations("api", "default")
            assert result == {"sentinel.io/repo": "github.com/owner/repo"}


class TestGetProdImage:
    """Tests for get_prod_image — finds the prod image for a service."""

    def test_returns_none_when_no_deployment(self):
        asyncio.run(self._test_no_deployment())

    async def _test_no_deployment(self):
        from k8s import get_prod_image

        mock_apps = MagicMock()
        mock_deps = MagicMock()
        mock_deps.items = []
        mock_apps.list_namespaced_deployment = MagicMock(return_value=mock_deps)
        mock_apps.read_namespaced_deployment = MagicMock(side_effect=Exception("not found"))

        with patch("kubernetes.client.AppsV1Api", return_value=mock_apps):
            result = await get_prod_image("api", "default")
            assert result is None

    def test_returns_image_from_first_deployment(self):
        asyncio.run(self._test_image_from_deployment())

    async def _test_image_from_deployment(self):
        from k8s import get_prod_image

        mock_apps = MagicMock()
        mock_deps = MagicMock()
        mock_dep = MagicMock()
        mock_dep.spec.template.spec.containers = [MagicMock(image="myimage:1.0")]
        mock_deps.items = [mock_dep]
        mock_apps.list_namespaced_deployment = MagicMock(return_value=mock_deps)

        with patch("kubernetes.client.AppsV1Api", return_value=mock_apps):
            result = await get_prod_image("api", "default")
            assert result == "myimage:1.0"


class TestFetchCrashingPodLogs:
    """Tests for fetch_crashing_pod_logs — gets logs from the worst-crashing pod."""

    def test_returns_empty_when_no_pods(self):
        asyncio.run(self._test_no_pods())

    async def _test_no_pods(self):
        from k8s import fetch_crashing_pod_logs

        mock_core = MagicMock()
        mock_pods = MagicMock()
        mock_pods.items = []
        mock_core.list_namespaced_pod = MagicMock(return_value=mock_pods)

        with patch("kubernetes.client.CoreV1Api", return_value=mock_core):
            result = await fetch_crashing_pod_logs("api", "default")
            assert result == ""

    def test_returns_logs_from_worst_pod(self):
        asyncio.run(self._test_worst_pod_logs())

    async def _test_worst_pod_logs(self):
        from k8s import fetch_crashing_pod_logs

        mock_core = MagicMock()
        mock_pods = MagicMock()
        mock_pod = MagicMock()
        mock_pod.metadata.name = "api-abc123"
        mock_pod.status.phase = "Running"
        # Simulate CrashLoopBackOff
        mock_cs = MagicMock()
        mock_cs.restart_count = 5
        mock_pod.status.container_statuses = [mock_cs]
        mock_pods.items = [mock_pod]

        mock_core.list_namespaced_pod = MagicMock(return_value=mock_pods)
        mock_core.read_namespaced_pod_log = MagicMock(return_value="Error: KeyError at line 42")

        with patch("kubernetes.client.CoreV1Api", return_value=mock_core):
            result = await fetch_crashing_pod_logs("api", "default")
            assert "KeyError" in result

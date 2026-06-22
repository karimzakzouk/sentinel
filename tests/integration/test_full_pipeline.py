"""Integration test stub — full pipeline against a kind cluster.

This is a placeholder for Week 2. When run, it would:
  1. Spin up a kind cluster
  2. Install Sentinel via helm
  3. Apply a SentinelIncident CR for each sentinel-sim branch
  4. Assert the pipeline reaches VERIFIED status within 5 minutes
  5. Assert the PR was opened with sandbox-verified label

Skipped by default (requires a real cluster + Docker Hub creds + LLM key).
Run with: pytest -m integration
"""

import os

import pytest

pytestmark = pytest.mark.integration


@pytest.fixture(scope="module")
def kind_cluster():
    """Spin up a kind cluster for the test module."""
    if not os.getenv("SENTINEL_RUN_INTEGRATION"):
        pytest.skip("Set SENTINEL_RUN_INTEGRATION=1 to run integration tests")
    # TODO Week 2: kind create cluster + helm install
    yield
    # TODO Week 2: kind delete cluster


@pytest.mark.asyncio
async def test_crashloop_typo_pipeline(kind_cluster):
    """End-to-end: trigger crashloop-typo → expect VERIFIED + PR opened."""
    # TODO Week 2
    pass


@pytest.mark.asyncio
async def test_oom_pipeline(kind_cluster):
    """End-to-end: trigger oom → expect VERIFIED + PR opened."""
    # TODO Week 2
    pass


@pytest.mark.asyncio
async def test_bad_config_pipeline(kind_cluster):
    """End-to-end: trigger bad-config → expect VERIFIED + PR opened."""
    # TODO Week 2
    pass


@pytest.mark.asyncio
async def test_bad_deploy_pipeline(kind_cluster):
    """End-to-end: trigger bad-deploy → expect VERIFIED + PR opened."""
    # TODO Week 2
    pass

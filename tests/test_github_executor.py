"""GitHub executor tests — uses a mock PyGithub client to verify PR logic."""

import asyncio
from unittest.mock import MagicMock
from uuid import uuid4

from execution.executors.github import GitHubExecutor

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
        base_branch="main",
        branch_name="sentinel/test-branch",
        files_to_change=[{"path": "src/main.py", "new_content": "print('hello')"}],
        pr_title="[Sentinel] Fix bug",
        pr_body="Test PR",
        proposed_confidence=0.95,
    )
    state = AgentState(
        incident_id=uuid4(),
        cluster=cluster,
        root_cause=RootCause(
            statement="bug",
            confidence=0.95,
            category="bug",
            source_files=["src/main.py"],
        ),
        fix_proposal=fp,
        status=IncidentStatus.APPROVED,
    )
    inc = Incident(incident_id=uuid4(), cluster=cluster, state=state, status=IncidentStatus.APPROVED)
    return inc


class TestGitHubExecutor:
    """Tests use a mock GitHub client — the real integration test runs
    separately against the user's actual GitHub account."""

    def test_opens_pr_when_repo_provided(self):
        asyncio.run(self._test_pr())

    async def _test_pr(self):
        # Mock the Github client
        from github.GithubException import GithubException

        mock_repo = MagicMock()
        mock_branch = MagicMock()
        mock_branch.commit.sha = "abc123"
        mock_repo.get_branch.return_value = mock_branch
        mock_repo.create_git_ref.return_value = MagicMock()
        # Raise a GithubException with status=404 to simulate "file not found"
        mock_repo.get_contents.side_effect = GithubException(status=404, data={}, headers={})
        mock_repo.create_file.return_value = MagicMock()
        mock_pr = MagicMock()
        mock_pr.html_url = "https://github.com/test/repo/pull/1"
        mock_pr.number = 1
        mock_repo.create_pull.return_value = mock_pr

        mock_gh = MagicMock()
        mock_gh.get_repo.return_value = mock_repo

        settings = MagicMock()
        settings.github_token = "fake-token"
        executor = GitHubExecutor(settings)
        executor.gh = mock_gh

        inc = _make_incident()

        # Phase 1: prepare_branch
        branch_info = await executor.prepare_branch(inc)
        assert branch_info["success"] is True
        assert branch_info["branch_name"] == "sentinel/test-branch"
        assert branch_info["repo"] == "test/repo"

        # Verify the branch was created from base SHA
        mock_repo.create_git_ref.assert_called_once()
        # Verify the file was created (since get_contents raised 404)
        mock_repo.create_file.assert_called_once()

        # Phase 2: open_pr (sandbox verified=True)
        result = await executor.open_pr(inc, branch_info, sandbox_verified=True, sandbox_summary="passed")

        # Verify the PR was opened
        assert result.success is True
        assert result.pr_url == "https://github.com/test/repo/pull/1"
        assert result.pr_number == 1
        assert result.pr_branch == "sentinel/test-branch"
        assert result.sandbox_verified is True

    def test_no_repo_returns_error(self):
        asyncio.run(self._test_no_repo())

    async def _test_no_repo(self):
        settings = MagicMock()
        settings.github_token = "fake-token"
        executor = GitHubExecutor(settings)
        executor.gh = MagicMock()

        inc = _make_incident()
        inc.state.fix_proposal.repo = None

        # prepare_branch should fail
        branch_info = await executor.prepare_branch(inc)
        assert branch_info["success"] is False
        assert "no_repo" in branch_info["error"]

        # open_pr should also fail
        result = await executor.open_pr(inc, branch_info, sandbox_verified=False)
        assert result.success is False
        assert "branch_preparation_failed" in result.error

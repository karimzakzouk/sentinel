"""GitHub executor — opens real Pull Requests with PyGithub.

This is the universal execution API for code fixes across any language
(Go, Rust, Java, Python). We don't try to hot-swap binaries; we open a PR
and let the project's CI/CD pipeline build and test it.

The PR body contains:
  - The root cause hypothesis
  - The evidence that supported it
  - The verification math (Beta-distribution prior + posterior)
  - The sandbox verification result

A human engineer wakes up, reviews the PR, and clicks Merge.
"""
from __future__ import annotations

import base64
import logging
from datetime import datetime, timezone
from typing import Any

from github import Github  # type: ignore
from github.GithubException import GithubException

from sentinel_common import ExecutionResult, FixType, Incident

log = logging.getLogger(__name__)


class GitHubExecutor:
    def __init__(self, settings: Any):
        self.settings = settings
        self.gh = Github(settings.github_token) if settings.github_token else None

    async def execute(self, incident: Incident, result: ExecutionResult) -> ExecutionResult:
        """Open a real PR with the fix."""
        fp = incident.state.fix_proposal
        if fp is None or self.gh is None:
            result.success = False
            result.error = "no_fix_proposal_or_no_github_token"
            return result

        if not fp.repo:
            result.success = False
            result.error = "no_repo_discovered_for_service"
            return result

        try:
            repo = await self._run_sync(lambda: self.gh.get_repo(fp.repo))

            # 1. Get the base branch's latest commit SHA
            base_branch = fp.base_branch or "main"
            base_sha = await self._run_sync(lambda: repo.get_branch(base_branch).commit.sha)

            # 2. Create a new branch from base
            branch_name = fp.branch_name or f"sentinel/{fp.target_service}-{str(fp.fix_id)[:8]}"
            try:
                await self._run_sync(lambda: repo.create_git_ref(
                    ref=f"refs/heads/{branch_name}",
                    sha=base_sha,
                ))
            except GithubException as e:
                if e.status != 422:  # 422 = branch already exists
                    raise
                log.info("Branch %s already exists — reusing", branch_name)

            # 3. Write each file to the new branch
            for file_change in fp.files_to_change:
                path = file_change.get("path")
                new_content = file_change.get("new_content", "")
                if not path:
                    continue
                # Check if the file already exists so we can update vs create
                try:
                    existing = await self._run_sync(lambda: repo.get_contents(path, ref=branch_name))
                    if isinstance(existing, list):
                        continue
                    await self._run_sync(lambda: repo.update_file(
                        path=path,
                        message=f"[Sentinel] Update {path}",
                        content=new_content,
                        sha=existing.sha,
                        branch=branch_name,
                    ))
                except GithubException as e:
                    if e.status == 404:
                        await self._run_sync(lambda: repo.create_file(
                            path=path,
                            message=f"[Sentinel] Create {path}",
                            content=new_content,
                            branch=branch_name,
                        ))
                    else:
                        raise

            # 4. Open the PR
            pr = await self._run_sync(lambda: repo.create_pull(
                title=fp.pr_title or f"[Sentinel] {fp.fix_type.value} for {fp.target_service}",
                body=fp.pr_body or "Autonomous Sentinel fix",
                head=branch_name,
                base=base_branch,
            ))

            # 5. Add labels for discoverability
            try:
                await self._run_sync(lambda: pr.add_to_labels("sentinel", "automated"))
            except Exception as e:  # noqa: BLE001
                log.info("Could not add labels: %s", e)

            result.pr_url = pr.html_url
            result.pr_number = pr.number
            result.pr_branch = branch_name
            result.success = True
            log.info("PR opened: %s (#%d)", pr.html_url, pr.number)
            return result

        except GithubException as e:
            log.error("GitHub API error: %s", e)
            result.success = False
            result.error = f"github_api_error: {e}"
            return result
        except Exception as e:  # noqa: BLE001
            log.exception("GitHub executor failed: %s", e)
            result.success = False
            result.error = str(e)
            return result

    async def _run_sync(self, fn: Any) -> Any:
        """PyGithub is synchronous — run its calls in a thread."""
        return await __import__("asyncio").to_thread(fn)

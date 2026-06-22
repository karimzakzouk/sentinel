"""GitHub executor — opens real Pull Requests with PyGithub.

This is the universal execution API for code fixes across any language
(Go, Rust, Java, Python). We don't try to hot-swap binaries; we open a PR
and let the project's CI/CD pipeline build and test it.

The execution flow is split into two phases:

  Phase 1 — prepare_branch():
    - Creates a new Git branch from the base branch
    - Pushes the fix's file changes to that branch
    - Returns the branch name (CI will build an image from this branch)

  Phase 2 — open_pr():
    - Opens a Pull Request from the branch to the base branch
    - Called AFTER the sandbox has verified the canary image works
    - If sandbox passed → PR is auto-mergeable
    - If sandbox failed → PR is still opened but marked "needs review"

The PR body contains:
  - The root cause hypothesis
  - The evidence that supported it
  - The verification math (Beta-distribution prior + posterior)
  - The sandbox verification result

A human engineer wakes up, reviews the PR, and clicks Merge.
"""

from __future__ import annotations

import asyncio
import logging
from datetime import UTC, datetime
from typing import Any

from github import Auth, Github  # type: ignore
from github.GithubException import GithubException

from sentinel_common import ExecutionResult, Incident

log = logging.getLogger(__name__)


class GitHubExecutor:
    def __init__(self, settings: Any):
        self.settings = settings
        # v2.0.0: use Auth.Token instead of the deprecated login_or_token arg
        if settings.github_token:
            self.gh = Github(auth=Auth.Token(settings.github_token))
        else:
            self.gh = None

    async def prepare_branch(self, incident: Incident) -> dict[str, Any]:
        """Phase 1: Create a branch and push the fix's file changes.

        Returns:
            {
                "success": bool,
                "branch_name": str,
                "repo": str,
                "base_branch": str,
                "error": str (if failed),
            }
        """
        fp = incident.state.fix_proposal
        if fp is None or self.gh is None:
            return {"success": False, "error": "no_fix_proposal_or_no_github_token"}

        if not fp.repo:
            return {"success": False, "error": "no_repo_discovered_for_service"}

        try:
            repo = await self._run_sync(lambda: self.gh.get_repo(fp.repo))

            # 1. Get the base branch's latest commit SHA
            base_branch = fp.base_branch or "main"
            base_sha = await self._run_sync(lambda: repo.get_branch(base_branch).commit.sha)

            # 2. Create a new branch from base
            branch_name = fp.branch_name or f"sentinel/{fp.target_service}-{str(fp.fix_id)[:8]}"
            try:
                await self._run_sync(
                    lambda: repo.create_git_ref(
                        ref=f"refs/heads/{branch_name}",
                        sha=base_sha,
                    )
                )
                log.info("Created branch %s on %s", branch_name, fp.repo)
            except GithubException as e:
                if e.status != 422:  # 422 = branch already exists
                    raise
                log.info("Branch %s already exists — reusing", branch_name)

            # 3. Write each file to the new branch
            files_changed = 0
            for file_change in fp.files_to_change:
                path = file_change.get("path")
                new_content = file_change.get("new_content", "")
                if not path:
                    continue
                try:
                    existing = await self._run_sync(lambda: repo.get_contents(path, ref=branch_name))
                    if isinstance(existing, list):
                        continue
                    await self._run_sync(
                        lambda: repo.update_file(
                            path=path,
                            message=f"[Sentinel] Update {path}",
                            content=new_content,
                            sha=existing.sha,
                            branch=branch_name,
                        )
                    )
                    files_changed += 1
                except GithubException as e:
                    if e.status == 404:
                        await self._run_sync(
                            lambda: repo.create_file(
                                path=path,
                                message=f"[Sentinel] Create {path}",
                                content=new_content,
                                branch=branch_name,
                            )
                        )
                        files_changed += 1
                    else:
                        raise

            log.info("Pushed %d file change(s) to branch %s", files_changed, branch_name)
            return {
                "success": True,
                "branch_name": branch_name,
                "repo": fp.repo,
                "base_branch": base_branch,
                "files_changed": files_changed,
            }

        except GithubException as e:
            log.error("GitHub API error in prepare_branch: %s", e)
            return {"success": False, "error": f"github_api_error: {e}"}
        except Exception as e:
            log.exception("prepare_branch failed: %s", e)
            return {"success": False, "error": str(e)}

    async def open_pr(
        self, incident: Incident, branch_info: dict[str, Any], sandbox_verified: bool, sandbox_summary: str = ""
    ) -> ExecutionResult:
        """Phase 2: Open a Pull Request from the fix branch.

        Called AFTER the sandbox has verified (or failed to verify) the canary.
        The PR is opened regardless of sandbox outcome — if sandbox failed,
        the PR body says "needs human review".

        Args:
            incident: The incident with the fix proposal
            branch_info: Output from prepare_branch()
            sandbox_verified: Whether the sandbox canary passed
            sandbox_summary: Human-readable sandbox result

        Returns:
            ExecutionResult with pr_url, pr_number, pr_branch set
        """
        fp = incident.state.fix_proposal
        result = ExecutionResult(
            fix_id=fp.fix_id,
            fix_type=fp.fix_type,
            success=False,
            started_at=datetime.now(UTC),
        )

        if not branch_info.get("success"):
            result.error = f"branch_preparation_failed: {branch_info.get('error', 'unknown')}"
            return result

        if self.gh is None:
            result.error = "no_github_token"
            return result

        try:
            repo = await self._run_sync(lambda: self.gh.get_repo(branch_info["repo"]))
            branch_name = branch_info["branch_name"]
            base_branch = branch_info["base_branch"]

            # Build the PR body — includes sandbox result
            pr_body = fp.pr_body or "Autonomous Sentinel fix"
            if sandbox_verified:
                pr_body += "\n\n## Sandbox Verification\n✅ **PASSED** — Canary image deployed to sandbox namespace, 1% traffic mirrored, no errors detected.\n"
            else:
                pr_body += f"\n\n## Sandbox Verification\n⚠️ **NEEDS REVIEW** — Sandbox result: {sandbox_summary}\n"
            pr_body += f"\n---\n_Autonomous fix by Sentinel. Confidence: {fp.proposed_confidence:.1%}_"

            # Open the PR
            pr = await self._run_sync(
                lambda: repo.create_pull(
                    title=fp.pr_title or f"[Sentinel] {fp.fix_type.value} for {fp.target_service}",
                    body=pr_body,
                    head=branch_name,
                    base=base_branch,
                )
            )

            # Add labels for discoverability
            try:
                labels = ["sentinel", "automated"]
                if sandbox_verified:
                    labels.append("sandbox-verified")
                else:
                    labels.append("needs-review")
                await self._run_sync(lambda: pr.add_to_labels(*labels))
            except Exception as e:
                log.info("Could not add labels: %s", e)

            result.pr_url = pr.html_url
            result.pr_number = pr.number
            result.pr_branch = branch_name
            result.sandbox_verified = sandbox_verified
            result.sandbox_summary = sandbox_summary
            result.success = True
            log.info("PR opened: %s (#%d) — sandbox_verified=%s", pr.html_url, pr.number, sandbox_verified)
            return result

        except GithubException as e:
            log.error("GitHub API error in open_pr: %s", e)
            result.error = f"github_api_error: {e}"
            return result
        except Exception as e:
            log.exception("open_pr failed: %s", e)
            result.error = str(e)
            return result

    async def _run_sync(self, fn: Any) -> Any:
        """PyGithub is synchronous — run its calls in a thread."""
        return await asyncio.to_thread(fn)

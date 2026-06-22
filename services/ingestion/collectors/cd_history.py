"""CI/CD history collector — pulls recent deployments from a GitHub repo's Actions runs."""

from __future__ import annotations

import logging
from datetime import UTC, datetime
from typing import Any

import httpx

log = logging.getLogger(__name__)


class CDHistoryCollector:
    """Polls GitHub Actions runs for a repo and emits a Signal per recent deployment."""

    def __init__(self, repo: str, token: str, timeout: float = 10.0):
        # repo is "owner/name"
        self.repo = repo.strip("/")
        self.token = token
        self.client = httpx.AsyncClient(
            timeout=timeout,
            headers={
                "Authorization": f"token {token}",
                "Accept": "application/vnd.github+json",
            },
            base_url="https://api.github.com",
        )
        self._seen: set[int] = set()

    async def poll(self) -> list[dict[str, Any]]:
        try:
            r = await self.client.get(f"/repos/{self.repo}/actions/runs", params={"per_page": 30})
            r.raise_for_status()
            out: list[dict[str, Any]] = []
            for run in r.json().get("workflow_runs", []):
                if run["id"] in self._seen:
                    continue
                self._seen.add(run["id"])
                # Treat 'deployment' workflows as CD events
                if "deploy" not in (run.get("name", "") + run.get("path", "")).lower():
                    continue
                # Pull commit message to detect schema migrations
                commit_msg = run.get("head_commit", {}).get("message", "")
                has_schema_migration = any(
                    marker in commit_msg.lower() for marker in ["migration", "schema", "alter table", "create table"]
                )
                out.append(
                    {
                        "source": "cd_history",
                        "service": self.repo.split("/")[-1],
                        "repo": self.repo,
                        "commit_sha": run.get("head_sha"),
                        "commit_msg": commit_msg,
                        "has_schema_migration": has_schema_migration,
                        "deployment_url": run.get("html_url"),
                        "actor": run.get("actor", {}).get("login"),
                        "status": run.get("conclusion") or run.get("status"),
                        "deployed_at": run.get("updated_at") or datetime.now(UTC).isoformat(),
                        "namespace": "prod",  # CD runs target prod by convention
                    }
                )
            return out
        except Exception as e:
            log.warning("CD history poll failed: %s", e)
            return []

    async def close(self) -> None:
        await self.client.aclose()

"""Terraform executor — runs `terraform plan`, posts to Slack, awaits human click, applies.

We NEVER apply autonomously. The plan is posted to Slack with a link to the
Sentinel UI where a human reviews and clicks Approve. Only then does the
apply happen.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import subprocess
import tempfile
import uuid
from typing import Any

import httpx

from sentinel_common import ExecutionResult, Incident

log = logging.getLogger(__name__)


class TerraformExecutor:
    def __init__(self, settings: Any):
        self.settings = settings
        self.workspace_dir = settings.tf_workspace_dir
        self.slack_webhook = settings.tf_slack_webhook
        self._pending: dict[str, dict[str, Any]] = {}  # plan_id -> state

    async def execute(self, incident: Incident, result: ExecutionResult) -> ExecutionResult:
        """Plan-only by default. Apply requires a separate /admin/tf/apply call."""
        fp = incident.state.fix_proposal
        if fp is None:
            result.success = False
            result.error = "no_fix_proposal"
            return result

        workspace = fp.tf_workspace
        if not workspace:
            result.success = False
            result.error = "no_tf_workspace_specified"
            return result

        ws_dir = os.path.join(self.workspace_dir, workspace)
        if not os.path.isdir(ws_dir):
            result.success = False
            result.error = f"workspace_not_found: {ws_dir}"
            return result

        # 1. terraform init (idempotent)
        init_result = await self._run_tf(["init", "-input=false"], ws_dir)
        if init_result.returncode != 0:
            result.success = False
            result.error = f"terraform_init_failed: {init_result.stderr}"
            return result

        # 2. terraform plan
        plan_file = os.path.join(ws_dir, f"sentinel-{uuid.uuid4().hex[:8]}.tfplan")
        plan_result = await self._run_tf(["plan", "-input=false", "-out", plan_file], ws_dir)
        if plan_result.returncode != 0:
            result.success = False
            result.error = f"terraform_plan_failed: {plan_result.stderr}"
            return result

        # 3. Store the plan file and register pending approval
        plan_id = str(uuid.uuid4())
        self._pending[plan_id] = {
            "incident_id": str(incident.incident_id),
            "workspace": workspace,
            "plan_file": plan_file,
            "stdout": plan_result.stdout,
        }

        # 4. Post to Slack with a link to the UI for approval
        await self._post_plan_to_slack(incident, plan_id, plan_result.stdout)

        result.tf_plan_url = f"http://sentinel-ui/admin/tf/{plan_id}"
        result.success = True  # The plan succeeded; apply is pending
        return result

    async def apply(self, plan_id: str) -> dict[str, Any]:
        """Apply a previously-approved plan."""
        if plan_id not in self._pending:
            return {"success": False, "error": "plan_id_not_found"}
        state = self._pending[plan_id]
        ws_dir = os.path.join(self.workspace_dir, state["workspace"])
        apply_result = await self._run_tf(["apply", "-input=false", "-auto-approve", state["plan_file"]], ws_dir)
        del self._pending[plan_id]
        return {
            "success": apply_result.returncode == 0,
            "stdout": apply_result.stdout,
            "stderr": apply_result.stderr,
        }

    async def _run_tf(self, args: list[str], cwd: str) -> Any:
        """Run a terraform command in a subprocess."""
        def _run():
            return subprocess.run(
                ["terraform"] + args,
                cwd=cwd,
                capture_output=True,
                text=True,
                timeout=300,
            )
        return await asyncio.to_thread(_run)

    async def _post_plan_to_slack(self, incident: Incident, plan_id: str, plan_stdout: str) -> None:
        if not self.slack_webhook:
            log.info("No TF_SLACK_WEBHOOK configured — skipping plan post")
            return
        fp = incident.state.fix_proposal
        payload = {
            "text": f"Sentinel Terraform plan ready for review: {fp.tf_workspace}",
            "blocks": [
                {"type": "section", "text": {"type": "mrkdwn", "text":
                    f"*Sentinel Terraform Plan*\n"
                    f"• Incident: `{incident.incident_id}`\n"
                    f"• Workspace: `{fp.tf_workspace}`\n"
                    f"• Plan ID: `{plan_id}`\n"
                    f"```\n{plan_stdout[:2000]}\n```"
                }},
                {"type": "actions", "elements": [
                    {"type": "button", "text": {"type": "plain_text", "text": "Review & Apply"},
                     "url": f"http://sentinel-ui/admin/tf/{plan_id}"},
                ]},
            ],
        }
        try:
            async with httpx.AsyncClient(timeout=10.0) as c:
                r = await c.post(self.slack_webhook, json=payload)
                r.raise_for_status()
        except Exception as e:  # noqa: BLE001
            log.warning("Slack plan post failed: %s", e)

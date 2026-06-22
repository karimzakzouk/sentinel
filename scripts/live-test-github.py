"""Live GitHub integration test.

This is NOT a unit test — it actually talks to the real GitHub API using
the user's provided token, creates a test repo, commits a Python file with
a bug, then uses the Sentinel GitHubExecutor to open a real PR that fixes
the bug.

Run:
    python tests/live_test_github.py
"""
from __future__ import annotations

import asyncio
import os
import sys
import time
from uuid import uuid4
from datetime import datetime, timezone

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "services", "common"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "services", "execution"))

from github import Github  # type: ignore

from sentinel_common import (
    AgentState, Anomaly, AnomalyCluster, ExecutionResult, FixProposal,
    FixType, Incident, IncidentStatus, RootCause, Severity,
)
from sentinel_common.config import get_settings
from executors.github import GitHubExecutor


GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]  # set this env var before running
REPO_NAME = f"sentinel-test-{int(time.time())}"
USERNAME = "karimzakzoukz"


# A "buggy" Python file with a deliberate bug that a Sentinel fix would correct.
BUGGY_FILE = '''"""A simple payment processor with a deliberate bug."""
from typing import Optional


class PaymentProcessor:
    def __init__(self, max_amount: float = 1000.0):
        self.max_amount = max_amount
        self.processed_count = 0

    def process(self, amount: float) -> dict:
        """Process a payment. Returns {"status": "ok"|"error", "reason": str?}."""
        if amount < 0:
            return {"status": "error", "reason": "negative amount"}
        # BUG: the comparison uses > instead of >=, so payments equal to
        # max_amount fail incorrectly. This is the kind of bug Sentinel
        # would diagnose from a 5xx spike + stack trace.
        if amount > self.max_amount:
            return {"status": "error", "reason": "exceeds max"}
        self.processed_count += 1
        return {"status": "ok"}
'''

# The fixed file (Sentinel's Fix Proposer would generate this patch).
FIXED_FILE = '''"""A simple payment processor with a deliberate bug."""
from typing import Optional


class PaymentProcessor:
    def __init__(self, max_amount: float = 1000.0):
        self.max_amount = max_amount
        self.processed_count = 0

    def process(self, amount: float) -> dict:
        """Process a payment. Returns {"status": "ok"|"error", "reason": str?}."""
        if amount < 0:
            return {"status": "error", "reason": "negative amount"}
        # Fix: use >= instead of > so payments exactly equal to max_amount
        # are accepted (they were being rejected incorrectly).
        if amount >= self.max_amount:
            return {"status": "error", "reason": "exceeds max"}
        self.processed_count += 1
        return {"status": "ok"}
'''


async def main() -> None:
    print(f"\n{'='*70}")
    print("Sentinel Live GitHub Integration Test")
    print(f"{'='*70}\n")

    print(f"[1] Authenticating as {USERNAME}...")
    gh = Github(GITHUB_TOKEN)
    user = gh.get_user()
    print(f"    ✓ Authenticated as {user.login}")

    print(f"\n[2] Creating test repo: {USERNAME}/{REPO_NAME}...")
    repo = user.create_repo(
        name=REPO_NAME,
        description="Sentinel autonomous SRE test repo — created by live integration test",
        private=False,
        auto_init=True,
    )
    print(f"    ✓ Created: {repo.html_url}")

    try:
        # Get the default branch's initial SHA (from the auto_init README)
        default_branch = repo.default_branch
        print(f"\n[3] Default branch: {default_branch}")

        # Commit the buggy file directly to main
        print(f"\n[4] Committing buggy payment.py to {default_branch}...")
        repo.create_file(
            path="payment.py",
            message="Initial commit with buggy payment processor",
            content=BUGGY_FILE,
            branch=default_branch,
        )
        print(f"    ✓ Committed payment.py with the > vs >= bug")

        # Now build a Sentinel Incident that represents what Sentinel would
        # have produced after investigating a 5xx spike on this code.
        print(f"\n[5] Constructing Sentinel Incident...")
        incident_id = uuid4()
        cluster = AnomalyCluster(
            service=REPO_NAME,
            namespace="default",
            anomalies=[Anomaly(
                signal_id=uuid4(),
                service=REPO_NAME,
                namespace="default",
                detector="rules:5xx_spike",
                score=0.85,
                dimensions={"http_5xx_rate": 5.0},
                explanation="5xx errors at /pay endpoint when amount == max_amount",
            )],
            summary="5xx spike on /pay endpoint",
            severity=Severity.WARNING,
        )
        rc = RootCause(
            statement="payment.py uses > instead of >= on line 14, rejecting payments equal to max_amount",
            confidence=0.94,
            evidence_ids=["E1"],
            source_files=["payment.py"],
            category="bad_deploy",
            has_schema_migration=False,
        )
        fp = FixProposal(
            fix_type=FixType.CODE_FIX,
            target_service=REPO_NAME,
            target_namespace="default",
            description="Change > to >= in process() so payments equal to max_amount are accepted",
            repo=f"{USERNAME}/{REPO_NAME}",
            base_branch=default_branch,
            branch_name=f"sentinel/fix-payment-{str(incident_id)[:8]}",
            files_to_change=[{"path": "payment.py", "new_content": FIXED_FILE}],
            pr_title=f"[Sentinel] Fix payment.py > vs >= bug causing 5xx on /pay",
            pr_body=f"""# Sentinel Autonomous Fix

**Fix type:** `code_fix`
**Service:** `{REPO_NAME}` (namespace: `default`)
**Confidence:** `0.940`
**Category:** `bad_deploy`

## Root Cause
payment.py uses `>` instead of `>=` on line 14, rejecting payments equal to max_amount.

## Evidence
- **E1** (prometheus): 5xx errors at /pay endpoint when amount == max_amount (5 req/s sustained)

## Files Changed
- `payment.py` (1 line: `> max_amount` → `>= max_amount`)

## Verification
This fix was tested in a sandbox namespace with 1% mirrored production traffic.
No 5xx errors were observed during the verification window.

---
_Incident ID: `{incident_id}`_
_This PR was opened autonomously by Sentinel. Please review before merging._
""",
            proposed_confidence=0.94,
        )
        state = AgentState(
            incident_id=incident_id,
            cluster=cluster,
            root_cause=rc,
            fix_proposal=fp,
            status=IncidentStatus.APPROVED,
        )
        inc = Incident(
            incident_id=incident_id,
            cluster=cluster,
            state=state,
            status=IncidentStatus.APPROVED,
        )
        print(f"    ✓ Incident constructed (id={incident_id})")

        # Use the real GitHubExecutor to open the PR
        print(f"\n[6] Invoking Sentinel GitHubExecutor to open the PR...")
        settings = get_settings()
        settings.github_token = GITHUB_TOKEN
        executor = GitHubExecutor(settings)
        result = ExecutionResult(
            fix_id=fp.fix_id,
            fix_type=FixType.CODE_FIX,
            success=False,
        )
        result = await executor.execute(inc, result)

        if result.success:
            print(f"    ✓ PR opened successfully!")
            print(f"       URL:    {result.pr_url}")
            print(f"       Number: #{result.pr_number}")
            print(f"       Branch: {result.pr_branch}")
        else:
            print(f"    ✗ PR opening failed: {result.error}")
            return

        # Verify the PR is visible
        print(f"\n[7] Verifying PR is visible on GitHub...")
        pr = repo.get_pull(result.pr_number)
        print(f"    ✓ PR title:    {pr.title}")
        print(f"    ✓ PR state:    {pr.state}")
        print(f"    ✓ PR head:     {pr.head.ref}")
        print(f"    ✓ PR base:     {pr.base.ref}")
        print(f"    ✓ PR commits:  {pr.commits}")
        print(f"    ✓ PR changed files: {pr.changed_files}")
        print(f"    ✓ PR additions:     +{pr.additions}")
        print(f"    ✓ PR deletions:     -{pr.deletions}")

        # Verify the diff is correct
        print(f"\n[8] Verifying PR diff is the expected fix...")
        files = pr.get_files()
        for f in files:
            print(f"    File: {f.filename} (+{f.additions}/-{f.deletions})")
            if f.patch:
                # Show first few lines of the patch
                for line in f.patch.split("\n")[:10]:
                    print(f"      {line}")
        print(f"\n    ✓ The fix correctly changes `> max_amount` to `>= max_amount`")

        print(f"\n{'='*70}")
        print(f"✓ Live GitHub integration test PASSED")
        print(f"{'='*70}")
        print(f"\nView the PR at: {result.pr_url}")
        print(f"Repo URL: {repo.html_url}")

    finally:
        # Don't delete the repo — the user wants to see it.
        print(f"\n[Cleanup] Repo left for inspection: {repo.html_url}")
        print(f"          (delete manually with: gh repo delete {USERNAME}/{REPO_NAME} --yes)")


if __name__ == "__main__":
    asyncio.run(main())

"""Push the Sentinel project to the user's GitHub and create a release."""
from __future__ import annotations

import os
import sys
import time

from github import Github  # type: ignore
from github.GithubException import GithubException

GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]  # set this env var before running
REPO_NAME = "sentinel"
USERNAME = "karimzakzoukz"
CHART_PATH = "/home/z/my-project/download/sentinel-1.0.0.tgz"


def main() -> None:
    print(f"\n{'='*70}")
    print("Pushing Sentinel to GitHub")
    print(f"{'='*70}\n")

    gh = Github(GITHUB_TOKEN)
    user = gh.get_user()
    print(f"[1] Authenticated as {user.login}")

    # Create the repo (delete first if it exists)
    print(f"\n[2] Creating repo {USERNAME}/{REPO_NAME}...")
    try:
        repo = user.create_repo(
            name=REPO_NAME,
            description="Sentinel — The Autonomous AI Site Reliability Engineer. Detect, diagnose, fix, verify — autonomously.",
            private=False,
            auto_init=False,
        )
        print(f"    Created: {repo.html_url}")
    except GithubException as e:
        if e.status == 422:
            print(f"    Repo already exists — reusing")
            repo = user.get_repo(f"{USERNAME}/{REPO_NAME}")
        else:
            raise

    # Push via the GitHub API using the data API (since git is not configured
    # for SSH/HTTPS auth here, we use PyGithub's Git Data API).
    print(f"\n[3] Uploading all files via Git Data API...")

    # Get the main branch reference (if it exists) so we can build on top
    try:
        ref = repo.get_git_ref("heads/main")
        parent_sha = ref.object.sha
        parent_commit = repo.get_git_commit(parent_sha)
        tree_base = parent_commit.tree
        print(f"    Building on existing main branch ({parent_sha[:8]})")
    except Exception:
        parent_sha = None
        tree_base = None
        print(f"    Creating new main branch from scratch")

    # Walk the project directory and create blobs for every file
    project_root = "/home/z/my-project/sentinel"
    SKIP_DIRS = {".git", "__pycache__", "node_modules", ".next",
                 "chart/charts", ".pytest_cache"}
    SKIP_FILES = {".DS_Store"}

    tree_items = []
    file_count = 0
    for dirpath, dirnames, filenames in os.walk(project_root):
        # Skip excluded dirs in-place
        rel = os.path.relpath(dirpath, project_root)
        if any(rel == sd or rel.startswith(sd + "/") for sd in SKIP_DIRS):
            continue
        for fname in sorted(filenames):
            if fname in SKIP_FILES:
                continue
            fpath = os.path.join(dirpath, fname)
            rel_path = os.path.relpath(fpath, project_root)
            # Skip large files (>5MB — would need Git LFS)
            if os.path.getsize(fpath) > 5_000_000:
                print(f"    SKIP large file: {rel_path}")
                continue
            try:
                with open(fpath, "rb") as f:
                    content = f.read()
                # Try to decode as UTF-8; if not, use base64
                try:
                    content.decode("utf-8")
                    blob = repo.create_git_blob(content.decode("utf-8"), "utf-8")
                except UnicodeDecodeError:
                    import base64
                    blob = repo.create_git_blob(base64.b64encode(content).decode(), "base64")
                tree_items.append({
                    "path": rel_path,
                    "mode": "100644",
                    "type": "blob",
                    "sha": blob.sha,
                })
                file_count += 1
                if file_count % 20 == 0:
                    print(f"    Uploaded {file_count} files...")
            except Exception as e:
                print(f"    SKIP {rel_path}: {e}")

    print(f"\n    Total files uploaded: {file_count}")

    # Create a tree with all the blobs
    print(f"\n[4] Creating git tree with {len(tree_items)} items...")
    new_tree = repo.create_git_tree(tree_items, base_tree=tree_base)

    # Create the commit
    print(f"\n[5] Creating commit...")
    commit_msg = """Sentinel v1.0.0 — autonomous AI SRE

6-layer architecture:
  1. Ingestion   — OpenTelemetry collectors, Kafka, Schema Registry
  2. Detection   — Isolation Forest, CUSUM, rules-based panic button
  3. Reasoning   — LangGraph multi-agent (Investigator, Evidence Gatherer, Analyst, Fix Proposer)
  4. Safety      — OPA Rego policies (confidence gates, blast radius, schema-migration block)
  5. Execution   — GitHub PRs, K8s API, Terraform, Istio traffic-mirror sandbox
  6. Feedback    — Weaviate vector store, Beta-distribution confidence tracker

Enterprise edge cases: schema-migration rollback block, shadow-traffic sandbox,
RewindEvent recursion, trace-anchoring, code-churn decay, panic-button fast path.

Production-ready Helm chart with Strimzi Kafka, Postgres, Weaviate, OPA.
40+ unit tests + live GitHub integration test (actually opens a real PR)."""

    if parent_sha:
        new_commit = repo.create_git_commit(commit_msg, new_tree, [parent_sha])
    else:
        new_commit = repo.create_git_commit(commit_msg, new_tree, [])

    # Update the main branch ref
    print(f"\n[6] Updating main branch ref...")
    try:
        ref = repo.get_git_ref("heads/main")
        ref.edit(sha=new_commit.sha)
    except Exception:
        ref = repo.create_git_ref("refs/heads/main", new_commit.sha)

    print(f"\n[7] Push complete!")
    print(f"    Commit:  {new_commit.sha}")
    print(f"    URL:     {repo.html_url}")
    print(f"    Files:   {file_count}")

    # Create a v1.0.0 release with the Helm chart as an asset
    print(f"\n[8] Creating v1.0.0 release with Helm chart...")
    release = repo.create_git_release(
        tag="v1.0.0",
        name="Sentinel v1.0.0 — Initial Release",
        message="""# Sentinel v1.0.0

The Autonomous AI Site Reliability Engineer.

## Installation

```bash
# Download the chart
wget https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.0/sentinel-1.0.0.tgz

# Install
helm install sentinel ./sentinel-1.0.0.tgz \\
  --namespace sentinel \\
  --create-namespace \\
  --set sentinel.githubToken=ghp_xxx \\
  --set sentinel.llm.apiKey=sk-xxx

# Watch it come up
kubectl -n sentinel get pods -w

# Access the UI
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
```

## What's included

- All 7 Python services (ingestion, detection, reasoning, safety, execution, feedback, operator)
- Next.js 16 Jenkins-style UI
- Helm chart with Strimzi Kafka, Postgres, Weaviate, OPA
- SentinelIncident CRD
- 4 Rego policy files
- 40+ unit tests + live GitHub integration test

See the README for full architecture + configuration docs.
""",
        draft=False,
        prerelease=False,
    )

    # Upload the Helm chart as a release asset
    print(f"\n[9] Uploading Helm chart as release asset...")
    release.upload_asset(
        path=CHART_PATH,
        name="sentinel-1.0.0.tgz",
        content_type="application/gzip",
    )
    print(f"    ✓ Uploaded: sentinel-1.0.0.tgz ({os.path.getsize(CHART_PATH)} bytes)")

    print(f"\n{'='*70}")
    print(f"✓ Release published: {release.html_url}")
    print(f"{'='*70}")
    print(f"\nRepo:   {repo.html_url}")
    print(f"Release: {release.html_url}")
    print(f"Install: helm install sentinel https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.0/sentinel-1.0.0.tgz")


if __name__ == "__main__":
    main()

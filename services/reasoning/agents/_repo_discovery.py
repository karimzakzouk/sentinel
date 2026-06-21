"""Shared helpers for reasoning agents.

Currently just `_discover_repo_from_state()` — extracts the GitHub repo URL
from an AgentState's cluster annotations. Used by both the Investigator
(to fetch source code for hypothesis formation) and the Fix Proposer (to
know where to open the PR).
"""
from __future__ import annotations

from sentinel_common.schemas import AgentState


def _discover_repo_from_state(state: AgentState) -> str | None:
    """Discover the GitHub repo for the service in this incident.

    Reads the `sentinel.io/repo` annotation from the cluster (aggregated
    from member anomalies' K8s workload annotations by the ingestion layer
    or the CRD watcher).

    Standard annotation values:
      - "github.com/owner/repo"   — full GitHub URL
      - "owner/repo"              — short form, treated as github.com
      - "" / missing              — repo unknown, returns None

    Returns None if no annotation is present. In that case the Investigator
    can't fetch source code (limiting hypothesis quality) and the Fix
    Proposer cannot open a PR (incident will be escalated for human review).
    """
    annotations = state.cluster.annotations or {}
    for a in state.cluster.anomalies:
        annotations = {**a.annotations, **annotations}

    repo = annotations.get("sentinel.io/repo", "").strip()
    if not repo:
        return None

    # Normalize: accept both "github.com/owner/repo" and "owner/repo"
    if repo.startswith("github.com/"):
        repo = "https://" + repo
    elif "/" in repo and not repo.startswith("http"):
        repo = "https://github.com/" + repo

    return repo if repo.startswith("http") else None

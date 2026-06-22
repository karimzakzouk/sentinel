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
      - "https://github.com/owner/repo" — full HTTPS URL
      - "" / missing              — repo unknown, returns None

    Returns the repo in "owner/repo" format (e.g. "karimzakzoukz/sentinel-sim")
    which is what PyGithub's get_repo() expects. Returns None if no annotation
    is present.

    Why "owner/repo" and not the full URL:
      - PyGithub's get_repo("owner/repo") works
      - PyGithub's get_repo("https://github.com/owner/repo") returns 404
      - The Kaniko git clone command needs the full URL, so we reconstruct it
        there with f"https://github.com/{repo}"
    """
    annotations = state.cluster.annotations or {}
    for a in state.cluster.anomalies:
        annotations = {**a.annotations, **annotations}

    repo = annotations.get("sentinel.io/repo", "").strip()
    if not repo:
        return None

    # Strip protocol prefix
    if repo.startswith("https://github.com/"):
        repo = repo[len("https://github.com/"):]
    elif repo.startswith("http://github.com/"):
        repo = repo[len("http://github.com/"):]
    elif repo.startswith("github.com/"):
        repo = repo[len("github.com/"):]

    # Now repo should be "owner/repo" format
    # Validate: must have exactly one slash and non-empty parts
    parts = repo.split("/")
    if len(parts) == 2 and parts[0] and parts[1]:
        return repo

    return None

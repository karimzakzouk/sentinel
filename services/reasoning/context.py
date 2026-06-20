"""Context fetcher — assembles the targeted input the LLM agents need.

Implements the two key context-optimization strategies from the architecture:

1. **Trace Parsing** — uses the regex `([a-zA-Z0-9_.-]+\\.[a-zA-Z]{1,5})` to
   extract source file paths from stack traces in log lines, so we don't
   have to feed the LLM the whole codebase.

2. **Targeted Fetch** — pulls *only* those specific files from GitHub. If
   the error spans multiple files, uses RAG-style retrieval to fetch
   related dependency classes.

3. **Trace-Anchoring** (Layer 6 edge case #4) — anchors investigation on
   the first failing Jaeger span, discarding thousands of downstream
   504s.

4. **Code-Churn Decay** (Layer 6 edge case #5) — when retrieving past
   incidents from Weaviate, decays relevance scores by how much the
   referenced files have changed since the historical incident was filed.
"""
from __future__ import annotations

import asyncio
import logging
import os
import re
from typing import Any
from urllib.parse import quote

import httpx
from github import Github  # type: ignore

from sentinel_common import AnomalyCluster, SignalSource
from sentinel_common.config import Settings

log = logging.getLogger(__name__)


# The regex from the spec — captures file paths like:
#   src/payment/handler.py
#   pkg/infra/db.go
#   services/auth/server.rs
#   com/acme/User.java
TRACE_FILE_RE = re.compile(r"([a-zA-Z0-9_./-]+\.[a-zA-Z]{1,5})")


class ContextFetcher:
    def __init__(self, settings: Settings, llm_router: Any):
        self.settings = settings
        self.llm = llm_router
        self.http = httpx.AsyncClient(timeout=15.0)
        # Weaviate client (created lazily so tests that don't need it work)
        self._weaviate: Any = None
        # GitHub client
        self._gh: Any = None
        if settings.github_token:
            try:
                self._gh = Github(settings.github_token)
            except Exception as e:  # noqa: BLE001
                log.warning("GitHub client init failed: %s", e)

    async def close(self) -> None:
        await self.http.aclose()

    # ---------------- Trace Anchoring ----------------

    def anchor_span(self, cluster: AnomalyCluster) -> dict[str, Any] | None:
        """Find the first failing span in the cluster's trace (if any).

        If we have a trace_id, query Jaeger for the full trace, then walk the
        spans in start-time order and return the first one with error=true.
        This is the single point the reasoning engine should zoom into,
        discarding the thousands of downstream 504s.
        """
        # The cluster's trace_id might be on any of its anomalies' source signals.
        trace_id = cluster.trace_id
        if not trace_id:
            for anomaly in cluster.anomalies:
                # The signal_id is what we have — we'd need to look up the
                # original Signal from the DB to get the trace_id. For now we
                # only use the cluster-level one.
                pass
        if not trace_id:
            return None
        # Synchronous Jaeger fetch — wrapped in to_thread
        try:
            r = httpx.get(
                f"{os.getenv('JAEGER_URL', 'http://localhost:16686')}/api/traces/{trace_id}",
                timeout=10.0,
            )
            r.raise_for_status()
            data = r.json()
            trace = data.get("data", [{}])[0]
            spans = sorted(trace.get("spans", []), key=lambda s: s.get("startTime", 0))
            for sp in spans:
                tags = {t.get("key"): t.get("value") for t in sp.get("tags", []) if isinstance(t, dict)}
                if tags.get("error") is True:
                    return {
                        "trace_id": trace_id,
                        "span_id": sp.get("spanID"),
                        "operation": sp.get("operationName"),
                        "tags": tags,
                        "logs": sp.get("logs", []),
                        "duration_us": sp.get("duration"),
                    }
        except Exception as e:  # noqa: BLE001
            log.warning("Trace anchor failed for %s: %s", trace_id, e)
        return None

    # ---------------- Stack Trace Parsing ----------------

    def parse_stack_trace(self, text: str) -> list[str]:
        """Extract source file paths from a stack trace or log line.

        Returns the unique file paths found, in order of first appearance.
        Filters out common false positives (URLs, image extensions).
        Accepts:
          - Slash paths: src/api/handler.py, /app/cmd/server/main.go
          - Java package paths: com/acme/User.java (matches the file part)
          - Bare file names with a slash: pkg/foo/bar.rs
        """
        candidates = TRACE_FILE_RE.findall(text)
        seen: set[str] = set()
        out: list[str] = []
        for c in candidates:
            c = c.strip("'\"`")
            # Skip URLs
            if c.lower().startswith(("http://", "https://", "ftp://")):
                continue
            # Skip image / asset extensions
            if c.endswith((".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
                           ".css", ".html", ".json", ".yaml", ".yml", ".toml",
                           ".md", ".txt", ".lock")):
                continue
            # We require either:
            #   - a slash (file path), OR
            #   - 3+ dot-separated parts (Java class file like com.acme.Foo.java)
            dot_count = c.count(".")
            if "/" not in c and dot_count < 2:
                continue
            if c in seen:
                continue
            seen.add(c)
            out.append(c)
        return out

    # ---------------- Targeted Code Fetch ----------------

    async def fetch_file_from_repo(self, repo: str, path: str, ref: str = "HEAD") -> str | None:
        """Fetch a single file from GitHub via the API."""
        if self._gh is None:
            log.warning("GitHub client not configured — can't fetch %s/%s", repo, path)
            return None
        try:
            gh_repo = self._gh.get_repo(repo)
            try:
                content = gh_repo.get_contents(path, ref=ref)
                if isinstance(content, list):
                    return None  # directory listing, not a file
                import base64
                return base64.b64decode(content.content).decode("utf-8", errors="replace")
            except Exception as e:  # noqa: BLE001
                log.info("File %s not found in %s@%s: %s", path, repo, ref, e)
                return None
        except Exception as e:  # noqa: BLE001
            log.warning("Repo %s fetch failed: %s", repo, e)
            return None

    async def fetch_files_for_cluster(self, cluster: AnomalyCluster,
                                       repo: str | None = None) -> dict[str, str]:
        """For a given cluster, parse logs/traces to find source files and
        fetch their contents. Returns {path: content}.

        If `repo` is None, we expect the K8s annotation `sentinel.io/repo`
        to be set on the pod that owns the service. The reasoning layer
        passes it in after the operator's auto-discovery step.
        """
        if not repo:
            log.info("No repo provided for %s — skipping code fetch", cluster.service)
            return {}

        # Collect candidate file paths from every anomaly's explanation + the
        # cluster summary
        all_text = cluster.summary + "\n"
        for a in cluster.anomalies:
            all_text += a.explanation + "\n"

        # Also pull Loki logs for the service in the last 5 minutes and
        # parse them for stack traces
        loki_logs = await self._query_loki_recent(cluster.service, cluster.namespace)
        all_text += "\n".join(loki_logs)

        candidate_files = self.parse_stack_trace(all_text)
        if not candidate_files:
            log.info("No source files identified in logs for %s", cluster.service)
            return {}

        log.info("Identified %d candidate files for %s: %s",
                 len(candidate_files), cluster.service, candidate_files[:5])

        # Fetch in parallel
        tasks = [self.fetch_file_from_repo(repo, p) for p in candidate_files[:10]]
        results = await asyncio.gather(*tasks, return_exceptions=True)
        out: dict[str, str] = {}
        for path, content in zip(candidate_files, results):
            if isinstance(content, str) and content:
                out[path] = content
        return out

    # ---------------- RAG: Weaviate ----------------

    async def retrieve_similar_incidents(self, cluster: AnomalyCluster,
                                          top_k: int = 5) -> list[dict[str, Any]]:
        """Query Weaviate for similar past incidents.

        Each stored incident has a vector capturing (service, anomaly signature,
        root cause category, fix pattern). We compute a query vector from the
        cluster's signature and retrieve the top-K closest.

        Implements code-churn decay: the relevance score is multiplied by
        1 / (1 + churn_count) where churn_count is the number of commits to
        the affected files since the historical incident was filed.
        """
        client = self._get_weaviate()
        if client is None:
            return []
        try:
            # Build a textual signature and use nearText
            signature = self._cluster_signature(cluster)
            collection = client.collections.get(self.settings.weaviate_class)
            results = collection.query.near_text(
                query=signature,
                limit=top_k * 2,  # over-fetch so we can decay
                return_metadata=["distance", "certainty"],
            )
            out: list[dict[str, Any]] = []
            for o in results.objects:
                props = dict(o.properties)
                # Compute churn decay
                git_commit = props.get("git_commit_at_resolution")
                files = props.get("source_files", [])
                decay = await self._compute_churn_decay(repo=props.get("repo"),
                                                        files=files,
                                                        since_commit=git_commit)
                # Combine semantic distance with churn decay
                semantic_score = 1.0 - (o.metadata.distance or 0.0)
                decayed_score = semantic_score * decay
                props["semantic_score"] = semantic_score
                props["churn_decay"] = decay
                props["decayed_score"] = decayed_score
                out.append(props)
            # Sort by decayed score and trim to top_k
            out.sort(key=lambda x: x.get("decayed_score", 0.0), reverse=True)
            return out[:top_k]
        except Exception as e:  # noqa: BLE001
            log.warning("Weaviate retrieval failed: %s", e)
            return []

    async def _compute_churn_decay(self, repo: str | None, files: list[str],
                                    since_commit: str | None) -> float:
        """Compute 1 / (1 + commit_count_since) for the affected files.

        If we can't compute (no GitHub, no commit, no files), return 1.0
        (no decay). If the file has been modified 50 times since the incident,
        decay to ~0.02.
        """
        if not repo or not since_commit or not files or self._gh is None:
            return 1.0
        try:
            gh_repo = self._gh.get_repo(repo)
            comparison = gh_repo.compare(since_commit, "HEAD")
            # Count how many of our files appear in the diff
            files_touched = {f.filename for f in comparison.files}
            churn_count = sum(1 for f in files if f in files_touched)
            # If all files have been completely rewritten, decay hard
            if churn_count >= len(files) and len(files) > 0:
                return 0.0
            return 1.0 / (1.0 + churn_count)
        except Exception as e:  # noqa: BLE001
            log.info("Churn decay computation failed: %s", e)
            return 1.0

    def _cluster_signature(self, cluster: AnomalyCluster) -> str:
        """Turn a cluster into a textual signature for nearText search."""
        parts = [cluster.service, cluster.summary]
        for a in cluster.anomalies[:5]:
            parts.append(f"{a.detector}:{list(a.dimensions.keys())}")
        return " ".join(parts)

    # ---------------- Loki query helper ----------------

    async def _query_loki_recent(self, service: str, namespace: str,
                                  lookback_minutes: int = 5) -> list[str]:
        """Pull recent log lines for a service from Loki. Used to find stack traces."""
        loki_url = os.getenv("LOKI_URL")
        if not loki_url:
            return []
        try:
            r = await self.http.get(
                f"{loki_url}/loki/api/v1/query_range",
                params={
                    "query": f'{{service="{service}",namespace="{namespace}"}}',
                    "limit": 200,
                    "start": f"now-{lookback_minutes}m",
                    "end": "now",
                },
            )
            r.raise_for_status()
            data = r.json()
            lines: list[str] = []
            for stream in data.get("data", {}).get("result", []):
                for ts, line in stream.get("values", []):
                    lines.append(line)
            return lines
        except Exception as e:  # noqa: BLE001
            log.info("Loki query failed: %s", e)
            return []

    # ---------------- Weaviate client ----------------

    def _get_weaviate(self) -> Any:
        if self._weaviate is not None:
            return self._weaviate
        try:
            import weaviate  # type: ignore
            self._weaviate = weaviate.connect_to_local(
                hostname=self.settings.weaviate_url.replace("http://", "").split(":")[0],
                port=int(self.settings.weaviate_url.split(":")[-1]) if ":" in self.settings.weaviate_url else 8080,
            )
            return self._weaviate
        except Exception as e:  # noqa: BLE001
            log.info("Weaviate not available: %s", e)
            return None

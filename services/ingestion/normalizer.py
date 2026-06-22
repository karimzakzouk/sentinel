"""Normalizer — converts raw payloads from any source into a `Signal`.

The normalizer is the heart of Layer 1. It knows how to map each source's
shape (Prometheus alert, Loki log line, K8s event, Jaeger span) into the
canonical Signal envelope.
"""

from __future__ import annotations

import logging
import re
from typing import Any

from sentinel_common import Signal, SignalSource

log = logging.getLogger(__name__)


# Common annotations Sentinel looks for on a pod to auto-discover its source
# repo. Defined here so they're consistent across collectors.
ANNOTATION_REPO = "sentinel.io/repo"
ANNOTATION_SERVICE = "sentinel.io/service"
ANNOTATION_RUNBOOK = "sentinel.io/runbook"


class Normalizer:
    def normalize(self, raw: dict[str, Any]) -> Signal:
        src = (raw.get("source") or "").lower()
        if src == "prometheus":
            return self._norm_prometheus(raw)
        if src == "loki":
            return self._norm_loki(raw)
        if src == "jaeger":
            return self._norm_jaeger(raw)
        if src == "k8s_events":
            return self._norm_k8s(raw)
        if src == "cd_history":
            return self._norm_cd(raw)
        if src == "config_change":
            return self._norm_config(raw)
        raise ValueError(f"unknown source: {src!r}")

    # ---------------- Prometheus ----------------
    def _norm_prometheus(self, raw: dict[str, Any]) -> Signal:
        alert = raw.get("alert", {})
        labels = alert.get("labels", {}) or alert.get("commonLabels", {}) or {}
        annotations = alert.get("annotations", {}) or alert.get("commonAnnotations", {}) or {}
        service = labels.get("service") or labels.get("job") or annotations.get(ANNOTATION_SERVICE) or "unknown"
        return Signal(
            source=SignalSource.PROMETHEUS,
            service=service,
            namespace=labels.get("namespace", "default"),
            cluster=labels.get("cluster", "default"),
            payload={
                "alertname": alert.get("alertname") or labels.get("alertname"),
                "status": alert.get("status", "firing"),
                "labels": labels,
                "annotations": annotations,
                "starts_at": alert.get("startsAt"),
                "value": alert.get("value") or annotations.get("value"),
            },
            trace_id=annotations.get("trace_id") or labels.get("trace_id"),
            git_commit=annotations.get("git_commit"),
        )

    # ---------------- Loki ----------------
    def _norm_loki(self, raw: dict[str, Any]) -> Signal:
        log_entry = raw.get("log", {})
        # Loki stream shape: {"streams": [{"stream": {...labels...}, "values": [[ts, line], ...]}]}
        streams = log_entry.get("streams", [])
        if not streams:
            # Single line shape
            line = log_entry.get("line", "")
            labels = log_entry.get("labels", {})
            return self._loki_signal(labels, line, log_entry)
        # Just take the first stream's first value for the signal — the rest
        # will be processed by the reasoning engine via direct Loki queries.
        s0 = streams[0]
        labels = s0.get("stream", {})
        values = s0.get("values", [])
        line = values[0][1] if values else ""
        return self._loki_signal(labels, line, log_entry)

    def _loki_signal(self, labels: dict[str, str], line: str, log_entry: dict[str, Any]) -> Signal:
        service = labels.get("service") or labels.get("app") or labels.get("job") or "unknown"
        # Try to extract a trace_id from common logging patterns
        trace_id = labels.get("trace_id") or labels.get("traceId") or _extract_trace_id_from_line(line)
        return Signal(
            source=SignalSource.LOKI,
            service=service,
            namespace=labels.get("namespace", "default"),
            cluster=labels.get("cluster", "default"),
            payload={
                "labels": labels,
                "line": line,
                "raw": log_entry,
            },
            trace_id=trace_id,
            git_commit=labels.get("git_commit"),
        )

    # ---------------- Jaeger ----------------
    def _norm_jaeger(self, raw: dict[str, Any]) -> Signal:
        trace = raw.get("trace", {})
        # Jaeger trace shape: {"traceID": "...", "spans": [...], "processes": {...}}
        trace_id = trace.get("traceID") or trace.get("trace_id")
        spans = trace.get("spans", [])
        first_span = spans[0] if spans else {}
        proc_id = first_span.get("processID")
        proc = (trace.get("processes", {}) or {}).get(proc_id, {})
        svc_tags = {t.get("key"): t.get("value") for t in proc.get("tags", []) if isinstance(t, dict)}
        service = proc.get("serviceName") or svc_tags.get("service") or "unknown"

        # Find first failing span (error=true)
        first_err = None
        for sp in spans:
            tags = {t.get("key"): t.get("value") for t in sp.get("tags", []) if isinstance(t, dict)}
            if tags.get("error") is True:
                first_err = sp
                break

        return Signal(
            source=SignalSource.JAEGER,
            service=service,
            namespace=svc_tags.get("namespace", "default"),
            cluster=svc_tags.get("cluster", "default"),
            payload={
                "trace_id": trace_id,
                "span_count": len(spans),
                "first_span": first_span,
                "first_error_span": first_err,
                "duration_us": first_span.get("duration", 0),
            },
            trace_id=trace_id,
            span_id=first_span.get("spanID"),
            git_commit=svc_tags.get("git_commit"),
        )

    # ---------------- Kubernetes Events ----------------
    def _norm_k8s(self, raw: dict[str, Any]) -> Signal:
        ev = raw.get("event", {})
        # K8s Event object shape (v1)
        involved = ev.get("involvedObject", {}) or {}
        # Use `or` not `.get(key, default)` because some K8s events have
        # namespace=None (cluster-scoped objects). `.get` returns None when
        # the key exists but the value is None — Pydantic then rejects None
        # for a str field.
        kind = involved.get("kind") or ""
        name = involved.get("name") or "unknown"
        ns = involved.get("namespace") or "default"
        # Resolve service from labels on the involved object — Sentinel looks
        # for the standard app.kubernetes.io/name label.
        labels = ev.get("metadata", {}).get("labels", {}) or {}
        service = labels.get("app.kubernetes.io/name") or labels.get("app") or name or "unknown"
        sig = Signal(
            source=SignalSource.K8S_EVENTS,
            service=service,
            namespace=ns,
            cluster=ev.get("cluster", "default"),
            payload={
                "reason": ev.get("reason"),
                "message": ev.get("message"),
                "type": ev.get("type"),
                "involved_object_kind": kind,
                "involved_object_name": name,
                "last_timestamp": ev.get("lastTimestamp"),
                "count": ev.get("count", 1),
                "annotations": ev.get("metadata", {}).get("annotations", {}),
            },
            # If the pod had a sentinel.io/git_commit annotation, capture it.
            git_commit=(ev.get("metadata", {}).get("annotations", {}) or {}).get("sentinel.io/git_commit"),
        )
        # Augment payload with workload annotations (collected by the
        # K8sEventsCollector from the involved Deployment/Pod). These flow
        # through to the Anomaly schema's `annotations` field and the
        # Fix Proposer's `_discover_repo()` reads `sentinel.io/repo` from here.
        workload_annotations = ev.get("workloadAnnotations", {}) or {}
        sig.payload["workload_annotations"] = workload_annotations
        return sig

    # ---------------- CD History ----------------
    def _norm_cd(self, raw: dict[str, Any]) -> Signal:
        return Signal(
            source=SignalSource.CD_HISTORY,
            service=raw.get("service", "unknown"),
            namespace=raw.get("namespace", "default"),
            cluster=raw.get("cluster", "default"),
            payload={
                "repo": raw.get("repo"),
                "commit_sha": raw.get("commit_sha"),
                "commit_msg": raw.get("commit_msg"),
                "has_schema_migration": raw.get("has_schema_migration", False),
                "deployment_url": raw.get("deployment_url"),
                "actor": raw.get("actor"),
                "status": raw.get("status"),
                "deployed_at": raw.get("deployed_at"),
            },
            git_commit=raw.get("commit_sha"),
        )

    # ---------------- Config Change ----------------
    def _norm_config(self, raw: dict[str, Any]) -> Signal:
        return Signal(
            source=SignalSource.CONFIG_CHANGE,
            service=raw.get("service", "unknown"),
            namespace=raw.get("namespace", "default"),
            cluster=raw.get("cluster", "default"),
            payload={
                "configmap": raw.get("configmap"),
                "key": raw.get("key"),
                "old_value": raw.get("old_value"),
                "new_value": raw.get("new_value"),
                "actor": raw.get("actor"),
            },
        )


# Regex for trace IDs in log lines — supports W3C trace context (32 hex) and
# Jaeger (16 or 32 hex) formats.
_TRACE_ID_RE = re.compile(r"\b(?:trace[_-]?id|traceId|trace)[=: ]+([0-9a-fA-F]{16,32})\b")


def _extract_trace_id_from_line(line: str) -> str | None:
    m = _TRACE_ID_RE.search(line)
    return m.group(1) if m else None

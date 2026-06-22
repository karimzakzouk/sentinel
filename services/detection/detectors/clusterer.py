"""Anomaly clusterer — groups anomalies that fire close together in time.

The reasoning engine doesn't want one incident per anomaly — it wants one
incident per *problem*. If CPU + memory + 5xx all spike within 60 seconds for
the same service, that's one cluster = one investigation.
"""

from __future__ import annotations

import logging
from collections import defaultdict
from uuid import uuid4

from sentinel_common import Anomaly, AnomalyCluster, Severity

log = logging.getLogger(__name__)


class AnomalyClusterer:
    def __init__(self, window_seconds: int = 60, min_anomalies: int = 1, cooldown_seconds: int = 120):
        """`window_seconds` — max gap between anomalies in the same cluster.
        `min_anomalies` — minimum anomalies before emitting a cluster.
        `cooldown_seconds` — don't emit another cluster for the same service
        within this window (prevents incident storms).
        """
        self.window_seconds = window_seconds
        self.min_anomalies = min_anomalies
        self.cooldown_seconds = cooldown_seconds
        # service -> list of (ts, anomaly)
        self._pending: dict[str, list[tuple[float, Anomaly]]] = defaultdict(list)
        # service -> last emission ts
        self._last_emitted: dict[str, float] = {}

    def add(self, anomaly: Anomaly) -> AnomalyCluster | None:
        """Add an anomaly to its service's pending window. Emit a cluster if
        the window is closed or cooldown has expired."""
        now = anomaly.ts.timestamp()
        key = anomaly.service
        bucket = self._pending[key]

        # Drop anomalies that have aged out of the window
        cutoff = now - self.window_seconds
        self._pending[key] = [(t, a) for (t, a) in bucket if t >= cutoff]
        bucket = self._pending[key]

        bucket.append((now, anomaly))

        # Decide whether to emit. Emit when:
        #   - cooldown has expired AND
        #   - we have at least min_anomalies AND
        #   - we haven't emitted in the last `cooldown_seconds`
        last_emit = self._last_emitted.get(key, 0.0)
        if now - last_emit < self.cooldown_seconds:
            return None
        if len(bucket) < self.min_anomalies:
            return None

        # Pick the highest-severity anomaly to decide cluster severity
        max_score = max(a.score for _, a in bucket)
        if max_score >= 0.95:
            sev = Severity.CRITICAL
        elif max_score >= 0.7:
            sev = Severity.WARNING
        else:
            sev = Severity.INFO

        # Pick a trace_id if any anomaly's source signal had one
        trace_id = None
        for _, a in bucket:
            # The signal_id is what we have — fetch trace_id via DB if needed
            # (kept simple here; in production we'd join via signal_id)
            pass

        cluster = AnomalyCluster(
            cluster_id=uuid4(),
            service=key,
            namespace=bucket[0][1].namespace,
            anomalies=[a for _, a in bucket],
            severity=sev,
            summary=f"{len(bucket)} anomalies on {key} (max score={max_score:.2f})",
            trace_id=trace_id,
        )

        # Reset bucket and cooldown
        self._pending[key] = []
        self._last_emitted[key] = now
        return cluster

    def pending_count(self) -> int:
        return sum(len(v) for v in self._pending.values())

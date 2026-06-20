"""Rules-based detector — the deterministic "panic button" path (Layer 6 edge case #6).

When CPU > 95% or connection count > max, we don't wait for the LLM to figure
out what's wrong. We fire immediately so the execution layer can shed load or
scale horizontally within seconds. The reasoning engine kicks in *after*
stabilization to figure out *why*.
"""
from __future__ import annotations

import logging

from sentinel_common import Anomaly, Signal, SignalSource, Severity

log = logging.getLogger(__name__)


class RulesDetector:
    def __init__(self, cpu_critical_pct: float = 95.0, conn_critical: int = 9000,
                 restart_critical: int = 5):
        self.cpu_critical_pct = cpu_critical_pct
        self.conn_critical = conn_critical
        self.restart_critical = restart_critical

    async def observe(self, sig: Signal) -> list[Anomaly]:
        if sig.source != SignalSource.PROMETHEUS:
            return []
        metric_name = (sig.payload.get("alertname")
                       or sig.payload.get("labels", {}).get("alertname"))
        if metric_name is None:
            return []
        value = float(sig.payload.get("value") or 0.0)
        out: list[Anomaly] = []

        # CPU saturation
        if metric_name == "pod_cpu_pct" and value > self.cpu_critical_pct:
            out.append(Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="rules:panic_cpu",
                score=1.0,
                dimensions={"pod_cpu_pct": value},
                explanation=(
                    f"PANIC: CPU={value:.1f}% > {self.cpu_critical_pct}% — triggering "
                    f"deterministic fast-path mitigation"
                ),
            ))
        # Connection saturation
        elif metric_name == "active_connections" and value > self.conn_critical:
            out.append(Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="rules:panic_connections",
                score=1.0,
                dimensions={"active_connections": value},
                explanation=(
                    f"PANIC: active_connections={int(value)} > {self.conn_critical} — "
                    f"triggering load-shed / scale-out fast path"
                ),
            ))
        # Crash-loop detection
        elif metric_name == "pod_restart_count" and value >= self.restart_critical:
            out.append(Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="rules:crash_loop",
                score=min(1.0, value / 10.0),
                dimensions={"pod_restart_count": value},
                explanation=(
                    f"CrashLoopBackOff suspected: {int(value)} restarts observed — "
                    f"investigate logs / recent deployment"
                ),
            ))
        # 5xx spike
        elif metric_name == "http_5xx_rate" and value > 1.0:
            out.append(Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="rules:5xx_spike",
                score=min(1.0, value / 10.0),
                dimensions={"http_5xx_rate": value},
                explanation=(
                    f"5xx error rate elevated: {value:.2f} req/s — investigate "
                    f"upstream dependencies and recent deployments"
                ),
            ))
        return out

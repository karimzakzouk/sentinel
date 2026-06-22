"""CUSUM detector — univariate drift detection for individual metrics.

CUSUM = Cumulative Sum. It tracks the cumulative deviation of a metric from
its baseline mean and fires when that cumulative sum crosses a threshold.
It's the canonical algorithm for "this metric is drifting, not just spiking".

We run one CUSUM per (service, metric_name) pair.
"""
from __future__ import annotations

import logging
import math
from collections import defaultdict, deque
from typing import Any

from sentinel_common import Anomaly, Signal, SignalSource

log = logging.getLogger(__name__)


class CUSUMDetector:
    def __init__(self, threshold: float = 5.0, drift: float = 0.02,
                 warmup: int = 30, window_size: int = 200):
        """`threshold` = number of std deviations of cumulative drift before firing.
        `drift` = allowance parameter (k in CUSUM literature).
        """
        self.threshold = threshold
        self.drift = drift
        self.warmup = warmup
        self.window_size = window_size
        # (service, metric) -> state dict
        self._streams: dict[tuple[str, str], dict[str, Any]] = defaultdict(dict)

    async def observe(self, sig: Signal) -> list[Anomaly]:
        if sig.source != SignalSource.PROMETHEUS:
            return []
        metric_name = sig.payload.get("alertname") or sig.payload.get("labels", {}).get("alertname")
        if metric_name is None:
            return []
        value = float(sig.payload.get("value") or 0.0)
        key = (sig.service, metric_name)
        state = self._streams[key]

        # Initialize the rolling window
        if "window" not in state:
            state["window"] = deque(maxlen=self.window_size)
            state["s_pos"] = 0.0
            state["s_neg"] = 0.0
            state["baseline_mean"] = 0.0
            state["baseline_std"] = 1.0
            state["count"] = 0

        state["window"].append(value)
        state["count"] += 1
        n = state["count"]

        # Recompute baseline mean/std on every observation (cheap for window<=200)
        w = list(state["window"])
        if n < 2:
            return []
        mean = sum(w) / len(w)
        var = sum((x - mean) ** 2 for x in w) / len(w)
        std = math.sqrt(var) if var > 0 else 1.0

        # Update baseline only during warmup
        if n <= self.warmup:
            state["baseline_mean"] = mean
            state["baseline_std"] = std
            return []

        # Standardize against the baseline
        z = (value - state["baseline_mean"]) / max(state["baseline_std"], 1e-9)

        # Two-sided CUSUM
        state["s_pos"] = max(0.0, state["s_pos"] + z - self.drift)
        state["s_neg"] = min(0.0, state["s_neg"] + z + self.drift)

        # Fire if either side exceeds threshold
        if state["s_pos"] > self.threshold or -state["s_neg"] > self.threshold:
            direction = "upward" if state["s_pos"] > self.threshold else "downward"
            score = min(1.0, max(state["s_pos"], -state["s_neg"]) / (self.threshold * 2))
            anomaly = Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="cusum",
                score=score,
                dimensions={metric_name: value},
                explanation=(
                    f"CUSUM {direction} drift on {metric_name} for {sig.service}: "
                    f"value={value:.4g}, baseline_mean={state['baseline_mean']:.4g}, "
                    f"baseline_std={state['baseline_std']:.4g}, s_pos={state['s_pos']:.2f}, "
                    f"s_neg={state['s_neg']:.2f}"
                ),
            )
            # Reset cumulative sums after firing to avoid duplicate alerts
            state["s_pos"] = 0.0
            state["s_neg"] = 0.0
            return [anomaly]
        return []

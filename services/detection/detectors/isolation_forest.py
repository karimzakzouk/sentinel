"""Isolation Forest detector — multi-dimensional anomaly detection.

We keep a rolling window of the last N metric observations per service and
re-fit a small Isolation Forest every K observations. The forest flags
observations whose path-length in the tree is unusually short (= easy to
isolate = anomalous).
"""
from __future__ import annotations

import asyncio
import logging
import time
from collections import defaultdict, deque
from typing import Any

import numpy as np
from sklearn.ensemble import IsolationForest  # type: ignore

from sentinel_common import Anomaly, Signal, SignalSource

log = logging.getLogger(__name__)


class IsolationForestDetector:
    """Multi-dimensional anomaly detector.

    Each service gets its own rolling window. We re-fit the forest every
    `refit_every` observations to keep the model fresh.
    """

    def __init__(
        self,
        contamination: float = 0.05,
        min_samples: int = 50,
        window_size: int = 1000,
        refit_every: int = 50,
    ):
        self.contamination = contamination
        self.min_samples = min_samples
        self.window_size = window_size
        self.refit_every = refit_every
        # service -> deque of {metric_name: value, ts, signal_id}
        self._window: dict[str, deque[dict[str, Any]]] = defaultdict(deque)
        # service -> last fit count
        self._last_fit_count: dict[str, int] = defaultdict(int)
        # service -> fitted model
        self._models: dict[str, IsolationForest] = {}
        # Lock per service so we don't refit twice in parallel
        self._locks: dict[str, asyncio.Lock] = defaultdict(asyncio.Lock)

    async def observe(self, sig: Signal) -> Anomaly | None:
        # Only operate on Prometheus metrics — they have the multi-dim payload
        # we need.
        if sig.source != SignalSource.PROMETHEUS:
            return None
        metric_name = sig.payload.get("alertname") or sig.payload.get("labels", {}).get("alertname")
        if metric_name is None:
            return None

        # Build the observation vector
        value = float(sig.payload.get("value") or 0.0)
        observation = {
            "ts": sig.ts.timestamp(),
            "signal_id": str(sig.signal_id),
            metric_name: value,
        }

        w = self._window[sig.service]
        w.append(observation)
        if len(w) > self.window_size:
            w.popleft()

        # Need enough samples before we even start scoring
        if len(w) < self.min_samples:
            return None

        lock = self._locks[sig.service]
        async with lock:
            # Refit on a cadence
            if (len(w) - self._last_fit_count[sig.service]) >= self.refit_every:
                await asyncio.to_thread(self._fit, sig.service, w)
                self._last_fit_count[sig.service] = len(w)

            model = self._models.get(sig.service)
            if model is None:
                return None

            # Score the latest observation
            feature_names, X = self._build_matrix(w)
            if X.shape[0] == 0:
                return None
            latest_vec = X[-1:].reshape(1, -1)
            score = -float(model.score_samples(latest_vec)[0])  # higher = more anomalous
            # Normalize to 0..1 via sigmoid
            normalized = 1.0 / (1.0 + float(np.exp(-score + 1.0)))

            # Threshold: the top `contamination` fraction of scores
            # The IsolationForest already labels anomalies via predict():
            #   1 = normal, -1 = anomaly
            is_anomaly = model.predict(latest_vec)[0] == -1
            if not is_anomaly and normalized < 0.7:
                return None

            return Anomaly(
                signal_id=sig.signal_id,
                service=sig.service,
                namespace=sig.namespace,
                detector="isolation_forest",
                score=normalized,
                dimensions={metric_name: value},
                explanation=(
                    f"IsolationForest flagged {metric_name}={value:.4g} for {sig.service} "
                    f"(raw score={score:.4f}, normalized={normalized:.4f})"
                ),
            )

    def _fit(self, service: str, window: deque[dict[str, Any]]) -> None:
        try:
            _, X = self._build_matrix(window)
            if X.shape[0] < self.min_samples:
                return
            m = IsolationForest(
                n_estimators=80,
                contamination=self.contamination,
                random_state=42,
                n_jobs=-1,
            )
            m.fit(X)
            self._models[service] = m
        except Exception as e:  # noqa: BLE001
            log.warning("IF fit failed for %s: %s", service, e)

    def _build_matrix(self, window: deque[dict[str, Any]]) -> tuple[list[str], np.ndarray]:
        """Build a dense matrix from the heterogeneous window.

        Each observation may carry different metric names. We collect the
        union of metric names and use NaN for missing values (IsolationForest
        handles NaNs in recent sklearn versions).
        """
        all_metrics: set[str] = set()
        for obs in window:
            all_metrics.update(k for k in obs.keys() if k not in ("ts", "signal_id"))
        metric_names = sorted(all_metrics)
        rows: list[list[float]] = []
        for obs in window:
            row = [float(obs.get(m, np.nan)) for m in metric_names]
            rows.append(row)
        if not rows:
            return metric_names, np.zeros((0, len(metric_names)))
        X = np.array(rows, dtype=float)
        # Replace NaN with column mean (better for IF than leaving NaN)
        col_mean = np.nanmean(X, axis=0)
        nan_mask = np.isnan(X)
        if nan_mask.any():
            inds = np.where(nan_mask)
            X[inds] = np.take(col_mean, inds[1])
        return metric_names, X

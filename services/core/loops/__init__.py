"""Loops package — the 7 background asyncio tasks that drive Sentinel.

Each loop is a single async function that runs forever (until cancelled):
  - ingestion_loop    — K8s events + Prometheus/Loki → signals
  - detection_loop    — signals → anomaly clusters
  - reasoning_loop    — anomaly clusters → incidents (LangGraph)
  - safety_loop       — incidents → OPA verdict → execution queue
  - execution_loop    — approved incidents → sandbox + PR
  - feedback_loop     — resolved incidents → Beta tracker + Weaviate + postmortem
  - crd_watcher_loop  — SentinelIncident CRs → anomaly clusters (manual trigger)
"""

from __future__ import annotations

from .crd_watcher import crd_watcher_loop
from .detection import detection_loop
from .execution import execution_loop
from .feedback import feedback_loop
from .ingestion import ingestion_loop
from .reasoning import reasoning_loop
from .safety import safety_loop

__all__ = [
    "crd_watcher_loop",
    "detection_loop",
    "execution_loop",
    "feedback_loop",
    "ingestion_loop",
    "reasoning_loop",
    "safety_loop",
]

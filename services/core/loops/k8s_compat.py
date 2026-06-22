"""K8s helper aliases — re-exports from `k8s/` so loops can import as relative.

This avoids circular imports: the loops import from here, and this module
imports from the `k8s` package which has no dependencies on the loops.
"""

from __future__ import annotations

from .k8s import (
    fetch_crashing_pod_logs,
    get_prod_image,
    lookup_workload_annotations,
    patch_cr_status,
)

__all__ = [
    "fetch_crashing_pod_logs",
    "get_prod_image",
    "lookup_workload_annotations",
    "patch_cr_status",
]

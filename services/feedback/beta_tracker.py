"""Beta distribution confidence tracker.

Each (service, fix_pattern) pair has a Beta(alpha, beta) distribution.
The mean alpha/(alpha+beta) is the historical success rate of that fix
pattern for that service.

  - Start with priors alpha=1, beta=1 (uniform)
  - On each resolved incident:
      - if success: alpha += 1
      - if failure: beta  += 1

The BetaTracker wraps the persistent BetaStore so the reasoning layer can
fetch priors synchronously during hypothesis scoring.
"""

from __future__ import annotations

import logging
from typing import Any

log = logging.getLogger(__name__)


class BetaTracker:
    def __init__(self, beta_store: Any):
        self.store = beta_store

    async def get_prior(self, service: str, fix_pattern: str) -> tuple[float, float]:
        """Returns (alpha, beta) for the given (service, fix_pattern)."""
        alpha, beta, _, _ = await self.store.get(service, fix_pattern)
        return alpha, beta

    async def get_prior_mean(self, service: str, fix_pattern: str) -> float:
        """Returns the prior mean = alpha / (alpha + beta)."""
        alpha, beta = await self.get_prior(service, fix_pattern)
        return alpha / (alpha + beta) if (alpha + beta) > 0 else 0.5

    async def record(self, service: str, fix_pattern: str, success: bool) -> tuple[float, float]:
        """Update alpha or beta based on the outcome. Returns new (alpha, beta)."""
        return await self.store.record(service, fix_pattern, success)

    def posterior_mean(self, alpha: float, beta: float) -> float:
        """Compute the posterior mean for the given alpha, beta."""
        return alpha / (alpha + beta) if (alpha + beta) > 0 else 0.5

    def posterior_std(self, alpha: float, beta: float) -> float:
        """Compute the posterior standard deviation."""
        n = alpha + beta
        if n <= 1:
            return 0.5
        var = (alpha * beta) / ((n**2) * (n + 1))
        return var**0.5

    def confidence_interval(self, alpha: float, beta: float, z: float = 1.96) -> tuple[float, float]:
        """95% confidence interval (default z=1.96) for the posterior mean."""
        mean = self.posterior_mean(alpha, beta)
        std = self.posterior_std(alpha, beta)
        return (max(0.0, mean - z * std), min(1.0, mean + z * std))

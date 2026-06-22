"""Beta tracker tests — verifies the math of confidence calibration."""

# We can't easily test the async db store without a real Postgres. Instead
# we test the BetaTracker's math functions directly.
from feedback.beta_tracker import BetaTracker


class TestBetaMath:
    def test_prior_mean_uniform(self):
        bt = BetaTracker(beta_store=None)
        # alpha=1, beta=1 => mean=0.5
        assert bt.posterior_mean(1.0, 1.0) == 0.5

    def test_prior_mean_after_50_successes_2_failures(self):
        """The spec example: 50 successes + 2 failures => ~0.96 confidence."""
        bt = BetaTracker(beta_store=None)
        # Start with priors alpha=1, beta=1
        alpha, beta = 1.0, 1.0
        # 50 successes
        for _ in range(50):
            alpha += 1
        # 2 failures
        for _ in range(2):
            beta += 1
        # mean = 51 / (51 + 3) = 0.944
        mean = bt.posterior_mean(alpha, beta)
        assert 0.93 < mean < 0.97, f"Expected ~0.944, got {mean}"

    def test_posterior_std_decreases_with_more_data(self):
        """As we gather more evidence, the variance should decrease."""
        bt = BetaTracker(beta_store=None)
        std_low_data = bt.posterior_std(2.0, 2.0)  # alpha+beta = 4
        std_high_data = bt.posterior_std(51.0, 51.0)  # alpha+beta = 102
        assert std_high_data < std_low_data

    def test_confidence_interval_is_in_unit_range(self):
        bt = BetaTracker(beta_store=None)
        low, high = bt.confidence_interval(1.0, 1.0, z=1.96)
        assert 0.0 <= low <= 1.0
        assert 0.0 <= high <= 1.0
        assert low < high

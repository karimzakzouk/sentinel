package stats

import (
	"math"
	"testing"
)

func TestBinomialUpperTail(t *testing.T) {
	for _, tc := range []struct {
		k, n int
		p    float64
		want float64
	}{
		{0, 10, 0.3, 1},
		{11, 10, 0.3, 0},
		{10, 10, 0.5, math.Pow(0.5, 10)},
		{1, 10, 0.1, 1 - math.Pow(0.9, 10)},
		{2, 12, 2.0 / 7, 1 - math.Pow(5.0/7, 12) - 12*(2.0/7)*math.Pow(5.0/7, 11)},
		// A large n stays finite and exact enough: the mean of Binomial(10000, 0.5) is its median.
		{5001, 10000, 0.5, 0.5 - 0.5*0.007978646}, // P(X ≥ 5001) = (1 − P(X = 5000)) / 2
	} {
		got := BinomialUpperTail(tc.k, tc.n, tc.p)
		if math.Abs(got-tc.want) > 1e-6*math.Max(1, tc.want) && math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("P(X ≥ %d | n=%d, p=%g) = %.9g, want %.9g", tc.k, tc.n, tc.p, got, tc.want)
		}
	}
}

func TestRateRatioTest(t *testing.T) {
	// A new error: none in five minutes on old pods, 20 in one minute on new ones.
	if p := RateRatioTest(0, 300, 20, 60, 2); p > 1e-9 {
		t.Errorf("new error p = %g", p)
	}
	// The same rate before and after is no evidence of a doubling.
	if p := RateRatioTest(50, 300, 10, 60, 2); p < 0.5 {
		t.Errorf("unchanged rate p = %g", p)
	}
	// Double the rate is exactly the null: no strong evidence either way.
	if p := RateRatioTest(50, 300, 20, 60, 2); p < 0.1 || p > 0.9 {
		t.Errorf("doubled rate p = %g", p)
	}
	if p := RateRatioTest(5, 0, 5, 60, 2); p != 1 {
		t.Errorf("without a before sample there is no test, p = %g", p)
	}
}

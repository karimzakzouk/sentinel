package stats

import (
	"math"
	"testing"
)

func TestRegIncBetaClosedForms(t *testing.T) {
	for _, x := range []float64{0.01, 0.2, 0.5, 0.8, 0.99} {
		// Beta(1, 1) is uniform; Beta(a, 1) has CDF x^a; Beta(1, b) has 1-(1-x)^b.
		if got := RegIncBeta(x, 1, 1); !approx(got, x, 1e-12) {
			t.Errorf("I_%v(1,1) = %v", x, got)
		}
		if got, want := RegIncBeta(x, 7, 1), math.Pow(x, 7); !approx(got, want, 1e-10) {
			t.Errorf("I_%v(7,1) = %v, want %v", x, got, want)
		}
		if got, want := RegIncBeta(x, 1, 5), 1-math.Pow(1-x, 5); !approx(got, want, 1e-10) {
			t.Errorf("I_%v(1,5) = %v, want %v", x, got, want)
		}
	}
	// Symmetry: I_x(a, b) = 1 - I_{1-x}(b, a).
	if a, b := RegIncBeta(0.3, 12.5, 4), 1-RegIncBeta(0.7, 4, 12.5); !approx(a, b, 1e-12) {
		t.Errorf("symmetry: %v vs %v", a, b)
	}
}

// The design's numbers: under a uniform prior, 28 straight successes put
// the 5th percentile of the success rate at 90%, and 58 at 95%.
func TestBetaQuantileOfStraightSuccesses(t *testing.T) {
	for successes, want := range map[int]float64{28: 0.90, 58: 0.95} {
		a := float64(successes + 1)
		got := BetaQuantile(0.05, a, 1)
		if exact := math.Pow(0.05, 1/a); !approx(got, exact, 1e-9) || got < want || BetaQuantile(0.05, a-1, 1) >= want {
			t.Errorf("%d successes: 5th percentile %v (exactly %v), want the first count to clear %v", successes, got, exact, want)
		}
	}
}

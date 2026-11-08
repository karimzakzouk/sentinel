package stats

import "math"

// BinomialUpperTail returns P(X ≥ k) for X ~ Binomial(n, p), summed in log
// space so large counts stay accurate.
func BinomialUpperTail(k, n int, p float64) float64 {
	switch {
	case k <= 0 || p >= 1:
		return 1
	case k > n || p <= 0:
		return 0
	}
	logP, logQ := math.Log(p), math.Log1p(-p)
	terms := make([]float64, 0, n-k+1)
	top := math.Inf(-1)
	for i := k; i <= n; i++ {
		t := logChoose(n, i) + float64(i)*logP + float64(n-i)*logQ
		terms = append(terms, t)
		top = math.Max(top, t)
	}
	sum := 0.0
	for _, t := range terms {
		sum += math.Exp(t - top)
	}
	return math.Min(1, math.Exp(top+math.Log(sum)))
}

// RateRatioTest asks whether c1 events over exposure t1 came at more than
// ratio times the rate of c0 events over exposure t0, and returns the
// p-value. It is the conditional test of a Poisson rate ratio: given
// n = c0 + c1 events, c1 ~ Binomial(n, ratio·t1 / (t0 + ratio·t1)) when the
// true ratio is exactly ratio.
func RateRatioTest(c0 int, t0 float64, c1 int, t1, ratio float64) float64 {
	if c1 == 0 || t0 <= 0 || t1 <= 0 {
		return 1
	}
	return BinomialUpperTail(c1, c0+c1, ratio*t1/(t0+ratio*t1))
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

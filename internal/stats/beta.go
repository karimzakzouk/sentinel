package stats

import "math"

// RegIncBeta returns the regularized incomplete beta function I_x(a, b): the
// probability that a Beta(a, b) variable is at most x. It uses the
// continued fraction, which converges fast on the side of the mean it is
// evaluated on.
func RegIncBeta(x, a, b float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	lgab, _ := math.Lgamma(a + b)
	front := math.Exp(a*math.Log(x) + b*math.Log1p(-x) + lgab - lga - lgb)
	if x < (a+1)/(a+b+2) {
		return front * betaContinuedFraction(x, a, b) / a
	}
	return 1 - front*betaContinuedFraction(1-x, b, a)/b
}

// betaContinuedFraction evaluates the incomplete beta function's continued
// fraction by the modified Lentz method.
func betaContinuedFraction(x, a, b float64) float64 {
	const tiny, eps = 1e-300, 1e-15
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 300; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d, c = 1+aa*d, 1+aa/c
		if math.Abs(d) < tiny {
			d = tiny
		}
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d, c = 1+aa*d, 1+aa/c
		if math.Abs(d) < tiny {
			d = tiny
		}
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}

// BetaQuantile returns the p-quantile of Beta(a, b), to within 1e-12.
func BetaQuantile(p, a, b float64) float64 {
	lo, hi := 0.0, 1.0
	for range 64 {
		mid := (lo + hi) / 2
		if RegIncBeta(mid, a, b) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

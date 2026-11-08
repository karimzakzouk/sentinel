// Package stats holds the statistical building blocks of the detectors. Every
// function documents its model, so its assumptions can be checked.
package stats

import (
	"math"
)

// Gamma is a Gamma(shape, rate) distribution over an event rate.
type Gamma struct {
	Shape float64
	Rate  float64
}

// Mean is the expected rate.
func (g Gamma) Mean() float64 { return g.Shape / g.Rate }

// Posterior updates the prior with count events observed over exposure time.
func (g Gamma) Posterior(count, exposure float64) Gamma {
	return Gamma{Shape: g.Shape + count, Rate: g.Rate + exposure}
}

// PredictiveTail returns P(X >= x) for the number of events X in a new window
// of length w, under the Gamma–Poisson model: rates are Gamma(g), and given a
// rate, events are Poisson. The predictive distribution is negative binomial
// with r = shape and success probability p = rate / (rate + w).
func (g Gamma) PredictiveTail(x int, w float64) float64 {
	if x <= 0 {
		return 1
	}
	r, p := g.Shape, g.Rate/(g.Rate+w)
	if r <= 0 || p <= 0 {
		return 1
	}
	if p >= 1 {
		return 0
	}
	q := 1 - p
	logPMF := func(k int) float64 {
		lg1, _ := math.Lgamma(r + float64(k))
		lg2, _ := math.Lgamma(r)
		lg3, _ := math.Lgamma(float64(k) + 1)
		return lg1 - lg2 - lg3 + r*math.Log(p) + float64(k)*math.Log(q)
	}
	mean := r * q / p
	if float64(x) <= mean {
		// The tail is large; one minus the lower sum is accurate enough.
		lower := 0.0
		for k := 0; k < x; k++ {
			lower += math.Exp(logPMF(k))
		}
		return math.Max(0, math.Min(1, 1-lower))
	}
	// Past the mean the terms shrink geometrically, so sum the tail directly to
	// keep precision for very small probabilities.
	sum := 0.0
	for k := x; ; k++ {
		term := math.Exp(logPMF(k))
		sum += term
		if term < sum*1e-15 || k-x > 100000 {
			break
		}
	}
	return math.Min(1, sum)
}

// Series is one count series' history: events and the time it was observed.
type Series struct {
	Count    float64
	Exposure float64
}

// PooledPrior fits a Gamma prior to many series' rates (empirical Bayes), so a
// new or quiet series borrows strength from similar ones.
//
// With fewer than three series, or no events at all, there is nothing to fit:
// the result is the hyperprior updated with everything observed, so plenty of
// quiet time across workloads lowers the expected rate. Otherwise the prior is
// fitted by the method of moments on the data alone, and its confidence (the
// shape) is capped by the number of events seen, so a prior is never more
// certain than the data behind it.
func PooledPrior(series []Series, hyper Gamma) Gamma {
	var count, exposure float64
	for _, s := range series {
		count += s.Count
		exposure += s.Exposure
	}
	if len(series) < 3 || count == 0 || exposure == 0 {
		return hyper.Posterior(count, exposure)
	}
	mean := count / exposure
	// Between-series variance of rates, minus the Poisson noise each rate
	// estimate carries on its own.
	var sumW, sumWD2, noise float64
	for _, s := range series {
		if s.Exposure <= 0 {
			continue
		}
		rate := s.Count / s.Exposure
		sumW += s.Exposure
		sumWD2 += s.Exposure * (rate - mean) * (rate - mean)
		noise += mean
	}
	between := math.Max((sumWD2-noise)/sumW, mean*mean/count)
	shape := math.Min(math.Max(mean*mean/between, 0.1), count)
	return Gamma{Shape: shape, Rate: shape / mean}
}

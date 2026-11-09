package stats

import "math"

// EProcess tests a null about a Bernoulli mean by betting: that the mean is
// at most Bound when Above, at least Bound otherwise. Each outcome
// multiplies its value by 1 + λ(x − Bound), or 1 + λ(Bound − x), for a bet
// λ chosen from earlier outcomes only and never staking all of the value.
// Under the null the value is a nonnegative supermartingale, so by Ville's
// inequality it reaches 1/α with probability at most α, however long the
// test runs and whenever it stops.
type EProcess struct {
	Bound float64
	Above bool
	value float64
	n     int
	// hits counts outcomes on the alternative's side: successes when
	// Above, failures otherwise.
	hits int
}

// NewEProcess starts a test of "the mean is at most bound" (above) or "at
// least bound".
func NewEProcess(bound float64, above bool) *EProcess {
	return &EProcess{Bound: bound, Above: above, value: 1}
}

// maxStake caps a bet at this share of what one adverse outcome can take,
// so no outcome ever zeroes the value.
const maxStake = 0.9

// Observe adds one outcome.
func (e *EProcess) Observe(success bool) {
	hit := success == e.Above
	m := e.Bound
	if !e.Above {
		m = 1 - e.Bound
	}
	lambda := e.bet(m)
	if hit {
		e.value *= 1 + lambda*(1-m)
	} else {
		e.value *= 1 - lambda*m
	}
	e.n++
	if hit {
		e.hits++
	}
}

// bet is the Kelly fraction for an optimistic running estimate of the hit
// rate, (hits + 2)/(n + 2.25), against null rate m, clipped to [0,
// maxStake/m]. Any predictable bet keeps the test valid; the estimate only
// decides how fast evidence piles up. Simulated against a fix that works
// three times in four, this one needs 15 runs where (hits + 1)/(n + 1.5)
// needs 18, for a little less power at rates near the bound.
func (e *EProcess) bet(m float64) float64 {
	p := (float64(e.hits) + 2) / (float64(e.n) + 2.25)
	return math.Max(0, math.Min((p-m)/(m*(1-m)), maxStake/m))
}

// Value is the evidence against the null so far.
func (e *EProcess) Value() float64 { return e.value }

// N is the number of outcomes observed.
func (e *EProcess) N() int { return e.n }

// Rejects reports whether the null is rejected at level alpha.
func (e *EProcess) Rejects(alpha float64) bool { return e.value >= 1/alpha }

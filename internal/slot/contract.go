package slot

import (
	"errors"
	"fmt"
	"math"
)

// Weighted is one outcome with its probability.
type Weighted[T comparable] struct {
	Value T       `json:"value"`
	P     float64 `json:"p"`
}

// Distribution is a probability distribution over discrete outcomes, such as
// fault classes. Classification slots return one, so engines are compared on
// calibration (Brier score, reliability), not just on their top answer.
type Distribution[T comparable] []Weighted[T]

const probabilityTolerance = 1e-6

// Validate checks that the distribution is non-empty, has no repeated
// outcomes, and that its probabilities are valid and sum to one.
func (d Distribution[T]) Validate() error {
	if len(d) == 0 {
		return errors.New("distribution is empty")
	}
	seen := make(map[T]bool, len(d))
	var sum float64
	for _, w := range d {
		if math.IsNaN(w.P) || w.P < 0 || w.P > 1 {
			return fmt.Errorf("probability %v of %v is outside [0, 1]", w.P, w.Value)
		}
		if seen[w.Value] {
			return fmt.Errorf("outcome %v appears twice", w.Value)
		}
		seen[w.Value] = true
		sum += w.P
	}
	if math.Abs(sum-1) > probabilityTolerance {
		return fmt.Errorf("probabilities sum to %v, not 1", sum)
	}
	return nil
}

// Top returns the most likely outcome. Ties go to the earlier entry.
func (d Distribution[T]) Top() Weighted[T] {
	var best Weighted[T]
	for i, w := range d {
		if i == 0 || w.P > best.P {
			best = w
		}
	}
	return best
}

// P returns the probability of v, or zero when v is absent.
func (d Distribution[T]) P(v T) float64 {
	for _, w := range d {
		if w.Value == v {
			return w.P
		}
	}
	return 0
}

// Ranked is one candidate in a Ranking.
type Ranked[T any] struct {
	Item  T       `json:"item"`
	Score float64 `json:"score"`
	InSet bool    `json:"inSet"`
}

// Ranking orders candidates from most to least likely and marks a confidence
// set: the leading candidates that together are expected to contain the true
// answer with the stated coverage. Reporting a set instead of one guess is how
// Sentinel says "it is one of these three".
type Ranking[T any] struct {
	Items    []Ranked[T] `json:"items"`
	Coverage float64     `json:"coverage"`
}

// Validate checks ordering, finiteness and that the set is a leading prefix.
func (r Ranking[T]) Validate() error {
	if len(r.Items) == 0 {
		return nil
	}
	if r.Coverage <= 0 || r.Coverage > 1 {
		return fmt.Errorf("coverage %v is outside (0, 1]", r.Coverage)
	}
	inPrefix := true
	for i, it := range r.Items {
		if math.IsNaN(it.Score) || math.IsInf(it.Score, 0) {
			return fmt.Errorf("item %d has a non-finite score", i)
		}
		if i > 0 && it.Score > r.Items[i-1].Score {
			return fmt.Errorf("item %d scores higher than item %d", i, i-1)
		}
		if it.InSet && !inPrefix {
			return fmt.Errorf("item %d is in the confidence set but an earlier item is not", i)
		}
		inPrefix = inPrefix && it.InSet
	}
	if !r.Items[0].InSet {
		return errors.New("the confidence set is empty")
	}
	return nil
}

// Set returns the items in the confidence set.
func (r Ranking[T]) Set() []T {
	var out []T
	for _, it := range r.Items {
		if !it.InSet {
			break
		}
		out = append(out, it.Item)
	}
	return out
}

// Estimate is a quantity with an interval at a stated confidence level.
type Estimate struct {
	Value float64 `json:"value"`
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
	Level float64 `json:"level"`
}

// Validate checks that the value lies inside a well-formed interval.
func (e Estimate) Validate() error {
	for _, v := range []float64{e.Value, e.Lower, e.Upper} {
		if math.IsNaN(v) {
			return errors.New("estimate contains NaN")
		}
	}
	if e.Lower > e.Value || e.Value > e.Upper {
		return fmt.Errorf("value %v is outside its interval [%v, %v]", e.Value, e.Lower, e.Upper)
	}
	if e.Level <= 0 || e.Level >= 1 {
		return fmt.Errorf("confidence level %v is outside (0, 1)", e.Level)
	}
	return nil
}

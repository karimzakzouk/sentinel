package causal

import "math"

// sigmoid is the standard logistic function. Used to squash a lag (in
// minutes) into a 0..1 score where 0min=0.5, 1min=0.73, 5min=0.92, 10min=0.98.
//
// The intuition: a 10-minute lead is much stronger evidence of causation
// than a 10-second lead (which could be coincidence).
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

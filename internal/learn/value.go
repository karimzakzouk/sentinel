// Package learn proposes values a fix sets from what a workload did while
// it was healthy, for settings where putting back the old value can be as
// wrong as the new one: a memory limit lowered by a commit may have been
// lowered on purpose, just too far.
package learn

import (
	"fmt"
	"math"
	"slices"
)

// MinSamples is the fewest observations a value is learned from.
const MinSamples = 20

const mebibyte = 1 << 20

// MemoryLimit is a memory limit for a container from its working sets
// while healthy, in bytes: the 99th percentile with room for a peak the
// samples missed, the larger of 30% and 8 MiB, rounded up to a mebibyte
// and written as a Kubernetes quantity.
func MemoryLimit(workingSets []float64) (string, error) {
	if len(workingSets) < MinSamples {
		return "", fmt.Errorf("%d samples of its memory use; %d are the fewest a limit is learned from", len(workingSets), MinSamples)
	}
	p := Quantile(workingSets, 0.99)
	limit := math.Max(1.3*p, p+8*mebibyte)
	return fmt.Sprintf("%dMi", int64(math.Ceil(limit/mebibyte))), nil
}

// Quantile is the q-quantile of values, 0 < q <= 1: the smallest value
// with at least a q share of values at or below it.
func Quantile(values []float64, q float64) float64 {
	s := slices.Clone(values)
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

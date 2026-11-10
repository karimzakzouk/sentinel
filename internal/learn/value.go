// Package learn proposes values a fix sets from what a workload did while
// it was healthy, for settings where putting back the old value can be as
// wrong as the new one: a memory, CPU or ephemeral-storage limit lowered by a
// commit may have
// been lowered on purpose, just too far.
package learn

import (
	"fmt"
	"math"
	"slices"
)

// MinSamples is the fewest observations a value is learned from.
const MinSamples = 20

const mebibyte = 1 << 20

// millicoreFloor is the least absolute room a learned CPU limit keeps
// above the 99th percentile of healthy use, in cores (20m).
const millicoreFloor = 0.020

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

// CPULimit is a CPU limit for a container from its use while healthy, in
// cores: the 99th percentile with room for a peak the samples missed, the
// larger of 30% and 20m, rounded up to a millicore and written as a
// Kubernetes millicore quantity.
// DiskLimit is an ephemeral-storage limit for a container from its use
// while healthy: the same quantile and room as MemoryLimit, since the API
// reports no separate ephemeral-storage series yet.
func DiskLimit(workingSets []float64) (string, error) {
	return MemoryLimit(workingSets)
}

func CPULimit(cores []float64) (string, error) {
	if len(cores) < MinSamples {
		return "", fmt.Errorf("%d samples of its CPU use; %d are the fewest a limit is learned from", len(cores), MinSamples)
	}
	p := Quantile(cores, 0.99)
	limit := math.Max(1.3*p, p+millicoreFloor)
	return fmt.Sprintf("%dm", int64(math.Ceil(limit*1000))), nil
}

// Quantile is the q-quantile of values, 0 < q <= 1: the smallest value
// with at least a q share of values at or below it.
func Quantile(values []float64, q float64) float64 {
	s := slices.Clone(values)
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

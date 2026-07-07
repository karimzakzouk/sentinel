package causal

import "math"

// mathExpImpl wraps math.Exp so the analyzer file doesn't need to import "math".
func mathExpImpl(x float64) float64 { return math.Exp(x) }

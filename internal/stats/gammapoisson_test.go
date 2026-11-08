package stats

import (
	"math"
	"testing"
)

func approx(a, b, rel float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= rel*math.Max(math.Abs(a), math.Abs(b))
}

// With a huge shape and rate the negative binomial approaches a Poisson with
// mean shape/rate × w, which has a closed form to compare against.
func TestPredictiveTailMatchesPoissonLimit(t *testing.T) {
	g := Gamma{Shape: 1e7, Rate: 1e7 / 2}
	for x, want := range map[int]float64{
		1: 1 - math.Exp(-2),
		3: 1 - math.Exp(-2)*(1+2+2),
		8: 1 - math.Exp(-2)*(1+2+2+4.0/3+2.0/3+4.0/15+4.0/45+8.0/315),
	} {
		if got := g.PredictiveTail(x, 1); !approx(got, want, 1e-4) {
			t.Errorf("P(X >= %d) = %.10g, want %.10g", x, got, want)
		}
	}
}

func TestPredictiveTailEdges(t *testing.T) {
	g := Gamma{Shape: 2, Rate: 10}
	if g.PredictiveTail(0, 1) != 1 {
		t.Error("P(X >= 0) must be 1")
	}
	prev := 1.0
	for x := 1; x < 60; x++ {
		p := g.PredictiveTail(x, 1)
		if p > prev || p < 0 {
			t.Fatalf("tail is not decreasing at %d: %g after %g", x, p, prev)
		}
		prev = p
	}
	if prev <= 0 || prev > 1e-20 {
		t.Errorf("far tail = %g; want a tiny positive number", prev)
	}
}

// The design's own example: one OOM in a service that never restarts is
// anomalous; the same OOM in a routinely restarting job is not.
func TestOneEventIsSurprisingOnlyForQuietSeries(t *testing.T) {
	const day, window = 86400.0, 300.0
	prior := Gamma{Shape: 0.5, Rate: 0.5 * 7 * day}
	quiet := prior.Posterior(0, 7*day)
	busy := prior.Posterior(700, 7*day)
	pQuiet, pBusy := quiet.PredictiveTail(1, window), busy.PredictiveTail(1, window)
	if pQuiet > 1e-3 {
		t.Errorf("one event in a quiet series: P = %g; want very small", pQuiet)
	}
	if pBusy < 0.1 {
		t.Errorf("one event in a busy series: P = %g; want unremarkable", pBusy)
	}
}

func TestPooledPrior(t *testing.T) {
	hyper := Gamma{Shape: 0.5, Rate: 0.5 * 604800}
	if g := PooledPrior(nil, hyper); !approx(g.Mean(), hyper.Mean(), 1e-9) {
		t.Errorf("no series: mean %g, want the hyperprior's %g", g.Mean(), hyper.Mean())
	}
	// Ten series with the same rate: the prior centers on it and is confident.
	var same []Series
	for range 10 {
		same = append(same, Series{Count: 50, Exposure: 1000})
	}
	g := PooledPrior(same, hyper)
	if !approx(g.Mean(), 0.05, 0.01) {
		t.Errorf("pooled mean = %g, want about 0.05", g.Mean())
	}
	// Widely different rates give a wider (smaller-shape) prior.
	varied := []Series{{1, 1000}, {10, 1000}, {100, 1000}, {300, 1000}}
	if gv := PooledPrior(varied, hyper); gv.Shape >= g.Shape {
		t.Errorf("varied rates gave shape %g, not wider than uniform rates' %g", gv.Shape, g.Shape)
	}
}

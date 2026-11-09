package stats

import (
	"math/rand/v2"
	"testing"
)

// At the edge of the null, where it is hardest to hold, an e-process may
// reject at level α on at most a share α of sequences, however long they
// run: Ville's inequality, checked by simulation.
func TestEProcessesHoldTheirLevelUnderOptionalStopping(t *testing.T) {
	const alpha, sequences, length = 0.05, 10000, 300
	for _, tc := range []struct {
		bound float64
		above bool
	}{{0.5, true}, {0.75, true}, {0.75, false}, {0.25, false}} {
		rng := rand.New(rand.NewPCG(7, uint64(tc.bound*100)))
		rejected := 0
		for range sequences {
			e := NewEProcess(tc.bound, tc.above)
			for range length {
				e.Observe(rng.Float64() < tc.bound)
				if e.Rejects(alpha) {
					rejected++
					break
				}
			}
		}
		if rate := float64(rejected) / sequences; rate > alpha {
			t.Errorf("bound %.2f above=%v: rejected %.3f of null sequences, above α=%.2f", tc.bound, tc.above, rate, alpha)
		} else {
			t.Logf("bound %.2f above=%v: rejected %.4f of null sequences at α=%.2f", tc.bound, tc.above, rate, alpha)
		}
	}
}

// How many runs the Lab needs at its tightest level, α/K with K = 2: a
// fix that always works, and a candidate that always fails.
func TestEProcessesDecideClearCasesQuickly(t *testing.T) {
	const alpha = 0.025
	runs := func(e *EProcess, outcome bool) int {
		for e.N() < 100 && !e.Rejects(alpha) {
			e.Observe(outcome)
		}
		return e.N()
	}
	for _, tc := range []struct {
		name    string
		e       *EProcess
		outcome bool
		within  int
	}{
		{"a fix is better than a failing baseline", NewEProcess(0.5, true), true, 6},
		{"a fix works more than three times in four", NewEProcess(0.75, true), true, 16},
		{"a failing candidate does not work three times in four", NewEProcess(0.75, false), false, 4},
	} {
		if n := runs(tc.e, tc.outcome); n > tc.within {
			t.Errorf("%s: took %d runs, more than %d", tc.name, n, tc.within)
		} else {
			t.Logf("%s: %d runs", tc.name, n)
		}
	}
}

// Evidence must not build on outcomes that agree with the null.
func TestEProcessesDoNotRejectOnTheNullsSide(t *testing.T) {
	e := NewEProcess(0.75, true)
	for range 200 {
		e.Observe(false)
	}
	if e.Value() > 1 {
		t.Fatalf("value %.3f after 200 failures, testing for success", e.Value())
	}
}

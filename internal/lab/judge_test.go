package lab

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// race runs blocks with the given success rates until the race decides or
// maxBlocks pass, and returns the verdict and the blocks it took.
func race(rng *rand.Rand, baseline float64, candidates, controls map[string]float64, maxBlocks int) (v1alpha1.ExperimentPhase, string, int) {
	var cands, ctrls []string
	for n := range candidates {
		cands = append(cands, n)
	}
	for n := range controls {
		ctrls = append(ctrls, n)
	}
	r := NewRace(0.05, cands, ctrls)
	for b := 1; ; b++ {
		outcomes := map[string]bool{}
		for n, p := range candidates {
			outcomes[n] = rng.Float64() < p
		}
		for n, p := range controls {
			outcomes[n] = rng.Float64() < p
		}
		r.Observe(rng.Float64() < baseline, outcomes)
		if phase, msg, final := r.Outcome(b < maxBlocks); final {
			return phase, msg, b
		}
	}
}

func TestTheRaceJudgesClearCases(t *testing.T) {
	deletion := map[string]float64{"deletion control": 0}
	for _, tc := range []struct {
		name       string
		baseline   float64
		candidates map[string]float64
		controls   map[string]float64
		phase      v1alpha1.ExperimentPhase
		says       string
		within     int
	}{
		{"a fix", 0, map[string]float64{"previous revision": 1}, deletion, PhasePassed, "the previous revision fixes it: it worked in", 20},
		{"a non-fix", 0, map[string]float64{"previous revision": 0}, deletion, PhaseFailed, "no candidate fixes it", 6},
		{"a failure the Lab cannot reproduce", 1, map[string]float64{"previous revision": 1}, deletion, PhaseNotSure, "the failure did not reproduce", 20},
		{"an oracle that cannot tell a fix from a deletion", 0, map[string]float64{"previous revision": 1},
			map[string]float64{"deletion control": 1}, PhaseNotSure, "the deletion control passed", 20},
		{"a reproduction", 0, nil, nil, PhasePassed, "the failure reproduces: the failing revision failed in", 6},
		{"a candidate too flaky to call", 0, map[string]float64{"previous revision": 0.8}, deletion, PhaseNotSure, "the runs ran out", 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phase, msg, blocks := race(rand.New(rand.NewPCG(3, 4)), tc.baseline, tc.candidates, tc.controls, 30)
			if phase != tc.phase || !strings.Contains(msg, tc.says) || blocks > tc.within {
				t.Fatalf("%s after %d blocks: %s; want %s saying %q within %d", phase, blocks, msg, tc.phase, tc.says, tc.within)
			}
			t.Logf("%s after %d blocks: %s", phase, blocks, msg)
		})
	}
}

// A candidate that works exactly three times in four, at the edge of what
// a fix must beat, against a baseline that always fails: the race may pass
// it on at most a share α of races, however many blocks they run.
func TestTheRaceHoldsItsLevel(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	const races = 2000
	passed := 0
	for range races {
		if phase, _, _ := race(rng, 0, map[string]float64{"candidate": worksAbove}, nil, 200); phase == PhasePassed {
			passed++
		}
	}
	if rate := float64(passed) / races; rate > 0.05 {
		t.Fatalf("passed %.3f of candidates at the edge, above α=0.05", rate)
	} else {
		t.Logf("passed %.4f of candidates at the edge, at α=0.05", rate)
	}
}

// With a second candidate, the verdict is still the first one's, and says
// how the other did: a configuration that fixes it, or one that does not.
func TestTheRaceReportsASecondCandidate(t *testing.T) {
	deletion := map[string]float64{"deletion control": 0}
	for _, tc := range []struct {
		name  string
		other float64
		says  string
	}{
		{"a configuration that fixes it", 1, "the configuration reverted fixes it too"},
		{"an image that fixes it", 0, "the configuration reverted does not"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRace(0.05, []string{"previous revision", "configuration reverted"}, []string{"deletion control"})
			rng := rand.New(rand.NewPCG(7, 8))
			var phase v1alpha1.ExperimentPhase
			var msg string
			for b := 1; b <= 40; b++ {
				r.Observe(false, map[string]bool{"previous revision": true, "configuration reverted": rng.Float64() < tc.other,
					"deletion control": rng.Float64() < deletion["deletion control"]})
				var final bool
				if phase, msg, final = r.Outcome(b < 40); final {
					break
				}
			}
			if phase != PhasePassed || !strings.Contains(msg, "the previous revision fixes it") || !strings.Contains(msg, tc.says) {
				t.Fatalf("%s: %s", phase, msg)
			}
		})
	}
}

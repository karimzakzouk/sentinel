package lab

import (
	"fmt"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/stats"
)

// worksAbove is the success rate a candidate must beat to pass, and stay
// below to fail: a fix works more than three times in four.
const worksAbove = 0.75

// Record is one variant's runs, tested against "it works more than three
// times in four" both ways.
type Record struct {
	Runs, Successes int
	works, fails    *stats.EProcess
}

func newRecord() *Record {
	return &Record{works: stats.NewEProcess(worksAbove, true), fails: stats.NewEProcess(worksAbove, false)}
}

func (r *Record) observe(ok bool) {
	r.Runs++
	if ok {
		r.Successes++
	}
	r.works.Observe(ok)
	r.fails.Observe(ok)
}

// Contender is a variant raced against the baseline in paired blocks.
type Contender struct {
	Name string
	*Record
	// better bets over discordant blocks against "no better than the
	// baseline".
	better *stats.EProcess
}

// Race judges contenders against the baseline, the failing revision, from
// paired runs. Controls are contenders that must not pass.
type Race struct {
	Alpha      float64
	Baseline   *Record
	Candidates []*Contender
	Controls   []*Contender
}

// NewRace starts a race of the named candidates and controls at level
// alpha, shared among the candidates.
func NewRace(alpha float64, candidates, controls []string) *Race {
	r := &Race{Alpha: alpha, Baseline: newRecord()}
	for _, n := range candidates {
		r.Candidates = append(r.Candidates, &Contender{Name: n, Record: newRecord(), better: stats.NewEProcess(0.5, true)})
	}
	for _, n := range controls {
		r.Controls = append(r.Controls, &Contender{Name: n, Record: newRecord(), better: stats.NewEProcess(0.5, true)})
	}
	return r
}

// Observe adds one block: the baseline's outcome, and each contender's by
// name.
func (r *Race) Observe(baseline bool, outcomes map[string]bool) {
	r.Baseline.observe(baseline)
	for _, c := range append(append([]*Contender(nil), r.Candidates...), r.Controls...) {
		ok, ran := outcomes[c.Name]
		if !ran {
			continue
		}
		c.observe(ok)
		if ok != baseline {
			c.better.Observe(ok)
		}
	}
}

// level is each test's share of α: candidates share it, and a verdict on
// one needs both its tests to reject, which keeps its error within that
// share without a further split.
func (r *Race) level() float64 {
	return r.Alpha / float64(max(1, len(r.Candidates)))
}

func (r *Race) verdict(c *Contender) v1alpha1.ExperimentPhase {
	switch {
	case c.better.Rejects(r.level()) && c.works.Rejects(r.level()):
		return PhasePassed
	case c.fails.Rejects(r.level()):
		return PhaseFailed
	}
	return ""
}

// Outcome is the race's verdict so far, and whether it is final. A
// candidate passes only once the failure has reproduced and every control
// has failed; without a budget left, an undecided race ends NOT SURE.
func (r *Race) Outcome(budgetLeft bool) (v1alpha1.ExperimentPhase, string, bool) {
	level := r.level()
	failing := fmt.Sprintf("%d of %d runs", r.Baseline.Runs-r.Baseline.Successes, r.Baseline.Runs)
	if r.Baseline.works.Rejects(level) {
		return PhaseNotSure, fmt.Sprintf("the failure did not reproduce: the failing revision worked in %d of %d runs", r.Baseline.Successes, r.Baseline.Runs), true
	}
	controlsFailed := true
	for _, c := range r.Controls {
		switch r.verdict(c) {
		case PhasePassed:
			return PhaseNotSure, fmt.Sprintf("the %s passed, so the Lab cannot tell a fix from it", c.Name), true
		case "":
			controlsFailed = false
		}
	}
	reproduced := r.Baseline.fails.Rejects(level)
	var passed, failed *Contender
	decided := true
	for _, c := range r.Candidates {
		switch r.verdict(c) {
		case PhasePassed:
			if passed == nil {
				passed = c
			}
		case PhaseFailed:
			if failed == nil {
				failed = c
			}
		default:
			decided = false
		}
	}
	switch {
	case len(r.Candidates) == 0 && reproduced:
		return PhasePassed, "the failure reproduces: the failing revision failed in " + failing, true
	case len(r.Candidates) > 0 && reproduced && controlsFailed && decided && passed != nil:
		return PhasePassed, fmt.Sprintf("the %s fixes it: it worked in %d of %d runs where the failing revision failed in %s",
			passed.Name, passed.Successes, passed.Runs, failing), true
	case len(r.Candidates) > 0 && reproduced && controlsFailed && decided:
		return PhaseFailed, fmt.Sprintf("no candidate fixes it: the %s worked in only %d of %d runs", failed.Name, failed.Successes, failed.Runs), true
	case !budgetLeft:
		return PhaseNotSure, fmt.Sprintf("the runs ran out before a verdict: %d blocks", r.Baseline.Runs), true
	}
	return "", "", false
}

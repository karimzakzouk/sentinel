package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// IncidentVersion is one recorded state of an incident.
type IncidentVersion struct {
	At       time.Time
	Verb     string
	Incident model.Incident
}

// ReadIncidents reads a Ledger export (CloudEvents, one per line) and returns
// every incident's recorded versions, oldest first.
func ReadIncidents(r io.Reader) (map[model.ID][]IncidentVersion, error) {
	out := map[model.ID][]IncidentVersion{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type string          `json:"type"`
			Time time.Time       `json:"time"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("ledger export: %w", err)
		}
		verb, ok := strings.CutPrefix(ev.Type, "sentinel.incident.")
		if !ok {
			continue
		}
		var inc model.Incident
		if err := json.Unmarshal(ev.Data, &inc); err != nil {
			return nil, fmt.Errorf("incident in ledger export: %w", err)
		}
		out[inc.ID] = append(out[inc.ID], IncidentVersion{At: ev.Time, Verb: verb, Incident: inc})
	}
	return out, sc.Err()
}

// LoadRuns reads every run record in a directory, oldest first.
func LoadRuns(dir string) ([]*Run, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var runs []*Run
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // the directory comes from the operator
		if err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		runs = append(runs, &r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].InjectedAt.Before(runs[j].InjectedAt) })
	return runs, nil
}

// RunScore is how Sentinel did on one run.
type RunScore struct {
	Run *Run `json:"run"`
	// Detected reports an incident that involved the root cause.
	Detected bool          `json:"detected"`
	Latency  time.Duration `json:"latency,omitempty"`
	Incident model.ID      `json:"incident,omitempty"`
	Class    string        `json:"class,omitempty"`
	// Rank is the root cause's place in the incident's root-cause ranking,
	// from 1; 0 when it is not ranked.
	Rank          int    `json:"rank"`
	ClassRight    bool   `json:"classRight"`
	CulpritRight  bool   `json:"culpritRight"`
	Decision      string `json:"decision,omitempty"`
	DecisionRight bool   `json:"decisionRight"`
	// FalseAlarms counts incidents during a control run.
	FalseAlarms int `json:"falseAlarms,omitempty"`
}

// Report sums up a scoring session.
type Report struct {
	Runs []RunScore `json:"runs"`
	// Incidents counts the incidents opened during the session, merged ones
	// excluded; Matched those that involved a run's root cause.
	Incidents, Matched, FaultRuns, DetectedRuns, ControlFalseAlarms int
	Precision, Recall                                               float64
	AC1, AC3, Avg5                                                  float64
	ClassAccuracy, CulpritAccuracy, DecisionAccuracy                float64
	MedianLatency                                                   time.Duration
}

// ScoreOptions tunes the matching.
type ScoreOptions struct {
	// Grace is how long after a revert an incident still counts toward the
	// run; a run without a revert counts for Grace after its injection.
	Grace time.Duration
}

// Score compares Sentinel's incidents with the runs' ground truth, per event
// and without point adjustment: every incident counts, once.
func Score(runs []*Run, incidents map[model.ID][]IncidentVersion, opts ScoreOptions) Report {
	var rep Report
	if len(runs) == 0 {
		return rep
	}
	window := func(r *Run) (time.Time, time.Time) {
		end := r.InjectedAt.Add(opts.Grace)
		if r.RevertedAt != nil {
			end = r.RevertedAt.Add(opts.Grace)
		}
		return r.InjectedAt, end
	}
	sessionStart, _ := window(runs[0])
	sessionEnd := sessionStart
	for _, r := range runs {
		if _, end := window(r); end.After(sessionEnd) {
			sessionEnd = end
		}
	}
	// Incidents opened in the session that were not folded into another.
	type candidate struct {
		id       model.ID
		openedAt time.Time
		versions []IncidentVersion
		matched  bool
	}
	var cands []*candidate
	for id, vs := range incidents {
		opened := vs[0].At
		if opened.Before(sessionStart) || opened.After(sessionEnd) || vs[len(vs)-1].Incident.MergedInto != "" {
			continue
		}
		cands = append(cands, &candidate{id: id, openedAt: opened, versions: vs})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].openedAt.Before(cands[j].openedAt) })
	rep.Incidents = len(cands)

	var latencies []time.Duration
	for _, r := range runs {
		start, end := window(r)
		s := RunScore{Run: r}
		var inWindow []*candidate
		for _, c := range cands {
			if !c.openedAt.Before(start) && !c.openedAt.After(end) {
				inWindow = append(inWindow, c)
			}
		}
		if r.GroundTruth.FaultClass == "none" || r.FaultKind == FaultNone {
			s.FalseAlarms = len(inWindow)
			rep.ControlFalseAlarms += s.FalseAlarms
			rep.Runs = append(rep.Runs, s)
			continue
		}
		rep.FaultRuns++
		root := r.GroundTruth.RootCause
		for _, c := range inWindow {
			inc := stateAt(c.versions, end)
			rank, involved := rootCauseRank(inc, root)
			if !involved {
				continue
			}
			c.matched = true
			if s.Detected && (rank == 0 || s.Rank != 0 && s.Rank <= rank) {
				continue
			}
			s.Detected, s.Rank, s.Incident = true, rank, inc.ID
			s.Latency = max(c.openedAt.Sub(manifested(r)), 0)
			if a := inc.Assessment; a != nil {
				s.Class, s.Decision = a.Class, a.Decision
				s.ClassRight = a.Class == r.GroundTruth.FaultClass
				s.CulpritRight = a.Culprit != nil && sameObject(a.Culprit.Object, root)
				s.DecisionRight = decisionMatches(a.Decision, r.GroundTruth.Mitigation)
			}
		}
		if s.Detected {
			rep.DetectedRuns++
			latencies = append(latencies, s.Latency)
		}
		rep.Runs = append(rep.Runs, s)
	}
	for _, c := range cands {
		if c.matched {
			rep.Matched++
		}
	}
	if rep.Incidents > 0 {
		rep.Precision = float64(rep.Matched) / float64(rep.Incidents)
	}
	if rep.FaultRuns > 0 {
		n := float64(rep.FaultRuns)
		rep.Recall = float64(rep.DetectedRuns) / n
		var ac [6]float64
		for _, s := range rep.Runs {
			if s.Run.GroundTruth.FaultClass == "none" || s.Run.FaultKind == FaultNone {
				continue
			}
			for k := 1; k <= 5; k++ {
				if s.Rank >= 1 && s.Rank <= k {
					ac[k]++
				}
			}
			rep.ClassAccuracy += boolf(s.ClassRight)
			rep.CulpritAccuracy += boolf(s.CulpritRight)
			rep.DecisionAccuracy += boolf(s.DecisionRight)
		}
		rep.AC1, rep.AC3 = ac[1]/n, ac[3]/n
		rep.Avg5 = (ac[1] + ac[2] + ac[3] + ac[4] + ac[5]) / (5 * n)
		rep.ClassAccuracy /= n
		rep.CulpritAccuracy /= n
		rep.DecisionAccuracy /= n
	}
	if len(latencies) > 0 {
		slices.Sort(latencies)
		rep.MedianLatency = latencies[len(latencies)/2]
	}
	return rep
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// stateAt returns the incident as last recorded at or before t.
func stateAt(vs []IncidentVersion, t time.Time) model.Incident {
	inc := vs[0].Incident
	for _, v := range vs {
		if v.At.After(t) {
			break
		}
		inc = v.Incident
	}
	return inc
}

// rootCauseRank returns the root cause's place in the incident's ranking and
// whether the incident involves it at all: ranked, blamed, or one of its
// workloads. An incident without a ranking ranks its own workload first.
func rootCauseRank(inc model.Incident, root Target) (int, bool) {
	if a := inc.Assessment; a != nil && len(a.RootCauses) > 0 {
		for i, rc := range a.RootCauses {
			if sameObject(rc.Object, root) {
				return i + 1, true
			}
		}
	} else if len(inc.Objects) > 0 && sameObject(inc.Objects[0], root) {
		return 1, true
	}
	if a := inc.Assessment; a != nil && a.Culprit != nil && sameObject(a.Culprit.Object, root) {
		return 0, true
	}
	for _, o := range inc.Objects {
		if sameObject(o, root) {
			return 0, true
		}
	}
	return 0, false
}

func sameObject(o model.ObjectRef, t Target) bool {
	return o.Kind == t.Kind && o.Namespace == t.Namespace && o.Name == t.Name
}

// decisionMatches compares a decision with the mitigation the ground truth
// names.
func decisionMatches(decision, mitigation string) bool {
	if mitigation == "scale" {
		return decision == "scale.restore"
	}
	return decision == mitigation
}

func manifested(r *Run) time.Time {
	if r.ManifestedAt != nil {
		return *r.ManifestedAt
	}
	return r.InjectedAt
}

// WriteMarkdown writes the report as Markdown tables.
func (rep Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("| Scenario | Detected | Latency | Class | Root cause rank | Decision |\n|---|---|---|---|---|---|\n")
	for _, s := range rep.Runs {
		if s.Run.GroundTruth.FaultClass == "none" || s.Run.FaultKind == FaultNone {
			fmt.Fprintf(&b, "| %s (control) | %d false alarm(s) | | | | |\n", s.Run.Scenario, s.FalseAlarms)
			continue
		}
		if !s.Detected {
			fmt.Fprintf(&b, "| %s | no | | | | |\n", s.Run.Scenario)
			continue
		}
		rank := "not ranked"
		if s.Rank > 0 {
			rank = fmt.Sprint(s.Rank)
		}
		fmt.Fprintf(&b, "| %s | yes | %s | %s %s | %s | %s %s |\n", s.Run.Scenario, s.Latency.Round(time.Second),
			s.Class, mark(s.ClassRight), rank, s.Decision, mark(s.DecisionRight))
	}
	fmt.Fprintf(&b, "\n| Measure | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Fault runs detected (recall) | %d of %d (%.2f) |\n", rep.DetectedRuns, rep.FaultRuns, rep.Recall)
	fmt.Fprintf(&b, "| Incidents matching a fault (precision) | %d of %d (%.2f) |\n", rep.Matched, rep.Incidents, rep.Precision)
	fmt.Fprintf(&b, "| Root cause ranked first (AC@1) | %.2f |\n| Root cause in the top three (AC@3) | %.2f |\n| Avg@5 | %.2f |\n",
		rep.AC1, rep.AC3, rep.Avg5)
	fmt.Fprintf(&b, "| Fault class right | %.2f |\n| Culprit change right | %.2f |\n| Decision right | %.2f |\n",
		rep.ClassAccuracy, rep.CulpritAccuracy, rep.DecisionAccuracy)
	fmt.Fprintf(&b, "| Median detection latency | %s |\n| False alarms during controls | %d |\n",
		rep.MedianLatency.Round(time.Second), rep.ControlFalseAlarms)
	_, err := io.WriteString(w, b.String())
	return err
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

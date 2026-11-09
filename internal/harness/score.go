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

// ActionEvent is one step of an action Sentinel took or proposed.
type ActionEvent struct {
	At     time.Time
	Verb   string
	Action model.Action
}

// ReadIncidents reads a Ledger export (CloudEvents, one per line) and returns
// every incident's recorded versions, oldest first.
func ReadIncidents(r io.Reader) (map[model.ID][]IncidentVersion, error) {
	incidents, _, err := ReadExport(r)
	return incidents, err
}

// ReadExport reads a Ledger export and returns every incident's recorded
// versions and every incident's action steps, oldest first.
func ReadExport(r io.Reader) (map[model.ID][]IncidentVersion, map[model.ID][]ActionEvent, error) {
	out, actions := map[model.ID][]IncidentVersion{}, map[model.ID][]ActionEvent{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type string          `json:"type"`
			Time time.Time       `json:"time"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, nil, fmt.Errorf("ledger export: %w", err)
		}
		if verb, ok := strings.CutPrefix(ev.Type, "sentinel.action."); ok {
			var a model.Action
			if err := json.Unmarshal(ev.Data, &a); err != nil {
				return nil, nil, fmt.Errorf("action in ledger export: %w", err)
			}
			actions[a.IncidentID] = append(actions[a.IncidentID], ActionEvent{At: ev.Time, Verb: verb, Action: a})
			continue
		}
		verb, ok := strings.CutPrefix(ev.Type, "sentinel.incident.")
		if !ok {
			continue
		}
		var inc model.Incident
		if err := json.Unmarshal(ev.Data, &inc); err != nil {
			return nil, nil, fmt.Errorf("incident in ledger export: %w", err)
		}
		out[inc.ID] = append(out[inc.ID], IncidentVersion{At: ev.Time, Verb: verb, Incident: inc})
	}
	return out, actions, sc.Err()
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
	// Acted names the action Sentinel carried out on the run's incident, and
	// Verified whether it cleared the incident; Recovery is the time from the
	// fault manifesting to that verification.
	Acted    string        `json:"acted,omitempty"`
	Verified bool          `json:"verified,omitempty"`
	Recovery time.Duration `json:"recovery,omitempty"`
	// WrongActions counts actions carried out that the ground truth does not
	// call for: any in a quiet run, or one other than the mitigation.
	WrongActions int `json:"wrongActions,omitempty"`
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
	// Acting: fault runs Sentinel acted on, those verified, wrong actions,
	// and the median time to verified recovery.
	ActedRuns, VerifiedRuns, WrongActions int
	MedianRecovery                        time.Duration
}

// ScoreOptions tunes the matching.
type ScoreOptions struct {
	// Grace is how long after a revert an incident still counts toward the
	// run; a run without a revert counts for Grace after its injection.
	Grace time.Duration
	// Actions are the incidents' action steps, from ReadExport; without
	// them, acting is not scored.
	Actions map[model.ID][]ActionEvent
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
		id         model.ID
		openedAt   time.Time
		versions   []IncidentVersion
		matched    bool
		falseAlarm bool
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

	var latencies, recoveries []time.Duration
	inWindow := func(r *Run) []*candidate {
		start, end := window(r)
		var out []*candidate
		for _, c := range cands {
			if !c.openedAt.Before(start) && !c.openedAt.After(end) {
				out = append(out, c)
			}
		}
		return out
	}
	rep.Runs = make([]RunScore, len(runs))
	// Fault runs claim their incidents first, so an incident that a fault
	// explains is never a neighboring quiet run's false alarm.
	for i, r := range runs {
		if r.Quiet() {
			continue
		}
		_, end := window(r)
		s := RunScore{Run: r}
		rep.FaultRuns++
		root := r.GroundTruth.RootCause
		for _, c := range inWindow(r) {
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
			s.scoreAction(opts.Actions[s.Incident], end)
			if s.Acted != "" {
				rep.ActedRuns++
			}
			if s.Verified {
				rep.VerifiedRuns++
				recoveries = append(recoveries, s.Recovery)
			}
		}
		rep.WrongActions += s.WrongActions
		rep.Runs[i] = s
	}
	for i, r := range runs {
		if !r.Quiet() {
			continue
		}
		_, end := window(r)
		s := RunScore{Run: r}
		for _, c := range inWindow(r) {
			if !c.matched && !c.falseAlarm {
				c.falseAlarm = true
				s.FalseAlarms++
				s.WrongActions += len(executed(opts.Actions[c.id], end))
			}
		}
		rep.ControlFalseAlarms += s.FalseAlarms
		rep.WrongActions += s.WrongActions
		rep.Runs[i] = s
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
			if s.Run.Quiet() {
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
	if len(recoveries) > 0 {
		slices.Sort(recoveries)
		rep.MedianRecovery = recoveries[len(recoveries)/2]
	}
	return rep
}

// executed returns the actions carried out by end.
func executed(events []ActionEvent, end time.Time) []ActionEvent {
	var out []ActionEvent
	for _, e := range events {
		if e.Verb == "executed" && !e.At.After(end) {
			out = append(out, e)
		}
	}
	return out
}

// scoreAction scores what Sentinel did about a detected fault by end: the
// action it carried out, whether it was the mitigation the ground truth
// names, and when it was verified.
func (s *RunScore) scoreAction(events []ActionEvent, end time.Time) {
	for _, e := range executed(events, end) {
		s.Acted = e.Action.Type
		if !decisionMatches(e.Action.Type, s.Run.GroundTruth.Mitigation) {
			s.WrongActions++
		}
	}
	for _, e := range events {
		if e.Verb == "verified" && !e.At.After(end) && s.Acted != "" {
			s.Verified, s.Recovery = true, max(e.At.Sub(manifested(s.Run)), 0)
		}
	}
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

// WriteMarkdown writes the report as Markdown tables. Acting columns and
// measures appear when Sentinel carried out actions in the session.
func (rep Report) WriteMarkdown(w io.Writer) error {
	acting := rep.ActedRuns > 0 || rep.WrongActions > 0
	var b strings.Builder
	b.WriteString("| Scenario | Detected | Latency | Class | Root cause rank | Decision |")
	if acting {
		b.WriteString(" Acted |")
	}
	b.WriteString("\n|---|---|---|---|---|---|")
	if acting {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, s := range rep.Runs {
		var row string
		switch {
		case s.Run.Quiet():
			kind := "control"
			if s.Run.FaultKind != FaultNone && s.Run.GroundTruth.FaultClass != "none" {
				kind = "heals itself"
			}
			row = fmt.Sprintf("| %s (%s) | %d false alarm(s) | | | | |", s.Run.Scenario, kind, s.FalseAlarms)
		case !s.Detected:
			row = fmt.Sprintf("| %s | no | | | | |", s.Run.Scenario)
		default:
			rank := "not ranked"
			if s.Rank > 0 {
				rank = fmt.Sprint(s.Rank)
			}
			row = fmt.Sprintf("| %s | yes | %s | %s %s | %s | %s %s |", s.Run.Scenario, s.Latency.Round(time.Second),
				s.Class, mark(s.ClassRight), rank, s.Decision, mark(s.DecisionRight))
		}
		b.WriteString(row)
		if acting {
			b.WriteString(" " + s.actedCell() + " |")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n| Measure | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Fault runs detected (recall) | %d of %d (%.2f) |\n", rep.DetectedRuns, rep.FaultRuns, rep.Recall)
	fmt.Fprintf(&b, "| Incidents matching a fault (precision) | %d of %d (%.2f) |\n", rep.Matched, rep.Incidents, rep.Precision)
	fmt.Fprintf(&b, "| Root cause ranked first (AC@1) | %.2f |\n| Root cause in the top three (AC@3) | %.2f |\n| Avg@5 | %.2f |\n",
		rep.AC1, rep.AC3, rep.Avg5)
	fmt.Fprintf(&b, "| Fault class right | %.2f |\n| Culprit change right | %.2f |\n| Decision right | %.2f |\n",
		rep.ClassAccuracy, rep.CulpritAccuracy, rep.DecisionAccuracy)
	fmt.Fprintf(&b, "| Median detection latency | %s |\n| False alarms in quiet runs (controls, faults that heal themselves) | %d |\n",
		rep.MedianLatency.Round(time.Second), rep.ControlFalseAlarms)
	if acting {
		fmt.Fprintf(&b, "| Fault runs Sentinel acted on | %d of %d |\n| Actions verified | %d of %d |\n", rep.ActedRuns, rep.FaultRuns,
			rep.VerifiedRuns, rep.ActedRuns)
		fmt.Fprintf(&b, "| Median time from fault to verified recovery | %s |\n| Wrong actions | %d |\n",
			rep.MedianRecovery.Round(time.Second), rep.WrongActions)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// actedCell says what Sentinel did about a run.
func (s RunScore) actedCell() string {
	switch {
	case s.WrongActions > 0:
		return fmt.Sprintf("%d wrong action(s) ✗", s.WrongActions)
	case s.Acted == "":
		return ""
	case s.Verified:
		return fmt.Sprintf("%s, verified after %s ✓", s.Acted, s.Recovery.Round(time.Second))
	default:
		return s.Acted + ", not verified"
	}
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

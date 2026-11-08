package brain

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

func newBrain(t *testing.T) (*Brain, *ledger.Ledger) {
	t.Helper()
	l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	rec := slot.LedgerRecorder{Ledger: l, Source: "/test"}
	classify, _ := slot.New[ClassifyInput, slot.Distribution[string]](slot.BrainClassify, NaiveBayes{}, nil, rec)
	attribute, _ := slot.New[AttributeInput, slot.Ranking[RankedChange]](slot.BrainAttribute, LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}, nil, rec)
	decide, _ := slot.New[DecideInput, Decision](slot.BrainDecide, ExpectedLoss{Impact: 60, Escalation: 10, Slower: 15, Disruption: 5, PReal: 0.95}, nil, rec)
	b := &Brain{Ledger: l, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Classify: classify, Attribute: attribute, Decide: decide,
		Revisions: func(model.ObjectRef) string { return "new" }, Lookback: time.Hour, Gap: 5 * time.Minute,
		Now: func() time.Time { return onset.Add(time.Minute) }}
	return b, l
}

func TestBadImageRolloutBecomesAnExplainedIncident(t *testing.T) {
	b, l := newBrain(t)
	change := watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"image:web"}, At: onset.Add(-90 * time.Second), Actor: "kubectl"}
	if err := b.OnChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactImagePullError, Count: 6, TailProbability: 1e-9, Alpha: 3e-6,
		Onset: onset, At: onset.Add(30 * time.Second), Revisions: map[string]int{"new": 6}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}

	incidents, _ := ledger.List[model.Incident](ctx, l, model.KindIncident, "")
	if len(incidents) != 1 || incidents[0].State != model.IncidentOpen {
		t.Fatalf("incidents = %+v", incidents)
	}
	id := incidents[0].ID
	hyps, _ := ledger.List[model.Hypothesis](ctx, l, model.KindHypothesis, id)
	var top model.Hypothesis
	for _, h := range hyps {
		if h.Posterior > top.Posterior {
			top = h
		}
	}
	if top.FaultClass != ClassBadImage || len(top.Ratios) == 0 {
		t.Fatalf("top hypothesis = %+v", top)
	}
	actions, _ := ledger.List[model.Action](ctx, l, model.KindAction, id)
	if len(actions) != 1 || actions[0].Type != ActionRolloutUndo || !actions[0].Shadow || actions[0].Target != web {
		t.Fatalf("actions = %+v; want one shadow rollback of web", actions)
	}
	for _, name := range []string{slot.BrainClassify, slot.BrainAttribute, slot.BrainDecide} {
		if recs, _ := slot.Records(ctx, l, name); len(recs) != 1 {
			t.Errorf("slot %s recorded %d decisions, want 1", name, len(recs))
		}
	}

	end := det
	end.Ended = true
	if err := b.OnDetection(ctx, end); err != nil {
		t.Fatal(err)
	}
	var got model.Incident
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _ = l.Get(ctx, model.KindIncident, id, &got); got.State != model.IncidentOpen {
		t.Fatalf("the incident is %s right after its signal went quiet; want it open for the gap", got.State)
	}
	b.Now = func() time.Time { return onset.Add(time.Minute + b.Gap) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, model.KindIncident, id, &got); err != nil || got.State != model.IncidentResolved {
		t.Fatalf("after the gap the incident is %s, want resolved (%v)", got.State, err)
	}
	if n, err := l.Verify(ctx); err != nil || n == 0 {
		t.Fatalf("ledger verify = %d, %v", n, err)
	}
}

func topClass(t *testing.T, l *ledger.Ledger) model.Hypothesis {
	t.Helper()
	hyps, err := ledger.List[model.Hypothesis](ctx, l, model.KindHypothesis, "")
	if err != nil {
		t.Fatal(err)
	}
	var top model.Hypothesis
	for _, h := range hyps {
		if h.Posterior > top.Posterior {
			top = h
		}
	}
	return top
}

// One OOM kill is not significant on its own, but it says what kind of crash
// loop this is.
func TestFactsBelowSignificanceInformTheClass(t *testing.T) {
	b, l := newBrain(t)
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 3, Onset: onset, At: onset,
		Window: map[watcher.FactKind]int{watcher.FactCrashLoop: 3, watcher.FactOOMKilled: 1}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	if top := topClass(t, l); top.FaultClass != ClassMemoryLimit {
		t.Fatalf("top hypothesis = %s (%.2f), want %s", top.FaultClass, top.Posterior, ClassMemoryLimit)
	}
}

// The image change and its revert behind an earlier, resolved incident must
// not make a later crash loop look like an image problem.
func TestChangesBeforeTheLastRecoveryDoNotShapeTheClass(t *testing.T) {
	b, l := newBrain(t)
	earlier := onset.Add(-20 * time.Minute)
	for _, c := range []watcher.Change{
		{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"image:web"}, At: earlier},
		{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"image:web"}, At: earlier.Add(5 * time.Minute)},
	} {
		if err := b.OnChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	first := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactImagePullError, Count: 4, Onset: earlier.Add(time.Minute), At: earlier.Add(time.Minute)}
	b.Now = func() time.Time { return earlier.Add(time.Minute) }
	if err := b.OnDetection(ctx, first); err != nil {
		t.Fatal(err)
	}
	ended := first
	ended.Ended = true
	b.Now = func() time.Time { return earlier.Add(6 * time.Minute) }
	if err := b.OnDetection(ctx, ended); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return earlier.Add(6*time.Minute + b.Gap) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"args:web"}, At: onset.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset.Add(time.Minute) }
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 3, Onset: onset, At: onset,
		Window: map[watcher.FactKind]int{watcher.FactCrashLoop: 3, watcher.FactRestarted: 2}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	recs, err := slot.Records(ctx, l, slot.BrainClassify)
	if err != nil || len(recs) != 2 {
		t.Fatalf("classify records = %d, %v; want 2", len(recs), err)
	}
	var in ClassifyInput
	if err := json.Unmarshal(recs[1].Input, &in); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in.ChangeFields, []string{"config"}) {
		t.Fatalf("change fields = %v, want only the change since the last recovery", in.ChangeFields)
	}
}

func logEvidence(pod string, at time.Time) watcher.LogEvidence {
	return watcher.LogEvidence{Workload: web, Pod: pod, Container: "web", Previous: true, Reason: "crash", CollectedAt: at,
		Summary: logs.Summary{Lines: 3, SHA256: "abc", Templates: []logs.TemplateCount{
			{Template: "Error: unknown flag: --no-such-flag", Count: 1, Error: true, Sample: "Error: unknown flag: --no-such-flag"},
		}}}
}

func TestLogEvidenceJoinsTheWorkloadsIncident(t *testing.T) {
	b, l := newBrain(t)
	b.Now = func() time.Time { return onset }
	if err := b.OnEvidence(ctx, logEvidence("web-old", onset.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := b.OnEvidence(ctx, logEvidence("web-1", onset.Add(-30*time.Second))); err != nil {
		t.Fatal(err)
	}
	if ev, _ := ledger.List[model.Evidence](ctx, l, model.KindEvidence, ""); len(ev) != 0 {
		t.Fatalf("evidence reached the Ledger without an incident: %+v", ev)
	}
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 3, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	during := logEvidence("web-2", onset.Add(time.Minute))
	during.Summary.Templates[0].Count = 2
	if err := b.OnEvidence(ctx, during); err != nil {
		t.Fatal(err)
	}

	evidence, _ := ledger.List[model.Evidence](ctx, l, model.KindEvidence, "")
	var pods []string
	for _, e := range evidence {
		pods = append(pods, e.Object.Name)
		if e.Trust != model.TrustUntrusted || !e.Redacted || !strings.Contains(e.Summary, "unknown flag") {
			t.Errorf("evidence = %+v", e)
		}
	}
	if fmt.Sprint(pods) != "[web-1 web-2]" {
		t.Fatalf("evidence from %v, want web-1 (just before the incident) and web-2 (during it)", pods)
	}
	incidents, _ := ledger.List[model.Incident](ctx, l, model.KindIncident, "")
	if len(incidents) != 1 || len(incidents[0].EvidenceIDs) != 2 {
		t.Fatalf("incident = %+v", incidents)
	}
}

// A crash loop prints the same lines at every crash, and a rollout sample of
// a quiet pod says nothing; neither belongs in the incident twice or at all.
func TestOnlyNewInformativeEvidenceIsRecorded(t *testing.T) {
	b, l := newBrain(t)
	b.Now = func() time.Time { return onset }
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 3, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	quiet := watcher.LogEvidence{Workload: web, Pod: "web-0", Container: "web", Reason: "rollout-before", CollectedAt: onset}
	for _, ev := range []watcher.LogEvidence{logEvidence("web-1", onset), logEvidence("web-1", onset.Add(time.Minute)),
		logEvidence("web-2", onset.Add(2*time.Minute)), quiet} {
		if err := b.OnEvidence(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if ev, _ := ledger.List[model.Evidence](ctx, l, model.KindEvidence, ""); len(ev) != 1 {
		t.Fatalf("evidence recorded = %d, want the crash logs once", len(ev))
	}
}

func TestAnIncidentNamesTheWorkloadsThatDependOnIt(t *testing.T) {
	b, l := newBrain(t)
	front := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "frontend"}
	b.Dependents = func(ref model.ObjectRef) []model.ObjectRef {
		if ref == web {
			return []model.ObjectRef{front}
		}
		return nil
	}
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactNoEndpoints, Count: 3, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	incidents, _ := ledger.List[model.Incident](ctx, l, model.KindIncident, "")
	if len(incidents) != 1 || len(incidents[0].Impacted) != 1 || incidents[0].Impacted[0] != front {
		t.Fatalf("incident = %+v", incidents)
	}
}

func TestAnUnexplainedCrashLoopIsEscalatedNotRolledBack(t *testing.T) {
	b, l := newBrain(t)
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 5, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	actions, _ := ledger.List[model.Action](ctx, l, model.KindAction, "")
	if len(actions) != 1 || actions[0].Type != ActionEscalate {
		t.Fatalf("actions = %+v; want one escalation", actions)
	}
}

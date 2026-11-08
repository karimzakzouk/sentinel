package brain

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
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

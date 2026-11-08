package brain

import (
	"context"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
)

var (
	ctx   = context.Background()
	web   = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	onset = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
)

func classify(t *testing.T, facts map[string]int, changes ...string) slot.Distribution[string] {
	t.Helper()
	d, err := NaiveBayes{}.Decide(ctx, ClassifyInput{Facts: facts, ChangeFields: changes})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("invalid distribution: %v", err)
	}
	return d
}

func TestClassifierPicksTheMatchingClass(t *testing.T) {
	cases := []struct {
		facts   map[string]int
		changes []string
		want    string
	}{
		{map[string]int{"pod.image-pull-error": 12}, []string{"image"}, ClassBadImage},
		{map[string]int{"pod.oom-killed": 4, "pod.crash-loop": 8}, []string{"resources"}, ClassMemoryLimit},
		{map[string]int{"pod.crash-loop": 9, "pod.restarted": 3}, []string{"config"}, ClassCrashOnStart},
		{map[string]int{"pod.not-ready": 20, "pod.probe-failed": 15}, []string{"probes"}, ClassProbe},
		{map[string]int{"pod.config-error": 10}, []string{"config"}, ClassConfigError},
		{map[string]int{"pod.unschedulable": 10}, []string{"scale"}, ClassUnschedulable},
		{map[string]int{"pod.crash-loop": 6, "pod.restarted": 6}, nil, ClassRuntimeCrash},
		{map[string]int{"log.new-error": 40}, []string{"config"}, ClassNewErrors},
		{map[string]int{"log.new-error": 40}, []string{"image"}, ClassNewErrors},
		{map[string]int{"service.no-endpoints": 12}, []string{"scale"}, ClassDependencyUnavailable},
		// Crashing pods also empty a Service; the crash still names the class.
		{map[string]int{"pod.crash-loop": 9, "pod.restarted": 3, "service.no-endpoints": 6}, []string{"config"}, ClassCrashOnStart},
	}
	for _, tc := range cases {
		if got := classify(t, tc.facts, tc.changes...).Top().Value; got != tc.want {
			t.Errorf("facts %v with changes %v: top class %s, want %s", tc.facts, tc.changes, got, tc.want)
		}
	}
}

func TestAttributionPrefersARelevantTimelyChange(t *testing.T) {
	classes := classify(t, map[string]int{"pod.image-pull-error": 10}, "image", "config")
	in := AttributeInput{
		Onset: onset, Classes: classes, NewRevisionShare: 1,
		Candidates: []CandidateChange{
			{ID: "cm", Kind: "configmap", Object: model.ObjectRef{Kind: "ConfigMap", Namespace: "shop", Name: "web-config"}, Fields: []string{"key:mode"}, At: onset.Add(-50 * time.Minute)},
			{ID: "img", Kind: "template", Object: web, Fields: []string{"image:web"}, At: onset.Add(-2 * time.Minute), ProducedNewestRevision: true},
			{ID: "late", Kind: "template", Object: web, Fields: []string{"image:web"}, At: onset.Add(5 * time.Minute)},
		},
	}
	r, err := LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}.Decide(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid ranking: %v", err)
	}
	if top := r.Items[0].Item; top.Change.ID != "img" || top.P < 0.8 || !r.Items[0].InSet {
		t.Fatalf("top = %+v (p %.3f)", top.Change, top.P)
	}
	for _, it := range r.Items {
		if it.Item.Change.ID == "late" && it.Item.P > 0.01 {
			t.Fatalf("a change after the onset got p = %.3f", it.Item.P)
		}
	}
	if len(r.Items[0].Item.Ratios) != 3 {
		t.Fatalf("a template change should carry three readable ratios, got %+v", r.Items[0].Item.Ratios)
	}
}

// A revert minutes earlier is also a template change; only the latest change
// produced the revision the failing pods run, so it must keep the blame.
func TestTheChangeThatProducedTheFailingRevisionWins(t *testing.T) {
	classes := classify(t, map[string]int{"pod.crash-loop": 8, "pod.restarted": 4}, "config", "resources")
	in := AttributeInput{Onset: onset, Classes: classes, NewRevisionShare: 1, Candidates: []CandidateChange{
		{ID: "revert", Kind: "template", Object: web, Fields: []string{"resources:web"}, At: onset.Add(-3 * time.Minute)},
		{ID: "args", Kind: "template", Object: web, Fields: []string{"args:web"}, At: onset.Add(-20 * time.Second), ProducedNewestRevision: true},
	}}
	r, err := LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}.Decide(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if top := r.Items[0].Item; top.Change.ID != "args" || top.P < 0.8 {
		t.Fatalf("top = %s with p %.3f; want the args change above 0.8", top.Change.ID, top.P)
	}
}

func TestAttributionWithNoCandidatesIsEmpty(t *testing.T) {
	r, err := LikelihoodRatios{Lookback: time.Hour, Typical: 5 * time.Minute}.Decide(ctx, AttributeInput{Onset: onset, NewRevisionShare: -1})
	if err != nil || len(r.Items) != 0 || r.Validate() != nil {
		t.Fatalf("ranking = %+v, %v", r, err)
	}
}

func TestExpectedLossDecisions(t *testing.T) {
	e := ExpectedLoss{Impact: 60, Escalation: 10, Slower: 15, Disruption: 5, PReal: 0.95}
	badImage := classify(t, map[string]int{"pod.image-pull-error": 10}, "image")
	culprit := &RankedChange{Change: CandidateChange{Kind: "template", Object: web}, P: 0.9}
	d, err := e.Decide(ctx, DecideInput{Workload: web, Classes: badImage, TopChange: culprit})
	if err != nil || d.Validate() != nil || d.Action != ActionRolloutUndo {
		t.Fatalf("decision = %+v, %v; want a rollback", d, err)
	}
	unexplained := classify(t, map[string]int{"pod.crash-loop": 6, "pod.restarted": 6})
	d, _ = e.Decide(ctx, DecideInput{Workload: web, Classes: unexplained})
	if d.Action != ActionEscalate {
		t.Fatalf("with no change to undo, decision = %+v; want escalate", d)
	}
	other := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "api"}
	d, _ = e.Decide(ctx, DecideInput{Workload: web, Classes: badImage, TopChange: &RankedChange{Change: CandidateChange{Kind: "template", Object: other}, P: 0.9}})
	if d.Action == ActionRolloutUndo {
		t.Fatal("rolled back a workload other than the one failing")
	}
	// A rollback would not bring back the replicas a scale change removed.
	gone := classify(t, map[string]int{"service.no-endpoints": 12}, "scale")
	d, _ = e.Decide(ctx, DecideInput{Workload: web, Classes: gone, TopChange: &RankedChange{Change: CandidateChange{Kind: "scale", Object: web}, P: 0.9}})
	if d.Validate() != nil || d.Action != ActionScaleRestore {
		t.Fatalf("decision = %+v; want the replicas restored", d)
	}
}

func TestFieldGroup(t *testing.T) {
	cases := map[[2]string]string{
		{"template", "image:web"}:     "image",
		{"template", "init:image:x"}:  "image",
		{"template", "resources:web"}: "resources",
		{"template", "probes:web"}:    "probes",
		{"template", "env:web"}:       "config",
		{"template", "restart"}:       "other",
		{"configmap", "key:mode"}:     "config",
		{"scale", "replicas:1->0"}:    "scale",
	}
	for in, want := range cases {
		if got := FieldGroup(in[0], in[1]); got != want {
			t.Errorf("FieldGroup(%q, %q) = %s, want %s", in[0], in[1], got, want)
		}
	}
}

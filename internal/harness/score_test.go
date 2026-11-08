package harness

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

var t0 = time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)

func ref(name string) model.ObjectRef {
	return model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: name}
}

func target(name string) Target {
	return Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "shop", Name: name}
}

func run(scenario string, at time.Duration, gt GroundTruth, kind FaultKind) *Run {
	injected := t0.Add(at)
	manifested, reverted := injected.Add(20*time.Second), injected.Add(80*time.Second)
	return &Run{Scenario: scenario, FaultKind: kind, InjectedAt: injected, ManifestedAt: &manifested, RevertedAt: &reverted, GroundTruth: gt}
}

// export writes incident events as a Ledger export.
func export(t *testing.T, events ...IncidentVersion) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	for _, ev := range events {
		line, err := json.Marshal(map[string]any{"type": "sentinel.incident." + ev.Verb, "time": ev.At, "data": ev.Incident})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(append(line, '\n'))
	}
	b.WriteString(`{"type":"sentinel.signal.observed","time":"2026-11-08T20:00:01Z","data":{}}` + "\n")
	return &b
}

func incident(id string, objects []model.ObjectRef, a *model.Assessment) model.Incident {
	return model.Incident{ID: model.ID(id), Objects: objects, Assessment: a}
}

func TestScore(t *testing.T) {
	runs := []*Run{
		run("bad-image", 0, GroundTruth{FaultClass: "deploy.bad-image", RootCause: target("web"), Mitigation: "rollout.undo"}, FaultPatch),
		run("redis-down", 10*time.Minute, GroundTruth{FaultClass: "dependency.unavailable", RootCause: target("redis"), Mitigation: "scale"}, FaultPatch),
		run("control", 20*time.Minute, GroundTruth{FaultClass: "none"}, FaultNone),
		run("missed", 30*time.Minute, GroundTruth{FaultClass: "deploy.crash-on-start", RootCause: target("api")}, FaultPatch),
	}
	right := &model.Assessment{Class: "deploy.bad-image", Culprit: &model.Culprit{Object: ref("web")}, Decision: "rollout.undo",
		RootCauses: []model.RankedObject{{Object: ref("web"), P: 0.99, InSet: true}}}
	redis := &model.Assessment{Class: "dependency.unavailable", Culprit: &model.Culprit{Object: ref("redis")}, Decision: "scale.restore",
		RootCauses: []model.RankedObject{{Object: ref("cart"), P: 0.6}, {Object: ref("redis"), P: 0.4}}}
	ledger := export(t,
		IncidentVersion{At: t0.Add(30 * time.Second), Verb: "opened", Incident: incident("i1", []model.ObjectRef{ref("web")}, right)},
		IncidentVersion{At: t0.Add(40 * time.Second), Verb: "opened", Incident: incident("i5", []model.ObjectRef{ref("unrelated")}, nil)},
		IncidentVersion{At: t0.Add(10*time.Minute + 15*time.Second), Verb: "opened", Incident: incident("i2", []model.ObjectRef{ref("cart")}, redis)},
		IncidentVersion{At: t0.Add(10*time.Minute + 5*time.Second), Verb: "opened", Incident: incident("i4", []model.ObjectRef{ref("cart")}, nil)},
		IncidentVersion{At: t0.Add(10*time.Minute + 15*time.Second), Verb: "merged",
			Incident: model.Incident{ID: "i4", Objects: []model.ObjectRef{ref("cart")}, MergedInto: "i2"}},
		IncidentVersion{At: t0.Add(20*time.Minute + 30*time.Second), Verb: "opened", Incident: incident("i3", []model.ObjectRef{ref("web")}, nil)},
		// Opened before the session: not counted.
		IncidentVersion{At: t0.Add(-time.Hour), Verb: "opened", Incident: incident("i0", []model.ObjectRef{ref("web")}, nil)},
	)
	incidents, err := ReadIncidents(ledger)
	if err != nil {
		t.Fatal(err)
	}
	rep := Score(runs, incidents, ScoreOptions{Grace: 2 * time.Minute})

	if rep.FaultRuns != 3 || rep.DetectedRuns != 2 || rep.Incidents != 4 || rep.Matched != 2 || rep.ControlFalseAlarms != 1 {
		t.Fatalf("counts: %d fault runs, %d detected, %d incidents, %d matched, %d control false alarms",
			rep.FaultRuns, rep.DetectedRuns, rep.Incidents, rep.Matched, rep.ControlFalseAlarms)
	}
	if rep.Precision != 0.5 || rep.Recall != 2.0/3 {
		t.Fatalf("precision %v, recall %v", rep.Precision, rep.Recall)
	}
	// web ranked first; redis second; the missed run ranks nothing.
	if rep.AC1 != 1.0/3 || rep.AC3 != 2.0/3 || rep.ClassAccuracy != 2.0/3 || rep.DecisionAccuracy != 2.0/3 || rep.CulpritAccuracy != 2.0/3 {
		t.Fatalf("AC@1 %v, AC@3 %v, class %v, decision %v, culprit %v", rep.AC1, rep.AC3, rep.ClassAccuracy, rep.DecisionAccuracy, rep.CulpritAccuracy)
	}
	if rep.Runs[0].Latency != 10*time.Second || rep.Runs[1].Rank != 2 || rep.MedianLatency == 0 {
		t.Fatalf("runs = %+v", rep.Runs[:2])
	}
	var md strings.Builder
	if err := rep.WriteMarkdown(&md); err != nil || !strings.Contains(md.String(), "| missed | no |") ||
		!strings.Contains(md.String(), "control (control) | 1 false alarm(s)") {
		t.Fatalf("markdown:\n%s", md.String())
	}
}

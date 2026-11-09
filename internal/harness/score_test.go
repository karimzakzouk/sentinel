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

// With actions, the scorer says what Sentinel did: the right action,
// verified, counts toward recovery; any other, or any in a quiet run, is a
// wrong action.
func TestScoreActions(t *testing.T) {
	badImage := run("bad-image", 0, GroundTruth{FaultClass: "deploy.bad-image", RootCause: target("web"), Mitigation: "rollout.undo"}, FaultPatch)
	redis := run("redis-down", 10*time.Minute, GroundTruth{FaultClass: "dependency.unavailable", RootCause: target("redis"), Mitigation: "scale"}, FaultPatch)
	control := run("control", 20*time.Minute, GroundTruth{FaultClass: "none", Mitigation: "none"}, FaultNone)
	ranked := func(name string) *model.Assessment {
		return &model.Assessment{RootCauses: []model.RankedObject{{Object: ref(name), P: 0.9, InSet: true}}}
	}
	incidents, err := ReadIncidents(export(t,
		IncidentVersion{At: t0.Add(30 * time.Second), Verb: "opened", Incident: incident("i1", []model.ObjectRef{ref("web")}, ranked("web"))},
		IncidentVersion{At: t0.Add(10*time.Minute + 30*time.Second), Verb: "opened", Incident: incident("i2", []model.ObjectRef{ref("redis")}, ranked("redis"))},
		IncidentVersion{At: t0.Add(20*time.Minute + 30*time.Second), Verb: "opened", Incident: incident("i3", []model.ObjectRef{ref("web")}, nil)},
	))
	if err != nil {
		t.Fatal(err)
	}
	step := func(at time.Duration, verb, typ string) ActionEvent {
		return ActionEvent{At: t0.Add(at), Verb: verb, Action: model.Action{Type: typ}}
	}
	actions := map[model.ID][]ActionEvent{
		"i1": {step(31*time.Second, "requested", "rollout.undo"), step(32*time.Second, "executed", "rollout.undo"),
			step(80*time.Second, "verified", "rollout.undo")},
		"i2": {step(10*time.Minute+31*time.Second, "executed", "rollout.undo")},
		"i3": {step(20*time.Minute+31*time.Second, "executed", "rollout.undo")},
	}
	rep := Score([]*Run{badImage, redis, control}, incidents, ScoreOptions{Grace: 2 * time.Minute, Actions: actions})
	if rep.ActedRuns != 2 || rep.VerifiedRuns != 1 || rep.WrongActions != 2 || rep.MedianRecovery != time.Minute {
		t.Fatalf("acted %d, verified %d, wrong %d, recovery %v; want 2, 1, 2 (the rollback for Redis and the one in the control), 1m",
			rep.ActedRuns, rep.VerifiedRuns, rep.WrongActions, rep.MedianRecovery)
	}
	var md strings.Builder
	if err := rep.WriteMarkdown(&md); err != nil || !strings.Contains(md.String(), "rollout.undo, verified after 1m0s ✓") ||
		!strings.Contains(md.String(), "| Wrong actions | 2 |") {
		t.Fatalf("markdown:\n%s", md.String())
	}
}

// With fixes, the scorer says what became of each fault delivered through
// Git: a fix at the line the ground truth names that held, a fix elsewhere
// that did not, and an abstention where abstaining was right.
func TestScoreFixes(t *testing.T) {
	gt := func(fix FixTruth) GroundTruth {
		return GroundTruth{FaultClass: "deploy.bad-image", RootCause: target("web"), Mitigation: "rollout.undo", Fix: &fix}
	}
	runs := []*Run{
		run("held", 0, gt(FixTruth{Path: "deployment.yaml", Line: 17}), FaultCommit),
		run("elsewhere", 10*time.Minute, gt(FixTruth{Path: "deployment.yaml", Line: 17}), FaultCommit),
		run("on-the-owner", 20*time.Minute, gt(FixTruth{Abstain: true}), FaultCommit),
	}
	ranked := &model.Assessment{RootCauses: []model.RankedObject{{Object: ref("web"), P: 0.9, InSet: true}}}
	incidents, err := ReadIncidents(export(t,
		IncidentVersion{At: t0.Add(30 * time.Second), Verb: "opened", Incident: incident("i1", []model.ObjectRef{ref("web")}, ranked)},
		IncidentVersion{At: t0.Add(10*time.Minute + 30*time.Second), Verb: "opened", Incident: incident("i2", []model.ObjectRef{ref("web")}, ranked)},
		IncidentVersion{At: t0.Add(20*time.Minute + 30*time.Second), Verb: "opened", Incident: incident("i3", []model.ObjectRef{ref("web")}, ranked)},
	))
	if err != nil {
		t.Fatal(err)
	}
	step := func(phase, change string) model.Fix {
		f := model.Fix{Phase: phase}
		if change != "" {
			f.Change = []string{change}
		}
		return f
	}
	fixes := map[model.ID][]model.Fix{
		"i1": {step("Requested", ""), step("Proposed", "deployment.yaml:17: web's image a -> b"), step("Applied", "deployment.yaml:17: web's image a -> b"),
			step("Held", "deployment.yaml:17: web's image a -> b")},
		"i2": {step("Proposed", "kustomization.yaml:4: web's image a -> b"), step("NotHeld", "kustomization.yaml:4: web's image a -> b")},
		"i3": {step("Requested", ""), step("Abstained", "")},
	}
	rep := Score(runs, incidents, ScoreOptions{Grace: 2 * time.Minute, Fixes: fixes})
	if rep.FixRuns != 3 || rep.FixesProposed != 2 || rep.FixesRight != 1 || rep.FixesHeld != 1 || rep.FixesNotHeld != 1 ||
		rep.RightAbstentions != 1 || rep.WrongAbstentions != 0 {
		t.Fatalf("fix runs %d, proposed %d, right %d, held %d, not held %d, right abstentions %d, wrong %d",
			rep.FixRuns, rep.FixesProposed, rep.FixesRight, rep.FixesHeld, rep.FixesNotHeld, rep.RightAbstentions, rep.WrongAbstentions)
	}
	var md strings.Builder
	if err := rep.WriteMarkdown(&md); err != nil || !strings.Contains(md.String(), "held at deployment.yaml:17 ✓") ||
		!strings.Contains(md.String(), "notheld at kustomization.yaml:4 ✗") || !strings.Contains(md.String(), "| Fixes that held once merged | 1 of 2 |") {
		t.Fatalf("markdown:\n%s", md.String())
	}
}

// Runs follow each other closely: the next fault's incident opens within a
// control's grace period, and belongs to the fault. A fault that heals
// itself, like a deleted pod its Deployment replaces, is quiet when right.
func TestQuietRunsNeighborsAndSelfHealing(t *testing.T) {
	control := run("control", 0, GroundTruth{FaultClass: "none", Mitigation: "none"}, FaultNone)
	next := run("bad-image", 81*time.Second, GroundTruth{FaultClass: "deploy.bad-image", RootCause: target("web"),
		Mitigation: "rollout.undo"}, FaultPatch)
	podKill := run("pod-kill", 10*time.Minute, GroundTruth{FaultClass: "infra.pod-kill", Mitigation: "none"}, FaultDeletePods)
	right := &model.Assessment{Class: "deploy.bad-image", Decision: "rollout.undo",
		RootCauses: []model.RankedObject{{Object: ref("web"), P: 0.99, InSet: true}}}
	incidents, err := ReadIncidents(export(t,
		IncidentVersion{At: t0.Add(90 * time.Second), Verb: "opened", Incident: incident("i1", []model.ObjectRef{ref("web")}, right)}))
	if err != nil {
		t.Fatal(err)
	}
	rep := Score([]*Run{control, next, podKill}, incidents, ScoreOptions{Grace: 2 * time.Minute})
	if rep.ControlFalseAlarms != 0 || rep.FaultRuns != 1 || rep.DetectedRuns != 1 || rep.Recall != 1 {
		t.Fatalf("%d false alarms, %d of %d fault runs detected; want the incident to be the fault's and the pod kill quiet",
			rep.ControlFalseAlarms, rep.DetectedRuns, rep.FaultRuns)
	}
	var md strings.Builder
	if err := rep.WriteMarkdown(&md); err != nil || !strings.Contains(md.String(), "pod-kill (heals itself) | 0 false alarm(s)") {
		t.Fatalf("markdown:\n%s", md.String())
	}
}

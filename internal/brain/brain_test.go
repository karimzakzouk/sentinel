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

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

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
	if a := incidents[0].Assessment; a == nil || a.Class != ClassBadImage || a.Culprit == nil || a.Culprit.Object != web ||
		a.Decision != ActionRolloutUndo {
		t.Fatalf("the incident record does not carry its assessment: %+v", a)
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

// A restarted controller takes back the incidents it left open, instead of
// leaving them open forever and opening duplicates.
func TestIncidentsSurviveARestart(t *testing.T) {
	b, l := newBrain(t)
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 3, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	restarted := &Brain{Ledger: l, Log: b.Log, Classify: b.Classify, Attribute: b.Attribute, Decide: b.Decide,
		Lookback: b.Lookback, Gap: b.Gap, Now: b.Now}
	if n, err := restarted.Restore(ctx); err != nil || n != 1 {
		t.Fatalf("restored %d, %v", n, err)
	}
	again := det
	again.ID = model.NewID()
	if err := restarted.OnDetection(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got := incidents(t, l); len(got) != 1 {
		t.Fatalf("incidents = %d after the restart, want the same one", len(got))
	}
	again.Ended = true
	if err := restarted.OnDetection(ctx, again); err != nil {
		t.Fatal(err)
	}
	restarted.Now = func() time.Time { return onset.Add(time.Hour) }
	if err := restarted.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := incidents(t, l); got[0].State != model.IncidentResolved {
		t.Fatalf("state = %s, want resolved", got[0].State)
	}
}

// Versions that did not take incidents back left several open on one
// workload; a restart keeps the newest and resolves the others.
func TestARestartResolvesDuplicatesLeftOpen(t *testing.T) {
	_, l := newBrain(t)
	var drafts []ledger.Draft
	for i, opened := range []time.Time{onset, onset.Add(time.Hour), onset.Add(30 * time.Minute)} {
		rec := model.Incident{ID: model.NewID(), State: model.IncidentOpen, OpenedAt: opened, UpdatedAt: opened,
			Objects: []model.ObjectRef{web}, Title: fmt.Sprint("left open ", i)}
		drafts = append(drafts, ledger.RecordDraft(source, model.KindIncident, "opened", rec.ID, rec.ID, rec))
	}
	if _, err := l.Append(ctx, drafts...); err != nil {
		t.Fatal(err)
	}
	b, _ := newBrain(t)
	b.Ledger = l
	if n, err := b.Restore(ctx); err != nil || n != 1 {
		t.Fatalf("restored %d, %v; want the newest only", n, err)
	}
	open := 0
	for _, inc := range incidents(t, l) {
		if inc.State == model.IncidentOpen {
			open++
			if !inc.OpenedAt.Equal(onset.Add(time.Hour)) {
				t.Fatalf("kept the incident opened at %v, want the newest", inc.OpenedAt)
			}
		}
	}
	if open != 1 {
		t.Fatalf("%d incidents still open on one workload", open)
	}
}

// errorSample is a minute of a pod's logs holding the given error templates,
// each once, besides its routine requests.
func errorSample(pod string, at time.Time, errs ...string) watcher.LogEvidence {
	templates := []logs.TemplateCount{{Template: "GET <*> 200", Count: 90}}
	for _, e := range errs {
		templates = append(templates, logs.TemplateCount{Template: e, Count: 1, Error: true, Sample: e})
	}
	return watcher.LogEvidence{Workload: web, Pod: pod, Container: "web", Reason: watcher.ReasonSample, CollectedAt: at,
		Summary: logs.Summary{Lines: 100, SHA256: pod + at.String(), Templates: templates}}
}

// Errors at an unusual rate with nothing to undo are escalated. A sample
// joins the incident only when it shows an error the workload did not log
// before and the incident has not recorded.
func TestAnErrorRateIncidentIsEscalatedWithItsNewErrors(t *testing.T) {
	b, l := newBrain(t)
	const routine, refused, breaker = "env platform is invalid", "redis: connection refused", "circuit breaker open"
	at := func(d time.Duration) time.Time { return onset.Add(d) }
	b.Now = func() time.Time { return at(-5 * time.Minute) }
	if err := b.OnEvidence(ctx, errorSample("web-1", at(-5*time.Minute), routine)); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset }
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactLogErrors, Count: 400, Onset: onset, At: onset,
		Window: map[watcher.FactKind]int{watcher.FactLogErrors: 400}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	for i, ev := range []watcher.LogEvidence{
		errorSample("web-1", at(time.Minute), routine, refused),
		errorSample("web-2", at(2*time.Minute), routine, refused),
		errorSample("web-1", at(3*time.Minute), routine),
		errorSample("web-2", at(5*time.Minute), refused, breaker),
	} {
		b.Now = func() time.Time { return ev.CollectedAt }
		if err := b.OnEvidence(ctx, ev); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	got := incidents(t, l)
	if len(got) != 1 || got[0].Assessment == nil || got[0].Assessment.Class != ClassRuntimeErrors ||
		got[0].Assessment.Decision != ActionEscalate {
		t.Fatalf("incident = %+v, want runtime errors, escalated", got)
	}
	evidence, _ := ledger.List[model.Evidence](ctx, l, model.KindEvidence, "")
	var collected []time.Time
	for _, e := range evidence {
		collected = append(collected, e.CollectedAt)
	}
	if len(evidence) != 2 || !evidence[0].CollectedAt.Equal(at(time.Minute)) || !evidence[1].CollectedAt.Equal(at(5*time.Minute)) {
		t.Fatalf("evidence from %v, want the first sample with the refused connections and the one where the breaker opened", collected)
	}
}

// The error rate can alarm before a deploy's new-error test has read the new
// pods; until it has, the deploy is the likely cause, and undoing it the
// answer, not an escalation.
func TestErrorsRightAfterADeployBlameTheDeploy(t *testing.T) {
	b, l := newBrain(t)
	change := watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"env:web"},
		At: onset.Add(-30 * time.Second), Actor: "kubectl"}
	if err := b.OnChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset }
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactLogErrors, Count: 120, Onset: onset, At: onset,
		Window: map[watcher.FactKind]int{watcher.FactLogErrors: 120}, Revisions: map[string]int{"new": 120}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	got := incidents(t, l)
	if a := got[0].Assessment; a == nil || a.Class != ClassNewErrors || a.Decision != ActionRolloutUndo {
		t.Fatalf("assessment = %+v, want new errors after a deploy, rolled back", a)
	}
}

// A long error message's details split one error into several templates;
// the incident records it once. Logs a rollout read that hold only the
// workload's routine errors are not evidence either.
func TestOneErrorWithVaryingDetailsIsRecordedOnce(t *testing.T) {
	b, l := newBrain(t)
	const routine = "env platform is either empty or invalid"
	cart := "request error: could not retrieve cart: rpc error: code = FailedPrecondition desc = Can't access cart storage."
	b.Now = func() time.Time { return onset.Add(-3 * time.Minute) }
	if err := b.OnEvidence(ctx, errorSample("web-1", onset.Add(-3*time.Minute), routine)); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset }
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactLogErrors, Count: 90, Onset: onset, At: onset}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	rollout := errorSample("web-2", onset.Add(30*time.Second), routine)
	rollout.Reason = "rollout-after"
	for _, ev := range []watcher.LogEvidence{
		rollout,
		errorSample("web-1", onset.Add(time.Minute), routine, cart+" inst: <num>, qu: <num>, aw: False, rs: NotStarted"),
		errorSample("web-2", onset.Add(2*time.Minute), cart+" inst: <num>, qu: <num>, qs: <num>, aw: True, rs: ReadAsync, ws: Idle"),
	} {
		b.Now = func() time.Time { return ev.CollectedAt }
		if err := b.OnEvidence(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if ev, _ := ledger.List[model.Evidence](ctx, l, model.KindEvidence, ""); len(ev) != 1 || !ev[0].CollectedAt.Equal(onset.Add(time.Minute)) {
		t.Fatalf("evidence = %d, want the first sample with the cart error only", len(ev))
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

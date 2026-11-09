package brain

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

type fakeRequester struct {
	calls []model.Action
	allow bool
}

func (f *fakeRequester) Request(_ context.Context, a model.Action, _ string) (bool, error) {
	f.calls = append(f.calls, a)
	return f.allow, nil
}

// badDeploy feeds the Brain a template change that produced the newest
// revision and the image pull errors that followed it.
func badDeploy(t *testing.T, b *Brain) watcher.Detection {
	t.Helper()
	change := watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"image:web"},
		At: onset.Add(-90 * time.Second), Actor: "kubectl"}
	if err := b.OnChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	det := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactImagePullError, Count: 6, Onset: onset,
		At: onset.Add(30 * time.Second), Revisions: map[string]int{"new": 6}}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	return det
}

func actionOf(t *testing.T, l *ledger.Ledger) model.Action {
	t.Helper()
	actions, err := ledger.List[model.Action](ctx, l, model.KindAction, "")
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions = %+v, %v", actions, err)
	}
	return actions[0]
}

func TestAnAllowedRollbackIsRequestedThenVerified(t *testing.T) {
	b, l := newBrain(t)
	req := &fakeRequester{allow: true}
	b.Act = req
	det := badDeploy(t, b)
	if len(req.calls) != 1 || req.calls[0].Type != ActionRolloutUndo || req.calls[0].Params[argFromTemplateHash] != "new" {
		t.Fatalf("requests = %+v, want one rollback of revision new", req.calls)
	}
	if a := actionOf(t, l); a.Shadow || a.State != model.ActionExecuting {
		t.Fatalf("action = %+v, want a requested, not a shadow, action", a)
	}
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Succeeded", Message: "rolled web back"}); err != nil {
		t.Fatal(err)
	}
	det.Ended, det.At = true, onset.Add(3*time.Minute)
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset.Add(4 * time.Minute) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if a := actionOf(t, l); a.State != model.ActionVerified || a.Params["result"] != "rolled web back" || a.Params["verification"] == "" {
		t.Fatalf("action = %+v, want it verified", a)
	}
}

func TestAnActionThatDoesNotClearTheIncidentIsUnverified(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	badDeploy(t, b)
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Succeeded"}); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset.Add(time.Minute + verifyWithin + time.Second) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if a := actionOf(t, l); a.State != model.ActionFailed || a.Params["verification"] == "" {
		t.Fatalf("action = %+v, want it failed verification", a)
	}
}

func TestARefusedRequestIsRecorded(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	badDeploy(t, b)
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Rejected", Message: "the kill switch is on"}); err != nil {
		t.Fatal(err)
	}
	if a := actionOf(t, l); a.State != model.ActionFailed || a.Params["result"] != "the kill switch is on" {
		t.Fatalf("action = %+v", a)
	}
}

func TestNothingIsRequestedWithoutPolicyOrSomethingToUndo(t *testing.T) {
	b, l := newBrain(t)
	req := &fakeRequester{allow: false}
	b.Act = req
	badDeploy(t, b)
	if a := actionOf(t, l); !a.Shadow || a.State != model.ActionProposed {
		t.Fatalf("action = %+v, want a shadow proposal when no policy allows it", a)
	}
	for _, tc := range []struct {
		action string
		change CandidateChange
	}{
		{ActionRolloutUndo, CandidateChange{Kind: string(watcher.ChangeConfigMap)}},
		{ActionRolloutUndo, CandidateChange{Kind: string(watcher.ChangeTemplate)}},
		{ActionScaleRestore, CandidateChange{Kind: string(watcher.ChangeScale), Fields: []string{"image:web"}}},
	} {
		if args := actionArgs(tc.action, &RankedChange{Change: tc.change}, "new"); args != nil {
			t.Errorf("%s of %+v: args %v, want none", tc.action, tc.change, args)
		}
	}
}

// After an action fails on a workload, Sentinel does not ask for it there
// again for a day, restarts included.
func TestAFailedActionOpensItsBreaker(t *testing.T) {
	b, l := newBrain(t)
	req := &fakeRequester{allow: true}
	b.Act = req
	det := badDeploy(t, b)
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Failed", Message: "web changed since the decision"}); err != nil {
		t.Fatal(err)
	}
	det.Ended, det.At = true, onset.Add(2*time.Minute)
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset.Add(time.Hour) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	restarted := &Brain{Ledger: l, Log: b.Log, Classify: b.Classify, Attribute: b.Attribute, Decide: b.Decide,
		Revisions: b.Revisions, Lookback: 2 * time.Hour, Gap: b.Gap, Now: b.Now, Act: req}
	if _, err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	again := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactImagePullError, Count: 6,
		Onset: onset.Add(time.Hour), At: onset.Add(time.Hour)}
	if err := restarted.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web,
		Fields: []string{"image:web"}, At: onset.Add(time.Hour - time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.OnDetection(ctx, again); err != nil {
		t.Fatal(err)
	}
	if len(req.calls) != 1 {
		t.Fatalf("%d requests; the breaker should have stopped the second", len(req.calls))
	}
}

// A request waiting for a person is not a failure; once approved and run,
// the action records who approved it.
func TestAnActionWaitsForItsApprover(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	badDeploy(t, b)
	inc := incidents(t, l)[0]
	waiting := ActionOutcome{IncidentID: inc.ID, Request: "shop/sentinel-1-rollout-undo", Phase: "Pending", Message: "waiting for a person to approve it"}
	if err := b.OnActionOutcome(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	if a := actionOf(t, l); a.State != model.ActionExecuting || a.Params["request"] != waiting.Request || len(b.breakers) != 0 {
		t.Fatalf("action = %+v, breakers %v; want it still in hand, with its request named", a, b.breakers)
	}
	done := ActionOutcome{IncidentID: inc.ID, Request: waiting.Request, Phase: "Succeeded", Message: "rolled web back", ApprovedBy: "alice"}
	if err := b.OnActionOutcome(ctx, done); err != nil {
		t.Fatal(err)
	}
	if a := actionOf(t, l); a.Params["approvedBy"] != "alice" || a.Params["result"] != "rolled web back" {
		t.Fatalf("action = %+v", a)
	}
}

// Sentinel's own rollback is a change to the workload too; it must not
// compete with the change it undid for the blame.
func TestSentinelsOwnChangeIsNeverTheCulprit(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	det := badDeploy(t, b)
	rollback := watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web, Fields: []string{"image:web"},
		At: onset.Add(31 * time.Second), Actor: model.ActorFieldManager}
	if err := b.OnChange(ctx, rollback); err != nil {
		t.Fatal(err)
	}
	more := det
	more.ID, more.Kind, more.At = model.NewID(), watcher.FactCrashLoop, onset.Add(40*time.Second)
	if err := b.OnDetection(ctx, more); err != nil {
		t.Fatal(err)
	}
	a := incidents(t, l)[0].Assessment
	if a == nil || a.Culprit == nil || a.Culprit.ChangeID == rollback.ID || a.Culprit.P < 0.9 || a.Decision != ActionRolloutUndo {
		t.Fatalf("assessment = %+v, culprit %+v; want the bad deploy still blamed, firmly", a, a.Culprit)
	}
}

func TestPeopleAreToldOnceWhatSentinelNeedsOrDid(t *testing.T) {
	b, _ := newBrain(t)
	var notices []notify.Notice
	b.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
	crash := watcher.Detection{ID: model.NewID(), Workload: web, Kind: watcher.FactCrashLoop, Count: 5, Onset: onset, At: onset}
	for range 2 {
		if err := b.OnDetection(ctx, crash); err != nil {
			t.Fatal(err)
		}
		crash.ID = model.NewID()
	}
	if len(notices) != 1 || notices[0].Kind != notify.KindEscalation || !strings.Contains(notices[0].Title, "no safe action") {
		t.Fatalf("notices = %+v, want one escalation", notices)
	}

	b2, l2 := newBrain(t)
	notices = nil
	b2.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
	b2.Act = &fakeRequester{allow: true}
	det := badDeploy(t, b2)
	inc := incidents(t, l2)[0]
	pending := ActionOutcome{IncidentID: inc.ID, Request: "shop/r1", Phase: "Pending", Message: "waiting for a person to approve it"}
	for range 2 {
		if err := b2.OnActionOutcome(ctx, pending); err != nil {
			t.Fatal(err)
		}
	}
	if err := b2.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Request: "shop/r1", Phase: "Succeeded"}); err != nil {
		t.Fatal(err)
	}
	det.Ended, det.At = true, onset.Add(2*time.Minute)
	if err := b2.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	b2.Now = func() time.Time { return onset.Add(3 * time.Minute) }
	if err := b2.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || notices[0].Kind != notify.KindApproval || !strings.Contains(notices[0].Detail, "sentinel approve shop/r1") ||
		notices[1].Kind != notify.KindActed || !strings.Contains(notices[1].Title, "rolled back") {
		t.Fatalf("notices = %+v, want the approval request once, then the verified rollback", notices)
	}
}

// Every verification adds to its cell's track record; 28 straight verified
// successes support autonomy, and the records outlive a restart.
func TestTrackRecordsCountVerifications(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	det := badDeploy(t, b)
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Succeeded"}); err != nil {
		t.Fatal(err)
	}
	det.Ended, det.At = true, onset.Add(2*time.Minute)
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return onset.Add(3 * time.Minute) }
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := ledger.List[model.TrackRecord](ctx, l, model.KindTrackRecord, "")
	key := model.TrackKey{Action: ActionRolloutUndo, FaultClass: ClassBadImage, Service: web.String()}
	if err != nil || len(records) != 1 || records[0].Key != key || records[0].Alpha != 2 || records[0].Beta != 1 ||
		records[0].Level != model.AutonomyApprove {
		t.Fatalf("records = %+v, %v", records, err)
	}

	// One verified success so far; 26 more make 27, and the 28th clears 90%.
	b.mu.Lock()
	for i := range 26 {
		b.track(key, true, inc.ID, onset.Add(time.Duration(i)*time.Hour))
	}
	level27 := b.tracks[key].Level
	b.track(key, true, inc.ID, onset.Add(30*time.Hour))
	level28 := b.tracks[key].Level
	b.mu.Unlock()
	if level27 != model.AutonomyApprove || level28 != model.AutonomyAutonomous {
		t.Fatalf("levels after 27 and 28 successes: %s and %s; want the 28th straight success to support autonomy", level27, level28)
	}

	restarted := &Brain{Ledger: l, Log: b.Log, Classify: b.Classify, Attribute: b.Attribute, Decide: b.Decide, Gap: b.Gap, Now: b.Now}
	if _, err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := restarted.tracks[key]; got == nil || got.Alpha != 2 {
		t.Fatalf("restored record = %+v, want the one in the Ledger", got)
	}
}

func TestScaleRestoreArgsComeFromTheChange(t *testing.T) {
	args := actionArgs(ActionScaleRestore, &RankedChange{Change: CandidateChange{Kind: string(watcher.ChangeScale),
		Fields: []string{"replicas:3->0"}}}, "")
	if args[argReplicas] != "3" || args[argFromReplicas] != "0" {
		t.Fatalf("args = %v", args)
	}
}

type fakeLab struct {
	asks []string
	busy bool
}

func (f *fakeLab) Verify(_ context.Context, incident model.ID, target model.ObjectRef, revision string) (string, bool, error) {
	if f.busy {
		return "", false, nil
	}
	f.asks = append(f.asks, target.Name+"@"+revision)
	return "lab-0/sentinel-" + string(incident)[:4] + "-verify", true, nil
}

// A proposed rollback goes to the Lab once, whatever the policy, naming the
// revision the change produced; its verdict is recorded on the incident and
// reaches whoever must approve.
func TestTheLabVerifiesAProposedRollbackOnce(t *testing.T) {
	b, l := newBrain(t)
	lab := &fakeLab{busy: true}
	b.Lab = lab
	det := badDeploy(t, b)
	if len(lab.asks) != 0 {
		t.Fatalf("asks = %v; the Lab was busy", lab.asks)
	}
	lab.busy = false
	det.Count, det.At = 9, det.At.Add(15*time.Second)
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	if len(lab.asks) != 1 || lab.asks[0] != "web@web-new" {
		t.Fatalf("asks = %v; want one, of web's failing revision", lab.asks)
	}
	inc := incidents(t, l)[0]
	exps, err := ledger.List[model.Experiment](ctx, l, model.KindExperiment, "")
	if err != nil || len(exps) != 1 || exps[0].Phase != "Requested" || exps[0].IncidentID != inc.ID {
		t.Fatalf("experiments = %+v, %v", exps, err)
	}

	b.Act = &fakeRequester{allow: true}
	b.open[web.String()].action = &model.Action{Type: ActionRolloutUndo, Target: web, Params: map[string]string{}}
	var notices []notify.Notice
	b.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
	if err := b.OnLabVerdict(ctx, LabVerdict{IncidentID: inc.ID, Experiment: "lab-0/x", Phase: "Passed",
		Message: "the previous revision fixes it", Runs: 15, Fidelity: []string{"runs one replica instead of 3"}}); err != nil {
		t.Fatal(err)
	}
	exps, _ = ledger.List[model.Experiment](ctx, l, model.KindExperiment, "")
	if len(exps) != 1 || exps[0].Phase != "Passed" || exps[0].Runs != 15 || len(exps[0].Fidelity) != 1 {
		t.Fatalf("experiments = %+v; want the verdict recorded", exps)
	}
	if len(notices) != 1 || !strings.Contains(notices[0].Title, "it fixes the failure") {
		t.Fatalf("notices = %+v; want the approver told", notices)
	}
}

// The rollback's request names the Experiment verifying it, for a policy
// that lets the Lab stand in for a person.
func TestARollbackRequestNamesItsVerification(t *testing.T) {
	b, _ := newBrain(t)
	req := &fakeRequester{allow: true}
	b.Act, b.Lab = req, &fakeLab{}
	badDeploy(t, b)
	if len(req.calls) != 1 || !strings.HasPrefix(req.calls[0].Params[argExperiment], "lab-0/") || req.calls[0].Params[argFromTemplateHash] != "new" {
		t.Fatalf("requests = %+v; want one rollback naming the Lab's Experiment", req.calls)
	}
}

// A cell's standing: nothing verified yet, some but short of the bar, and
// earned once the 5th percentile of its success rate clears it.
func TestACellsStandingSaysWhyItHasNotEarnedAutonomy(t *testing.T) {
	b, _ := newBrain(t)
	key := model.TrackKey{Action: ActionRolloutUndo, FaultClass: ClassBadImage, Service: web.String()}
	if earned, why := b.standing(key); earned || !strings.Contains(why, "has been verified yet") {
		t.Fatalf("with no record: %v, %q", earned, why)
	}
	for range 3 {
		b.track(key, true, "inc", onset)
	}
	if earned, why := b.standing(key); earned || !strings.Contains(why, "3 verified and 0 not") {
		t.Fatalf("after three: %v, %q", earned, why)
	}
	for range 25 {
		b.track(key, true, "inc", onset)
	}
	if earned, why := b.standing(key); !earned || why != "" {
		t.Fatalf("after 28 straight: %v, %q", earned, why)
	}
}

// A person who puts back the revision Sentinel rolled away from reverts the
// rollback: it counts against the cell, opens the breaker and is told.
// Sentinel's own change, or a person's new revision, does not.
func TestAPersonsRevertCountsAgainstTheRollback(t *testing.T) {
	b, l := newBrain(t)
	b.Act = &fakeRequester{allow: true}
	newest := "new"
	b.Revisions = func(model.ObjectRef) string { return newest }
	var notices []notify.Notice
	b.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
	det := badDeploy(t, b)
	inc := incidents(t, l)[0]
	if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Succeeded", Message: "rolled web back"}); err != nil {
		t.Fatal(err)
	}
	det.Ended, det.At = true, onset.Add(2*time.Minute)
	if err := b.OnDetection(ctx, det); err != nil {
		t.Fatal(err)
	}
	change := func(actor string, at time.Duration) {
		t.Helper()
		if err := b.OnChange(ctx, watcher.Change{ID: model.NewID(), Kind: watcher.ChangeTemplate, Object: web,
			Fields: []string{"image:web"}, At: onset.Add(at), Actor: actor}); err != nil {
			t.Fatal(err)
		}
	}
	sweep := func(at time.Duration) {
		t.Helper()
		b.Now = func() time.Time { return onset.Add(at) }
		if err := b.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	reverted := func() bool {
		events, err := l.Events(ctx, 0, 100000)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(events, func(e ledger.Event) bool { return e.Type == "sentinel.action.reverted" })
	}

	newest = "old"
	change(model.ActorFieldManager, 2*time.Minute)
	sweep(3 * time.Minute)
	change("kubectl-client-side-apply", 10*time.Minute)
	newest = "newer"
	sweep(11 * time.Minute)
	if reverted() {
		t.Fatal("Sentinel's own rollback, then a person's new revision, counted as a revert")
	}
	change("kubectl-client-side-apply", 20*time.Minute)
	newest = "new"
	sweep(21 * time.Minute)
	if !reverted() {
		t.Fatal("putting back the revision Sentinel rolled away from was not counted")
	}
	key := model.TrackKey{Action: ActionRolloutUndo, FaultClass: ClassBadImage, Service: web.String()}
	if rec := b.tracks[key]; rec == nil || rec.Alpha != 2 || rec.Beta != 2 {
		t.Fatalf("track record %+v; want the verified rollback and the revert", rec)
	}
	if b.breakerUntil(ActionRolloutUndo, web).IsZero() {
		t.Fatal("the breaker did not open")
	}
	if len(notices) == 0 || !strings.Contains(notices[len(notices)-1].Title, "was reverted by kubectl-client-side-apply") {
		t.Fatalf("notices = %+v", notices)
	}
	sweep(22 * time.Minute)
	if rec := b.tracks[key]; rec.Beta != 2 {
		t.Fatalf("the revert counted %v times", rec.Beta-1)
	}
}

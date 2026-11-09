package brain

import (
	"context"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
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

func TestScaleRestoreArgsComeFromTheChange(t *testing.T) {
	args := actionArgs(ActionScaleRestore, &RankedChange{Change: CandidateChange{Kind: string(watcher.ChangeScale),
		Fields: []string{"replicas:3->0"}}}, "")
	if args[argReplicas] != "3" || args[argFromReplicas] != "0" {
		t.Fatalf("args = %v", args)
	}
}

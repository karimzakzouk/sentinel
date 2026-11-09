package brain

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
)

type fakeFixer struct {
	asked    []string
	requests []FixRequest
}

func (f *fakeFixer) Fix(_ context.Context, r FixRequest) (string, error) {
	f.asked = append(f.asked, string(r.Incident)+" "+r.Target.String()+" "+r.FromRevision)
	f.requests = append(f.requests, r)
	return "sentinel/fix", nil
}

// A verified rollback asks the Fixer, once, to make it hold at its source.
// The Fixer's steps reach people when they need them: a pull request to
// review, an owner to resume. Once the owner has applied the fix, Sentinel
// checks that the workload stayed on the revision it rolled back to.
func TestAVerifiedRollbackIsFixedAtItsSource(t *testing.T) {
	for _, tc := range []struct {
		name     string
		after    string
		want     string
		wantKind string
	}{
		{"held", "old", "Held", notify.KindActed},
		{"not held", "new", "NotHeld", notify.KindEscalation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, l := newBrain(t)
			b.Act = &fakeRequester{allow: true}
			fixer := &fakeFixer{}
			b.Fixer = fixer
			newest := "new"
			b.Revisions = func(model.ObjectRef) string { return newest }
			var notices []notify.Notice
			b.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
			det := badDeploy(t, b)
			inc := incidents(t, l)[0]
			if err := b.OnActionOutcome(ctx, ActionOutcome{IncidentID: inc.ID, Phase: "Succeeded", Message: "rolled web back"}); err != nil {
				t.Fatal(err)
			}
			newest = "old"
			det.Ended, det.At = true, onset.Add(2*time.Minute)
			if err := b.OnDetection(ctx, det); err != nil {
				t.Fatal(err)
			}
			sweep := func(at time.Duration) {
				t.Helper()
				b.Now = func() time.Time { return onset.Add(at) }
				if err := b.Sweep(ctx); err != nil {
					t.Fatal(err)
				}
			}
			sweep(3 * time.Minute)
			sweep(4 * time.Minute)
			if len(fixer.asked) != 1 || fixer.asked[0] != string(inc.ID)+" "+web.String()+" new" {
				t.Fatalf("asked the Fixer %q", fixer.asked)
			}
			update := func(phase string) {
				t.Helper()
				if err := b.OnFixUpdate(ctx, FixUpdate{IncidentID: inc.ID, Phase: phase, Owner: "Kustomization flux-system/shop",
					Resume: "sentinel resume flux flux-system/shop", PullRequest: "http://forge/shop/pulls/1", MergeCommit: "abc",
					Change: []string{"kustomize/deployment.yaml:14: web's image"}}); err != nil {
					t.Fatal(err)
				}
			}
			update("Proposed")
			update("Proposed")
			update("Ready")
			var titles []string
			for _, n := range notices {
				titles = append(titles, n.Kind+": "+n.Title+" | "+n.Detail)
			}
			got := strings.Join(titles, "\n")
			if strings.Count(got, "Sentinel proposes a fix for "+web.String()+": http://forge/shop/pulls/1") != 1 ||
				!strings.Contains(got, "approval: The fix for "+web.String()+" is in Kustomization flux-system/shop's source") ||
				!strings.Contains(got, "resume it with `sentinel resume flux flux-system/shop`") {
				t.Fatalf("notices:\n%s", got)
			}
			update("Applied")
			newest = tc.after
			sweep(5 * time.Minute)
			fixes, err := ledger.List[model.Fix](ctx, l, model.KindFix, "")
			if err != nil || len(fixes) != 1 || fixes[0].Phase != "Applied" {
				t.Fatalf("fixes = %+v, %v; want it still Applied right after", fixes, err)
			}
			sweep(7 * time.Minute)
			fixes, _ = ledger.List[model.Fix](ctx, l, model.KindFix, "")
			if fixes[0].Phase != tc.want || notices[len(notices)-1].Kind != tc.wantKind {
				t.Fatalf("fix %s, last notice %+v; want %s and a %s notice", fixes[0].Phase, notices[len(notices)-1], tc.want, tc.wantKind)
			}
			// The fix's track record counts it: for when it held, against when not.
			track := b.tracks[fixTrack(fixes[0])]
			if track == nil || (tc.want == "Held") != (track.Alpha > track.Beta) {
				t.Fatalf("track record %+v after a fix that %s", track, tc.want)
			}
		})
	}
}

// The Fixer is told the incident's class, and for a memory limit the limit
// learned from what each container used outside the revision that failed.
func TestTheFixerIsToldTheClassAndALearnedLimit(t *testing.T) {
	b, _ := newBrain(t)
	fixer := &fakeFixer{}
	b.Fixer = fixer
	var except string
	b.MemoryUse = func(_ model.ObjectRef, rev string) map[string][]float64 {
		except = rev
		used := make([]float64, 40)
		for i := range used {
			used[i] = 6 << 20
		}
		return map[string][]float64{"web": used, "sidecar": used[:3]}
	}
	inc := &incident{rec: model.Incident{ID: "inc-1", Assessment: &model.Assessment{Class: ClassMemoryLimit}}}
	a := &model.Action{Type: ActionRolloutUndo, Target: web, Params: map[string]string{argFromTemplateHash: "bad"}}
	if drafts := b.askFix(ctx, inc, a); len(drafts) != 1 || len(fixer.requests) != 1 {
		t.Fatalf("drafts %d, requests %d", len(drafts), len(fixer.requests))
	}
	r := fixer.requests[0]
	if r.FaultClass != ClassMemoryLimit || except != "bad" || r.MemoryLimits["web"] != "14Mi" || len(r.MemoryLimits) != 1 {
		t.Fatalf("request %+v, learned outside %q", r, except)
	}
}

// Where a policy grants fix.merge at autonomous but makes fixes earn it, a
// person merges until fixes of that fault on that workload have held often
// enough; then the Fixer may. A fix that held counts for it.
func TestAFixEarnsItsOwnMerge(t *testing.T) {
	b, _ := newBrain(t)
	fixer := &fakeFixer{}
	b.Fixer = fixer
	b.MergePolicy = func(string) (model.AutonomyLevel, bool) { return model.AutonomyAutonomous, true }
	ask := func(id model.ID) FixRequest {
		t.Helper()
		inc := &incident{rec: model.Incident{ID: id, Assessment: &model.Assessment{Class: ClassBadImage}}}
		a := &model.Action{Type: ActionRolloutUndo, Target: web, Params: map[string]string{argFromTemplateHash: "bad"}}
		if len(b.askFix(ctx, inc, a)) != 1 {
			t.Fatal("no fix asked for")
		}
		return fixer.requests[len(fixer.requests)-1]
	}
	if r := ask("inc-1"); r.Merge {
		t.Fatal("a fix merged itself before any fix had held")
	}
	key := fixTrack(model.Fix{FaultClass: ClassBadImage, Target: web})
	for range 28 {
		b.track(key, true, "inc-0", b.now())
	}
	if r := ask("inc-2"); !r.Merge {
		t.Fatal("28 fixes held and the next still waits for a person")
	}
	b.MergePolicy = func(string) (model.AutonomyLevel, bool) { return model.AutonomyApprove, false }
	if r := ask("inc-3"); r.Merge {
		t.Fatal("a fix merged itself where the policy grants approve")
	}
}

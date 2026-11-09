package brain

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
	"github.com/karimzakzouk/sentinel/internal/watcher"
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

// The Fixer is told the newest crash in the workload's own logs, with its
// application's frames; another workload's crash is not its.
func TestTheFixerIsToldTheCrash(t *testing.T) {
	b, l := newBrain(t)
	fixer := &fakeFixer{}
	b.Fixer = fixer
	inc := &incident{rec: model.Incident{ID: "inc-1", Assessment: &model.Assessment{Class: ClassCrashOnStart}}}
	evidence := func(workload model.ObjectRef, fn string, at time.Duration) ledger.Draft {
		ev := watcher.LogEvidence{Workload: workload, Pod: workload.Name + "-1", CollectedAt: onset.Add(at), Summary: logs.Summary{
			Exceptions: []logs.ExceptionCount{{Exception: logs.Exception{Language: "go", Types: []string{"nil pointer"},
				Frames: []logs.Frame{{Function: fn, InApp: true}, {Function: "runtime.goexit"}}}}}}}
		e := evidenceRecord("inc-1", ev)
		return ledger.RecordDraft(source, model.KindEvidence, "attached", e.ID, "inc-1", e)
	}
	other := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: web.Namespace, Name: "front"}
	if _, err := l.Append(ctx, evidence(web, "example.com/shop/cart.Old", 0), evidence(web, "example.com/shop/cart.(*Store).Get", time.Minute),
		evidence(other, "example.com/front.Render", 2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	a := &model.Action{Type: ActionRolloutUndo, Target: web, Params: map[string]string{argFromTemplateHash: "bad"}}
	b.askFix(ctx, inc, a)
	if c := fixer.requests[0].Crash; c == nil || c.Language != "go" || len(c.Frames) != 1 || c.Frames[0] != "example.com/shop/cart.(*Store).Get" {
		t.Fatalf("crash %+v", c)
	}
}

// fakeImageLab answers image races, or is busy once.
type fakeImageLab struct {
	fakeVerifierNone
	busy  int
	asked []string
}

type fakeVerifierNone struct{}

func (fakeVerifierNone) Verify(context.Context, model.ID, model.ObjectRef, string) (string, bool, error) {
	return "", false, nil
}

func (l *fakeImageLab) VerifyImage(_ context.Context, _ model.ID, _ model.ObjectRef, revision, image string) (string, bool, error) {
	if l.busy > 0 {
		l.busy--
		return "", false, nil
	}
	l.asked = append(l.asked, revision+" "+image)
	return "sentinel-lab-0/sentinel-x-code", true, nil
}

// A fix to the code is told to people when its pull request opens; once
// its image is built, the Lab is asked to race it, again after a sweep if
// the Lab was busy; its verdict reaches people too.
func TestAFixToTheCodeIsRacedOnceBuilt(t *testing.T) {
	b, l := newBrain(t)
	lab := &fakeImageLab{busy: 1}
	b.Lab = lab
	var notices []notify.Notice
	b.Notify = func(_ context.Context, n notify.Notice) { notices = append(notices, n) }
	f := model.Fix{ID: model.FixID("inc-1"), IncidentID: "inc-1", Target: web, FromRevision: "bad", Phase: "Proposed"}
	if _, err := l.Append(ctx, ledger.RecordDraft(source, model.KindFix, "proposed", f.ID, "inc-1", f)); err != nil {
		t.Fatal(err)
	}
	update := func(code model.FixCode) {
		t.Helper()
		if err := b.OnFixUpdate(ctx, FixUpdate{IncidentID: "inc-1", Phase: "Proposed", Code: &code}); err != nil {
			t.Fatal(err)
		}
	}
	update(model.FixCode{PullRequest: "http://forge/shop/pulls/2", Change: []string{"cart/store.go:3: Get put back"}})
	if len(notices) != 1 || !strings.Contains(notices[0].Title, "a fix to "+web.String()+"'s code") {
		t.Fatalf("notices %+v", notices)
	}
	update(model.FixCode{PullRequest: "http://forge/shop/pulls/2", Image: "registry.test/shop:3f2a9c1"})
	if len(lab.asked) != 0 {
		t.Fatal("asked a busy Lab")
	}
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(lab.asked) != 1 || lab.asked[0] != web.Name+"-bad registry.test/shop:3f2a9c1" {
		t.Fatalf("asked %v", lab.asked)
	}
	if err := b.OnLabVerdict(ctx, LabVerdict{IncidentID: "inc-1", Experiment: "sentinel-lab-0/sentinel-x-code", Phase: "Passed",
		Message: "the image fixes it"}); err != nil {
		t.Fatal(err)
	}
	last := notices[len(notices)-1]
	if !strings.Contains(last.Title, "The Lab on the fix to "+web.String()+"'s code: it fixes the failure") {
		t.Fatalf("last notice %+v", last)
	}
}

// A fix to the code built while the controller was down is raced after it
// restarts; one the Lab was already asked about is not asked about again.
func TestAFixToTheCodeBuiltBeforeARestartIsRaced(t *testing.T) {
	b, l := newBrain(t)
	built := model.Fix{ID: model.FixID("inc-1"), IncidentID: "inc-1", Target: web, FromRevision: "bad", Phase: "Applied",
		Code: &model.FixCode{PullRequest: "http://forge/shop/pulls/2", Image: "registry.test/shop:3f2a9c1"}}
	raced := model.Fix{ID: model.FixID("inc-2"), IncidentID: "inc-2", Target: web, FromRevision: "bad", Phase: "Applied",
		Code: &model.FixCode{PullRequest: "http://forge/shop/pulls/3", Image: "registry.test/shop:77aa001"}}
	e := model.Experiment{ID: codeExperimentID("inc-2"), IncidentID: "inc-2", Purpose: "verify-code", Target: web, Phase: "Requested"}
	if _, err := l.Append(ctx, ledger.RecordDraft(source, model.KindFix, "code", built.ID, "inc-1", built),
		ledger.RecordDraft(source, model.KindFix, "code", raced.ID, "inc-2", raced),
		ledger.RecordDraft(source, model.KindExperiment, "requested", e.ID, "inc-2", e)); err != nil {
		t.Fatal(err)
	}
	lab := &fakeImageLab{}
	b.ImageLab = lab
	if _, err := b.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(lab.asked) != 1 || lab.asked[0] != web.Name+"-bad registry.test/shop:3f2a9c1" {
		t.Fatalf("asked %v", lab.asked)
	}
}

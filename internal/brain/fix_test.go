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

type fakeFixer struct{ asked []string }

func (f *fakeFixer) Fix(_ context.Context, incident model.ID, target model.ObjectRef, from string) (string, error) {
	f.asked = append(f.asked, string(incident)+" "+target.String()+" "+from)
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
		})
	}
}

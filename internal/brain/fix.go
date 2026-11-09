package brain

import (
	"context"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/learn"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
)

// Fixer asks for the change at the source that makes the rollback of a
// workload hold. It returns which Fix answers.
type Fixer interface {
	Fix(ctx context.Context, r FixRequest) (string, error)
}

// FixRequest is what the Fixer is asked: to make the rollback of Target,
// away from the revision with pod template hash FromRevision, hold where
// that revision was written. FaultClass says which of the revision's
// changes to blame; MemoryLimits are limits learned, per container, from
// what it used while healthy.
type FixRequest struct {
	Incident     model.ID
	Target       model.ObjectRef
	FromRevision string
	FaultClass   string
	MemoryLimits map[string]string
	// Merge lets the Fixer merge the fix itself.
	Merge bool
}

// ActionFixMerge is the policy action that lets the Fixer merge its own
// pull requests, and the action of a fix's track record: a fix that held
// counts for it, one that did not or that a person declined, against.
const ActionFixMerge = "fix.merge"

// FixUpdate is a step the Fixer reports for an incident's fix.
type FixUpdate struct {
	IncidentID  model.ID
	Fix         string
	Phase       string
	Message     string
	Owner       string
	Resume      string
	Repository  string
	PullRequest string
	MergeCommit string
	Change      []string
}

// holdSettle is how long after its owner applied a fix Sentinel checks that
// the workload did not go back to the revision it was rolled back from.
const holdSettle = 2 * time.Minute

type holdCheck struct {
	target model.ObjectRef
	from   string
	since  time.Time
}

// askFix asks the Fixer, once per incident, for the change at the source
// that makes a verified rollback of a Deployment hold. Callers hold b.mu.
func (b *Brain) askFix(ctx context.Context, inc *incident, a *model.Action) []ledger.Draft {
	from := a.Params[argFromTemplateHash]
	if b.Fixer == nil || inc.fixAsked || a.Type != ActionRolloutUndo || a.Target.Kind != "Deployment" || from == "" {
		return nil
	}
	r := FixRequest{Incident: inc.rec.ID, Target: a.Target, FromRevision: from}
	if inc.rec.Assessment != nil {
		r.FaultClass = inc.rec.Assessment.Class
	}
	if r.FaultClass == ClassMemoryLimit && b.MemoryUse != nil {
		for container, used := range b.MemoryUse(a.Target, from) {
			if limit, err := learn.MemoryLimit(used); err == nil {
				if r.MemoryLimits == nil {
					r.MemoryLimits = map[string]string{}
				}
				r.MemoryLimits[container] = limit
			} else {
				b.Log.Info("no memory limit learned", "incident", inc.rec.ID, "container", container, "reason", err)
			}
		}
	}
	why := ""
	if b.MergePolicy != nil {
		if level, gated := b.MergePolicy(a.Target.Namespace); level == model.AutonomyAutonomous {
			earned, standing := b.standing(model.TrackKey{Action: ActionFixMerge, FaultClass: r.FaultClass, Service: a.Target.String()})
			r.Merge = !gated || earned
			if !r.Merge {
				why = "; a person merges it, since " + standing
			}
		}
	}
	name, err := b.Fixer.Fix(ctx, r)
	if err != nil {
		b.Log.Error("could not ask the Fixer for a fix at the source", "incident", inc.rec.ID, "error", err)
		return nil
	}
	inc.fixAsked = true
	f := model.Fix{ID: model.FixID(inc.rec.ID), IncidentID: inc.rec.ID, Target: a.Target, FromRevision: from, Phase: "Requested",
		Message: name + why, FaultClass: r.FaultClass, Merge: r.Merge}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindFix, "requested", f.ID, inc.rec.ID, f)}
}

// OnFixUpdate records a step of an incident's fix and tells people what it
// needs from them: a review of the pull request, resuming the owner once
// the fix is in its source, or fixing it themselves when the Fixer could
// not. Once the owner has applied the fix, Sentinel checks that it held.
func (b *Brain) OnFixUpdate(ctx context.Context, u FixUpdate) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := ledger.List[model.Fix](ctx, b.Ledger, model.KindFix, "")
	if err != nil {
		return err
	}
	var f *model.Fix
	for i := range records {
		if records[i].ID == model.FixID(u.IncidentID) {
			f = &records[i]
		}
	}
	switch {
	case f == nil:
		b.Log.Info("the Fixer reported on a fix Sentinel did not ask for", "fix", u.Fix, "phase", u.Phase)
		return nil
	case f.Phase == u.Phase || f.Phase == "Held" || f.Phase == "NotHeld":
		return nil
	}
	f.Phase, f.Message, f.Owner, f.Resume, f.Repository = u.Phase, u.Message, u.Owner, u.Resume, u.Repository
	f.PullRequest, f.MergeCommit, f.Change = u.PullRequest, u.MergeCommit, u.Change
	b.Log.Info("fix "+strings.ToLower(u.Phase), "incident", u.IncidentID, "target", f.Target.String(), "pullRequest", u.PullRequest, "message", u.Message)
	target := f.Target.String()
	var drafts []ledger.Draft
	resume := "resume it with `" + u.Resume + "`"
	if u.Resume == "" {
		resume = "resume what Sentinel paused"
	}
	switch u.Phase {
	case "Proposed":
		b.tell(ctx, notify.KindApproval, u.IncidentID, f.Target, "Sentinel proposes a fix for "+target+": "+u.PullRequest,
			strings.Join(u.Change, "\n")+"\n"+u.Message)
	case "Abstained", "Failed":
		b.tell(ctx, notify.KindEscalation, u.IncidentID, f.Target, "Sentinel has no fix in Git for "+target,
			u.Message+". Fix the change where it was made, then "+resume+".")
	case "Declined":
		b.tell(ctx, notify.KindEscalation, u.IncidentID, f.Target, "The fix for "+target+" was closed without merging",
			u.Owner+" stays paused. Fix the change where it was made, then "+resume+".")
		drafts = append(drafts, b.track(fixTrack(*f), false, u.IncidentID, b.now()))
	case "Ready":
		b.tell(ctx, notify.KindApproval, u.IncidentID, f.Target, "The fix for "+target+" is in "+u.Owner+"'s source",
			"Merged as "+u.MergeCommit+": "+resume+", and Sentinel checks that "+target+" keeps running.")
	case "Applied":
		if b.holding == nil {
			b.holding = map[model.ID]holdCheck{}
		}
		b.holding[u.IncidentID] = holdCheck{target: f.Target, from: f.FromRevision, since: b.now()}
	}
	_, err = b.Ledger.Append(ctx, append([]ledger.Draft{ledger.RecordDraft(source, model.KindFix, strings.ToLower(u.Phase), f.ID, u.IncidentID, *f)},
		drafts...)...)
	return err
}

// fixTrack is the cell of a fix's track record.
func fixTrack(f model.Fix) model.TrackKey {
	return model.TrackKey{Action: ActionFixMerge, FaultClass: f.FaultClass, Service: f.Target.String()}
}

// holds checks the workloads of fixes their owners applied a while ago:
// back on the revision they were rolled back from, the fix did not hold.
// Callers hold b.mu.
func (b *Brain) holds(ctx context.Context, now time.Time) []ledger.Draft {
	var drafts []ledger.Draft
	for id, h := range b.holding {
		if now.Sub(h.since) < holdSettle {
			continue
		}
		delete(b.holding, id)
		f := model.Fix{ID: model.FixID(id)}
		records, err := ledger.List[model.Fix](ctx, b.Ledger, model.KindFix, "")
		if err != nil {
			b.Log.Error("could not read a fix", "incident", id, "error", err)
			continue
		}
		for _, r := range records {
			if r.ID == f.ID {
				f = r
			}
		}
		if b.revision(h.target) == h.from {
			f.Phase = "NotHeld"
			f.Message = h.target.String() + " runs the revision it was rolled back from again"
			b.tell(ctx, notify.KindEscalation, id, h.target, "The fix for "+h.target.String()+" did not hold", f.Message)
		} else {
			f.Phase = "Held"
			f.Message = f.Owner + " is resumed with the fix, and " + h.target.String() + " kept running the revision Sentinel rolled back to"
			b.tell(ctx, notify.KindActed, id, h.target, "Fixed at the source: "+h.target.String(), f.Message)
		}
		b.Log.Info("fix "+strings.ToLower(f.Phase), "incident", id, "target", h.target.String(), "message", f.Message)
		drafts = append(drafts, ledger.RecordDraft(source, model.KindFix, strings.ToLower(f.Phase), f.ID, id, f),
			b.track(fixTrack(f), f.Phase == "Held", id, now))
	}
	return drafts
}

// tell sends a notice about an incident that may have closed already.
func (b *Brain) tell(ctx context.Context, kind string, incident model.ID, workload model.ObjectRef, title, detail string) {
	if b.Notify != nil {
		b.Notify(ctx, notify.Notice{Kind: kind, Incident: incident, Workload: workload, Title: title, Detail: detail})
	}
}

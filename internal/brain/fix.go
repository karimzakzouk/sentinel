package brain

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/learn"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/logs"
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
// changes to blame; MemoryLimits and CPULimits are limits learned, per
// container, from what it used while healthy.
type FixRequest struct {
	Incident     model.ID
	Target       model.ObjectRef
	FromRevision string
	FaultClass   string
	MemoryLimits map[string]string
	CPULimits    map[string]string
	// Merge lets the Fixer merge the fix itself.
	Merge bool
	// Crash is the stack the workload crashed with, from its logs, when
	// they held one: the application's own functions, innermost first.
	Crash *Crash
	// Errors are the error lines the failing revision logged, as masked
	// templates: where no stack says where it failed, the source that
	// logs them may.
	Errors []string
	// LabVerdict is what the Lab found of the learned values, raced before
	// the pull request that sets them opens.
	LabVerdict string
	// CodeOnly asks for a fix to the code alone, for a crash no rollback
	// could stop: FromRevision is then the revision that crashes, still
	// running, and the culprit is whichever commit last changed the code
	// the crash ran through.
	CodeOnly bool
}

// Crash is a parsed crash stack.
type Crash struct {
	Language string
	Types    []string
	Message  string
	Frames   []string
	// Stack is the frames with where each ran, where the runtime printed it.
	Stack []logs.Frame
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
	// Code is how the fix to the failing image's code stands, when the
	// Fixer looked for one.
	Code *model.FixCode
}

// ImageVerifier asks the Lab whether an image, a fix to a failing
// revision's code that its source's pipeline built, fixes an incident.
type ImageVerifier interface {
	VerifyImage(ctx context.Context, incident model.ID, target model.ObjectRef, revision, image string) (string, bool, error)
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
	if r.FaultClass == ClassCPULimit && b.CPUUse != nil {
		for container, used := range b.CPUUse(a.Target, from) {
			if limit, err := learn.CPULimit(used); err == nil {
				if r.CPULimits == nil {
					r.CPULimits = map[string]string{}
				}
				r.CPULimits[container] = limit
			} else {
				b.Log.Info("no CPU limit learned", "incident", inc.rec.ID, "container", container, "reason", err)
			}
		}
	}
	r.Crash, r.Errors = b.crashOf(ctx, inc.rec.ID, a.Target)
	inc.fixAsked = true
	if (len(r.MemoryLimits) > 0 || len(r.CPULimits) > 0) && b.ValueLab != nil {
		// A learned limit is raced in the Lab before the pull request that
		// sets it opens; the Fixer is asked once the verdict is in.
		if b.valueFixes == nil {
			b.valueFixes = map[model.ID]*valueFix{}
		}
		vf := &valueFix{req: r, revision: a.Target.Name + "-" + from}
		b.valueFixes[inc.rec.ID] = vf
		return b.askValueLab(ctx, vf)
	}
	return b.requestFix(ctx, r)
}

// valueFix is a fix request waiting on the Lab's verdict on the values it
// sets.
type valueFix struct {
	req      FixRequest
	revision string
	asked    bool
}

// askValueLab asks the Lab, once, to race the failing revision with the
// learned values in place; when every Lab namespace is busy the sweep asks
// again, and when it cannot be asked the Fixer is asked without a verdict.
// Callers hold b.mu.
func (b *Brain) askValueLab(ctx context.Context, vf *valueFix) []ledger.Draft {
	if vf.asked {
		return nil
	}
	r := vf.req
	experiment, asked, err := b.ValueLab(ctx, r.Incident, r.Target, vf.revision, r.MemoryLimits, r.CPULimits)
	switch {
	case err != nil:
		b.Log.Error("could not ask the Lab about a learned limit; the Fixer is asked without its verdict", "incident", r.Incident, "error", err)
		delete(b.valueFixes, r.Incident)
		return b.requestFix(ctx, r)
	case !asked:
		return nil
	}
	vf.asked = true
	e := model.Experiment{ID: model.ExperimentID(r.Incident) + "/values", IncidentID: r.Incident, Purpose: "verify-values", Target: r.Target,
		Revision: vf.revision, Lab: experiment, Phase: "Requested"}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindExperiment, "requested", e.ID, r.Incident, e)}
}

// onValuesVerdict asks the Fixer for the fix whose values the Lab raced,
// with the verdict: where the learned values did not fix the failure, the
// Fixer does not set them. Callers hold b.mu.
func (b *Brain) onValuesVerdict(ctx context.Context, v LabVerdict) error {
	vf, ok := b.valueFixes[v.IncidentID]
	if !ok {
		return nil
	}
	delete(b.valueFixes, v.IncidentID)
	r := vf.req
	r.LabVerdict = labVerdictWord(v.Phase) + ": " + v.Message
	b.Log.Info("the Lab judged a learned limit", "incident", v.IncidentID, "phase", v.Phase, "message", v.Message)
	if v.Phase == "Failed" {
		r.MemoryLimits, r.CPULimits = nil, nil
	}
	drafts := b.requestFix(ctx, r)
	if len(drafts) == 0 {
		return nil
	}
	_, err := b.Ledger.Append(ctx, drafts...)
	return err
}

// requestFix asks the Fixer for a fix and records the request. Callers
// hold b.mu.
func (b *Brain) requestFix(ctx context.Context, r FixRequest) []ledger.Draft {
	why := ""
	if b.MergePolicy != nil {
		if level, gated := b.MergePolicy(r.Target.Namespace); level == model.AutonomyAutonomous {
			earned, standing := b.standing(model.TrackKey{Action: ActionFixMerge, FaultClass: r.FaultClass, Service: r.Target.String()})
			r.Merge = !gated || earned
			if !r.Merge {
				why = "; a person merges it, since " + standing
			}
		}
	}
	name, err := b.Fixer.Fix(ctx, r)
	if err != nil {
		b.Log.Error("could not ask the Fixer for a fix at the source", "incident", r.Incident, "error", err)
		return nil
	}
	f := model.Fix{ID: model.FixID(r.Incident), IncidentID: r.Incident, Target: r.Target, FromRevision: r.FromRevision, Phase: "Requested",
		Message: name + why, FaultClass: r.FaultClass, Merge: r.Merge}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindFix, "requested", f.ID, r.Incident, f)}
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
	if f == nil {
		b.Log.Info("the Fixer reported on a fix Sentinel did not ask for", "fix", u.Fix, "phase", u.Phase)
		return nil
	}
	var drafts []ledger.Draft
	codeChanged := u.Code != nil && (f.Code == nil || !sameCode(*f.Code, *u.Code))
	if codeChanged {
		drafts = append(drafts, b.onCode(ctx, f, u)...)
	}
	if f.Phase == u.Phase || f.Phase == "Held" || f.Phase == "NotHeld" {
		if !codeChanged {
			return nil
		}
		_, err := b.Ledger.Append(ctx, append([]ledger.Draft{ledger.RecordDraft(source, model.KindFix, "code", f.ID, u.IncidentID, *f)}, drafts...)...)
		return err
	}
	f.Phase, f.Message, f.Owner, f.Resume, f.Repository = u.Phase, u.Message, u.Owner, u.Resume, u.Repository
	f.PullRequest, f.MergeCommit, f.Change = u.PullRequest, u.MergeCommit, u.Change
	b.Log.Info("fix "+strings.ToLower(u.Phase), "incident", u.IncidentID, "target", f.Target.String(), "pullRequest", u.PullRequest, "message", u.Message)
	target := f.Target.String()
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

func sameCode(a, b model.FixCode) bool {
	return a.PullRequest == b.PullRequest && a.Image == b.Image && a.Verdict == b.Verdict && a.Message == b.Message && a.Attempt == b.Attempt
}

// onCode records a step of the fix to the failing image's code and tells
// people what it needs: a review of its pull request; once the source's
// pipeline has built it, the Lab is asked to race the image. Callers hold
// b.mu.
func (b *Brain) onCode(ctx context.Context, f *model.Fix, u FixUpdate) []ledger.Draft {
	prev, now := f.Code, u.Code
	f.Code = now
	target := f.Target.String()
	var drafts []ledger.Draft
	switch {
	case now.PullRequest != "" && (prev == nil || prev.PullRequest != now.PullRequest):
		b.tell(ctx, notify.KindApproval, u.IncidentID, f.Target, "Sentinel proposes a fix to "+target+"'s code: "+now.PullRequest,
			strings.Join(now.Change, "\n")+"\nOnce your pipeline builds it, the Lab races it against the failing image.")
	case now.Image != "" && (prev == nil || prev.Image != now.Image):
		drafts = append(drafts, b.askCodeLab(ctx, f, now.Image)...)
	case prev != nil && now.Attempt > prev.Attempt:
		b.tell(ctx, notify.KindApproval, u.IncidentID, f.Target, "Sentinel tries another fix to "+target+"'s code: "+now.PullRequest,
			strings.Join(now.Change, "\n")+"\n"+now.Message)
	}
	b.Log.Info("fix to the code", "incident", u.IncidentID, "target", target, "pullRequest", now.PullRequest, "image", now.Image,
		"verdict", now.Verdict, "message", now.Message)
	return drafts
}

// askCodeLab asks the Lab, once, whether the image built from the fix to
// the code fixes the incident; when every Lab namespace is busy, the sweep
// asks again. Callers hold b.mu.
func (b *Brain) askCodeLab(ctx context.Context, f *model.Fix, image string) []ledger.Draft {
	iv := b.ImageLab
	if iv == nil {
		lab, ok := b.Lab.(ImageVerifier)
		if !ok {
			return nil
		}
		iv = lab
	}
	revision := f.Target.Name + "-" + f.FromRevision
	experiment, asked, err := iv.VerifyImage(ctx, f.IncidentID, f.Target, revision, image)
	if err != nil || !asked {
		if err != nil {
			b.Log.Error("could not ask the Lab about a fix to the code", "incident", f.IncidentID, "error", err)
		}
		if b.codeLab == nil {
			b.codeLab = map[model.ID]model.Fix{}
		}
		b.codeLab[f.IncidentID] = *f
		return nil
	}
	delete(b.codeLab, f.IncidentID)
	e := model.Experiment{ID: codeExperimentID(f.IncidentID), IncidentID: f.IncidentID, Purpose: "verify-code", Target: f.Target,
		Revision: revision, Lab: experiment, Phase: "Requested"}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindExperiment, "requested", e.ID, f.IncidentID, e)}
}

func codeExperimentID(incident model.ID) model.ID { return model.ExperimentID(incident) + "/code" }

// crashOf is the newest crash stack, with the application's own frames,
// in the logs of the workload the incident holds as evidence.
func (b *Brain) crashOf(ctx context.Context, incident model.ID, target model.ObjectRef) (*Crash, []string) {
	records, err := ledger.List[model.Evidence](ctx, b.Ledger, model.KindEvidence, incident)
	if err != nil {
		return nil, nil
	}
	var newest *Crash
	var errs []string
	var at time.Time
	for _, e := range records {
		if len(e.Data) == 0 || e.CollectedAt.Before(at) {
			continue
		}
		var data struct {
			Workload model.ObjectRef `json:"workload"`
			Summary  logs.Summary    `json:"summary"`
		}
		if json.Unmarshal(e.Data, &data) != nil || data.Workload.Kind != target.Kind || data.Workload.Namespace != target.Namespace ||
			data.Workload.Name != target.Name {
			continue
		}
		for _, t := range data.Summary.Templates {
			if t.Error && len(errs) < 5 && !slices.Contains(errs, t.Template) {
				errs = append(errs, t.Template)
			}
		}
		for _, ex := range data.Summary.Exceptions {
			var frames []string
			var stack []logs.Frame
			for _, f := range ex.Frames {
				if f.InApp {
					frames = append(frames, f.Function)
					stack = append(stack, f)
				}
			}
			if len(frames) > 0 {
				newest, at = &Crash{Language: ex.Language, Types: ex.Types, Message: ex.Message, Frames: frames, Stack: stack}, e.CollectedAt
			}
		}
	}
	return newest, errs
}

// codeOnlyClasses are crashes in code a rollback cannot stop when no change
// is to blame: a bug that was there before the newest revision.
var codeOnlyClasses = map[string]bool{"runtime.crash": true, "deploy.crash-on-start": true, "runtime.errors": true}

// askCodeFix asks the Fixer, once per incident, for a fix to the code of a
// Deployment that crashes with nothing to undo, once its logs hold a stack
// or errors to start from. Callers hold b.mu.
func (b *Brain) askCodeFix(ctx context.Context, inc *incident, target model.ObjectRef, class string) []ledger.Draft {
	if b.Fixer == nil || !b.CodeFixWithoutRollback || inc.fixAsked || target.Kind != "Deployment" || !codeOnlyClasses[class] {
		return nil
	}
	rev := b.revision(target)
	if rev == "" {
		return nil
	}
	crash, errs := b.crashOf(ctx, inc.rec.ID, target)
	if crash == nil && len(errs) == 0 {
		return nil
	}
	r := FixRequest{Incident: inc.rec.ID, Target: target, FromRevision: rev, FaultClass: class, Crash: crash, Errors: errs, CodeOnly: true}
	name, err := b.Fixer.Fix(ctx, r)
	if err != nil {
		b.Log.Error("could not ask the Fixer for a fix to the code", "incident", inc.rec.ID, "error", err)
		return nil
	}
	inc.fixAsked = true
	b.Log.Info("asked the Fixer for a fix to the code, with nothing to roll back", "incident", inc.rec.ID, "target", target.String())
	f := model.Fix{ID: model.FixID(inc.rec.ID), IncidentID: inc.rec.ID, Target: target, FromRevision: rev, Phase: "Requested",
		Message: name + "; a fix to the code alone, as nothing could be rolled back", FaultClass: class}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindFix, "requested", f.ID, inc.rec.ID, f)}
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

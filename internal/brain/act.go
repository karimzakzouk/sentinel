package brain

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
	"github.com/karimzakzouk/sentinel/internal/stats"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

// Arguments the actor's contracts take.
const (
	argFromTemplateHash = "fromTemplateHash"
	argReplicas         = "replicas"
	argFromReplicas     = "fromReplicas"
)

// actionArgs returns the actor's arguments for undoing the blamed change,
// or nil when the change is not one the action can undo: a rollback undoes
// only the template change that produced the workload's newest revision,
// and a scale only a recorded replica change.
func actionArgs(action string, top *RankedChange, revision string) map[string]string {
	if top == nil {
		return nil
	}
	c := top.Change
	switch {
	case action == ActionRolloutUndo && c.Kind == string(watcher.ChangeTemplate) && c.ProducedNewestRevision && revision != "":
		return map[string]string{argFromTemplateHash: revision}
	case action == ActionScaleRestore && c.Kind == string(watcher.ChangeScale):
		for _, f := range c.Fields {
			before, after, ok := strings.Cut(strings.TrimPrefix(f, "replicas:"), "->")
			if ok && strings.HasPrefix(f, "replicas:") {
				return map[string]string{argReplicas: before, argFromReplicas: after}
			}
		}
	}
	return nil
}

// classLabels name the fault classes for people, as the UI does.
var classLabels = map[string]string{
	ClassBadImage: "Bad image", ClassCrashOnStart: "Crash on start", ClassMemoryLimit: "Memory limit too low",
	ClassProbe: "Broken health check", ClassConfigError: "Configuration error", ClassUnschedulable: "Cannot schedule",
	ClassRuntimeCrash: "Crashing", ClassNewErrors: "New errors after a deploy", ClassDependencyUnavailable: "Dependency unavailable",
	ClassRuntimeErrors: "Errors without a change",
}

func classLabel(c string) string {
	if l, ok := classLabels[c]; ok {
		return l
	}
	return c
}

// verbs say what an action does and did, for people.
var verbs = map[string][2]string{
	ActionRolloutUndo:  {"roll back", "rolled back"},
	ActionScaleRestore: {"restore the replicas of", "restored the replicas of"},
}

func verb(action string, past bool) string {
	v, ok := verbs[action]
	if !ok {
		return action
	}
	if past {
		return v[1]
	}
	return v[0]
}

// notice tells people once per incident and kind. Callers hold b.mu.
func (b *Brain) notice(ctx context.Context, inc *incident, kind, title, detail string) {
	if b.Notify == nil || inc.notified[kind+title] {
		return
	}
	if inc.notified == nil {
		inc.notified = map[string]bool{}
	}
	inc.notified[kind+title] = true
	b.Notify(ctx, notify.Notice{Kind: kind, Incident: inc.rec.ID, Workload: inc.primary, Title: title, Detail: detail})
}

// breakerOpen is how long Sentinel stops asking for an action on a workload
// after it failed or did not clear its incident there.
const breakerOpen = 24 * time.Hour

func breakerKey(action string, target model.ObjectRef) string { return action + " " + target.String() }

// breakerUntil returns when the breaker for an action on a workload closes,
// zero when it is closed. Callers hold b.mu.
func (b *Brain) breakerUntil(action string, target model.ObjectRef) time.Time {
	if until := b.breakers[breakerKey(action, target)]; b.now().Before(until) {
		return until
	}
	return time.Time{}
}

// trip opens the breaker for an action on a workload. Callers hold b.mu.
func (b *Brain) trip(a *model.Action, from time.Time) {
	if b.breakers == nil {
		b.breakers = map[string]time.Time{}
	}
	b.breakers[breakerKey(a.Type, a.Target)] = from.Add(breakerOpen)
	b.Log.Warn("Sentinel stops asking for this action on this workload for a while", "action", a.Type,
		"target", a.Target.String(), "until", from.Add(breakerOpen))
}

// ActionOutcome is what the actor did with a request.
type ActionOutcome struct {
	IncidentID model.ID
	// Request names the request, as namespace/name.
	Request string
	// Phase is the request's final phase, such as Succeeded or Rejected, or
	// Pending while it waits for a person.
	Phase      string
	Message    string
	ApprovedBy string
}

// OnActionOutcome records what the actor did with the incident's action. A
// success starts verification; anything else leaves the incident to people.
func (b *Brain) OnActionOutcome(ctx context.Context, o ActionOutcome) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var inc *incident
	for _, i := range b.open {
		if i.rec.ID == o.IncidentID {
			inc = i
		}
	}
	if inc == nil || inc.action == nil || !inc.actedAt.IsZero() || inc.verdict {
		b.Log.Info("an action finished for an incident no longer open", "incident", o.IncidentID, "phase", o.Phase, "message", o.Message)
		return nil
	}
	a := inc.action
	a.Params["result"] = o.Message
	if o.Request != "" {
		a.Params["request"] = o.Request
	}
	if o.ApprovedBy != "" {
		a.Params["approvedBy"] = o.ApprovedBy
	}
	step := "executed"
	switch o.Phase {
	case "Pending":
		step = "awaiting"
		b.notice(ctx, inc, notify.KindApproval, "Sentinel asks to "+verb(a.Type, false)+" "+a.Target.String(),
			o.Message+"\nApprove with `sentinel approve "+o.Request+"`, or veto with `sentinel veto "+o.Request+"`.")
	case "Succeeded":
		inc.actedAt = b.now()
		if from := a.Params[argFromTemplateHash]; a.Type == ActionRolloutUndo && from != "" {
			b.rollbacks = append(b.rollbacks, rollback{action: *a, incident: inc.rec.ID, from: from, at: inc.actedAt,
				key: model.TrackKey{Action: a.Type, FaultClass: inc.rec.Assessment.Class, Service: inc.primary.String()}})
		}
	default:
		a.State, step = model.ActionFailed, "failed"
		inc.verdict = true
		b.Log.Warn("the actor did not run the action; escalating", "incident", inc.rec.ID, "action", a.Type,
			"phase", o.Phase, "message", o.Message)
		b.trip(a, b.now())
		b.notice(ctx, inc, notify.KindEscalation, "Sentinel could not "+verb(a.Type, false)+" "+a.Target.String(), o.Message)
	}
	b.Log.Info("action "+step, "incident", inc.rec.ID, "action", a.Type, "target", a.Target.String(), "message", o.Message)
	_, err := b.Ledger.Append(ctx, ledger.RecordDraft(source, model.KindAction, step, a.ID, inc.rec.ID, *a))
	return err
}

// verify records whether an executed action cleared its incident's signals
// in time. Callers hold b.mu.
func (b *Brain) verify(inc *incident) []ledger.Draft {
	if inc.action == nil || inc.actedAt.IsZero() || inc.verdict {
		return nil
	}
	now := b.now()
	a := inc.action
	switch {
	case !inc.quietSince.IsZero():
		a.State = model.ActionVerified
		a.Params["verification"] = "the incident's signals cleared " + now.Sub(inc.actedAt).Round(time.Second).String() + " after the action"
	case now.Sub(inc.actedAt) >= verifyWithin:
		a.State = model.ActionFailed
		a.Params["verification"] = "the incident's signals were still active " + verifyWithin.String() + " after the action"
		b.Log.Warn("the action did not clear the incident; escalating", "incident", inc.rec.ID, "action", a.Type)
		b.trip(a, now)
	default:
		return nil
	}
	inc.verdict = true
	step := "verified"
	if a.State == model.ActionFailed {
		step = "unverified"
		b.notice(context.Background(), inc, notify.KindEscalation, "Sentinel "+verb(a.Type, true)+" "+a.Target.String()+
			", but the incident did not clear", a.Params["verification"])
	} else {
		b.notice(context.Background(), inc, notify.KindActed, "Sentinel "+verb(a.Type, true)+" "+a.Target.String()+
			"; the incident cleared", a.Params["verification"])
	}
	b.Log.Info("action "+step, "incident", inc.rec.ID, "action", a.Type, "verification", a.Params["verification"])
	drafts := []ledger.Draft{ledger.RecordDraft(source, model.KindAction, step, a.ID, inc.rec.ID, *a)}
	if inc.rec.Assessment != nil {
		key := model.TrackKey{Action: a.Type, FaultClass: inc.rec.Assessment.Class, Service: inc.primary.String()}
		drafts = append(drafts, b.track(key, a.State == model.ActionVerified, inc.rec.ID, now))
	}
	return drafts
}

// trackTarget is the success rate whose 5th percentile a cell must clear to
// support autonomy: 28 straight verified successes under the uniform prior.
const trackTarget = 0.9

// track adds a verified success or a failed verification to a cell's Beta
// record, which starts uniform. Refusals and approvals do not count: they say
// nothing about whether the action works. The level is what the record
// supports; phase 5 lets it gate autonomy. Callers hold b.mu.
func (b *Brain) track(key model.TrackKey, success bool, incident model.ID, now time.Time) ledger.Draft {
	if b.tracks == nil {
		b.tracks = map[model.TrackKey]*model.TrackRecord{}
	}
	t := b.tracks[key]
	if t == nil {
		t = &model.TrackRecord{Key: key, Alpha: 1, Beta: 1}
		b.tracks[key] = t
	}
	successes, failures := decayed(t, now)
	if success {
		successes++
	} else {
		failures++
	}
	t.Alpha, t.Beta, t.UpdatedAt = 1+successes, 1+failures, now
	t.Level = model.AutonomyApprove
	if b.lowerBound(key, now) >= trackTarget {
		t.Level = model.AutonomyAutonomous
	}
	return ledger.RecordDraft(source, model.KindTrackRecord, "updated", TrackID(key), incident, *t)
}

// trackHalfLife is how long a verified or failed action takes to count half
// as much: workloads change, and old evidence says less about them.
const trackHalfLife = 30 * 24 * time.Hour

// poolWeight is the most a cell borrows from the same action on the same
// fault elsewhere, in runs: enough to move a new workload's record, never
// enough to earn it autonomy alone.
const poolWeight = 5.0

// decayed is a record's successes and failures as of now, each halving
// every trackHalfLife.
func decayed(t *model.TrackRecord, now time.Time) (float64, float64) {
	w := 1.0
	if !t.UpdatedAt.IsZero() && now.After(t.UpdatedAt) {
		w = math.Exp2(-float64(now.Sub(t.UpdatedAt)) / float64(trackHalfLife))
	}
	return (t.Alpha - 1) * w, (t.Beta - 1) * w
}

// lowerBound is the 5th percentile of a cell's success rate as of now: its
// own decayed record, with a prior of up to poolWeight runs at the rate the
// same action shows on the same fault on other workloads. Callers hold b.mu.
func (b *Brain) lowerBound(key model.TrackKey, now time.Time) float64 {
	var own, ownFailed, pooled, pooledFailed float64
	if t := b.tracks[key]; t != nil {
		own, ownFailed = decayed(t, now)
	}
	for k, t := range b.tracks {
		if k != key && k.Action == key.Action && k.FaultClass == key.FaultClass {
			s, f := decayed(t, now)
			pooled, pooledFailed = pooled+s, pooledFailed+f
		}
	}
	rate := (pooled + 1) / (pooled + pooledFailed + 2)
	weight := math.Min(poolWeight, pooled+pooledFailed)
	return stats.BetaQuantile(0.05, 1+own+weight*rate, 1+ownFailed+weight*(1-rate))
}

// TrackID is the Ledger entity a cell's record is kept under.
func TrackID(key model.TrackKey) model.ID { return model.ID("track/" + key.String()) }

// Arguments the Brain passes about an action, which the actor's request
// carries in annotations (act.ParamExperiment, ParamEarned, ParamTrack):
// the Lab Experiment verifying it, and whether its cell's track record has
// earned autonomy, with a sentence for whoever must approve it when not.
const (
	argExperiment = "experiment"
	argEarned     = "earned"
	argTrack      = "track"
)

// standing says whether a cell's track record has earned autonomy, and in
// a sentence for an approver why not. Callers hold b.mu.
func (b *Brain) standing(key model.TrackKey) (bool, string) {
	now := b.now()
	lower := b.lowerBound(key, now)
	if lower >= trackTarget {
		return true, ""
	}
	t := b.tracks[key]
	if t == nil || t.Alpha+t.Beta <= 2 {
		return false, "no " + verb(key.Action, false) + " of this workload for this fault has been verified yet; " +
			"a person approves until enough are"
	}
	successes, failures := decayed(t, now)
	return false, fmt.Sprintf("its track record has not earned autonomy yet: %.0f verified and %.0f not, and at least %.0f%% of "+
		"such actions succeeding is all 95%% confidence allows, short of %.0f%%", successes, failures, 100*lower, 100*trackTarget)
}

// askLab asks the Lab, once per incident, whether rolling back fixes it:
// for a rollback of the Deployment revision blamed for the incident,
// whether or not policy lets Sentinel act. It returns what to record and
// the Experiment that answers, once asked. Callers hold b.mu.
func (b *Brain) askLab(ctx context.Context, inc *incident, decision Decision, args map[string]string) ([]ledger.Draft, string) {
	hash := args[argFromTemplateHash]
	if b.Lab == nil || inc.labAsked || decision.Action != ActionRolloutUndo || decision.Target.Kind != "Deployment" || hash == "" {
		return nil, inc.experiment
	}
	revision := decision.Target.Name + "-" + hash
	experiment, asked, err := b.Lab.Verify(ctx, inc.rec.ID, decision.Target, revision)
	switch {
	case err != nil:
		b.Log.Error("could not ask the Lab to verify a rollback", "incident", inc.rec.ID, "error", err)
		return nil, ""
	case !asked:
		return nil, ""
	}
	inc.labAsked, inc.experiment = true, experiment
	e := model.Experiment{ID: model.ExperimentID(inc.rec.ID), IncidentID: inc.rec.ID, Purpose: "verify-fix", Target: decision.Target,
		Revision: revision, Lab: experiment, Phase: "Requested"}
	return []ledger.Draft{ledger.RecordDraft(source, model.KindExperiment, "requested", e.ID, inc.rec.ID, e)}, experiment
}

// LabVerdict is the Lab's verdict on an incident's proposed rollback.
type LabVerdict struct {
	IncidentID model.ID
	Experiment string
	Phase      string
	Message    string
	Runs       int32
	Fidelity   []string
}

// OnLabVerdict records the Lab's verdict, and tells whoever is asked to
// approve the rollback.
func (b *Brain) OnLabVerdict(ctx context.Context, v LabVerdict) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := ledger.List[model.Experiment](ctx, b.Ledger, model.KindExperiment, "")
	if err != nil {
		return err
	}
	var e *model.Experiment
	for i := range records {
		if records[i].ID == model.ExperimentID(v.IncidentID) {
			e = &records[i]
		}
	}
	if e == nil {
		b.Log.Info("a Lab verdict arrived for an experiment Sentinel did not ask for", "experiment", v.Experiment, "phase", v.Phase)
		return nil
	}
	e.Lab, e.Phase, e.Message, e.Runs, e.Fidelity = v.Experiment, v.Phase, v.Message, v.Runs, v.Fidelity
	b.Log.Info("the Lab judged a rollback", "incident", v.IncidentID, "phase", v.Phase, "message", v.Message, "runs", v.Runs)
	for _, inc := range b.open {
		if inc.rec.ID == v.IncidentID && inc.action != nil && inc.actedAt.IsZero() && !inc.verdict {
			b.notice(ctx, inc, notify.KindApproval, "The Lab on rolling back "+inc.action.Target.String()+": "+labVerdictWord(v.Phase), v.Message)
		}
	}
	_, err = b.Ledger.Append(ctx, ledger.RecordDraft(source, model.KindExperiment, strings.ToLower(v.Phase), e.ID, v.IncidentID, *e))
	return err
}

func labVerdictWord(phase string) string {
	switch phase {
	case "Passed":
		return "it fixes the failure"
	case "Failed":
		return "it does not fix the failure"
	}
	return "not sure"
}

// revertWindow is how long after a rollback a person's undoing of it counts
// against the rollback.
const revertWindow = 24 * time.Hour

// revertJudge is how long a revision someone put back must run without
// trouble before putting it back counts against Sentinel's rollback of it.
// One that fails again shows the rollback was right: a pipeline that
// redeploys a broken change, or an owner resumed before Git was fixed, puts
// back revisions too.
const revertJudge = 10 * time.Minute

// maxRollbacksOfARevision is how many times within the revert window
// Sentinel rolls a workload away from one revision. Past that, whoever
// keeps putting it back is told to fix it at its source instead of Sentinel
// taking turns with them.
const maxRollbacksOfARevision = 2

// rollback is a rollback Sentinel carried out: its cell, and the revision it
// rolled away from. revertedAt is when someone put that revision back, by
// revertedBy; judged is set once the revert has been judged either way.
type rollback struct {
	key        model.TrackKey
	action     model.Action
	incident   model.ID
	from       string
	at         time.Time
	revertedAt time.Time
	revertedBy string
	judged     bool
}

// rollbacksOf counts the rollbacks within the revert window that rolled a
// workload away from a revision. Callers hold b.mu.
func (b *Brain) rollbacksOf(target model.ObjectRef, revision string) int {
	n := 0
	for _, r := range b.rollbacks {
		if r.action.Target == target && r.from == revision && b.now().Sub(r.at) <= revertWindow {
			n++
		}
	}
	return n
}

// reverts finds rollbacks someone undid: within a day, the workload's newest
// revision is again the one Sentinel rolled away from, and its template was
// last changed by someone else. Once the revision has run for revertJudge
// without the Watcher detecting trouble on the workload, the revert counts
// against its cell, opens its breaker and is told; if trouble comes back,
// the rollback stands. Callers hold b.mu.
func (b *Brain) reverts(ctx context.Context, now time.Time) []ledger.Draft {
	var drafts []ledger.Draft
	for key, at := range b.troubledAt {
		if now.Sub(at) > revertWindow {
			delete(b.troubledAt, key)
		}
	}
	kept := b.rollbacks[:0]
	for _, each := range b.rollbacks {
		if now.Sub(each.at) > revertWindow {
			continue
		}
		kept = append(kept, each)
		r := &kept[len(kept)-1]
		target := r.action.Target
		if r.revertedAt.IsZero() {
			last, changed := b.lastTemplate[target.String()]
			if !changed || !last.At.After(r.at) || last.Actor == model.ActorFieldManager || b.revision(target) != r.from {
				continue
			}
			r.revertedAt, r.revertedBy = last.At, cmp.Or(last.Actor, "someone")
			b.Log.Info("someone put back the revision Sentinel rolled away from; Sentinel watches whether it fails again",
				"incident", r.incident, "target", target.String(), "by", r.revertedBy)
		}
		switch {
		case r.judged:
		case b.troubledAt[target.String()].After(r.revertedAt):
			r.judged = true
			b.Log.Info("the revision Sentinel rolled away from failed again once put back; the rollback stands",
				"incident", r.incident, "target", target.String(), "by", r.revertedBy)
		case now.Sub(r.revertedAt) >= revertJudge:
			r.judged = true
			a := r.action
			a.Params = maps.Clone(a.Params)
			a.Params["revertedBy"] = r.revertedBy
			drafts = append(drafts, ledger.RecordDraft(source, model.KindAction, "reverted", a.ID, r.incident, a),
				b.track(r.key, false, r.incident, now))
			b.trip(&a, now)
			b.Log.Warn("a person reverted Sentinel's rollback", "incident", r.incident, "target", target.String(), "by", r.revertedBy)
			if b.Notify != nil {
				b.Notify(ctx, notify.Notice{Kind: notify.KindEscalation, Incident: r.incident, Workload: target,
					Title:  "Sentinel's rollback of " + target.String() + " was reverted by " + r.revertedBy,
					Detail: "The revision Sentinel rolled away from has run without trouble since. Sentinel counts it against rolling back this workload for this fault, and will not roll it back on its own for a day."})
			}
		}
	}
	b.rollbacks = kept
	return drafts
}

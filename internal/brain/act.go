package brain

import (
	"context"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/notify"
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
	return []ledger.Draft{ledger.RecordDraft(source, model.KindAction, step, a.ID, inc.rec.ID, *a)}
}

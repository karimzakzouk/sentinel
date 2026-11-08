package brain

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
	"github.com/karimzakzouk/sentinel/internal/topology"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

const source = "/brain"

// ChangeEventType is the Ledger event type of observed changes.
const ChangeEventType = "sentinel.change.observed"

// Brain groups detections into incidents, explains them and records what it
// would do. In observe mode it never acts: every action it chooses is
// recorded as a shadow action.
type Brain struct {
	Ledger    *ledger.Ledger
	Log       *slog.Logger
	Classify  *slot.Slot[ClassifyInput, slot.Distribution[string]]
	Attribute *slot.Slot[AttributeInput, slot.Ranking[RankedChange]]
	Decide    *slot.Slot[DecideInput, Decision]
	// Revisions reports a workload's newest pod revision, or "" when unknown.
	Revisions func(model.ObjectRef) string
	// Dependents, when set, reports the workloads that depend on one,
	// directly or through others.
	Dependents func(model.ObjectRef) []model.ObjectRef
	// Dependencies, when set, reports the workloads one depends on, with
	// their distance; Localize ranks them as possible root causes.
	Dependencies func(model.ObjectRef) []topology.Reach
	// Deviations, when set, reports how far a workload's resource use is
	// from its usual.
	Deviations func(model.ObjectRef) []watcher.Deviation
	Localize   *slot.Slot[LocalizeInput, slot.Ranking[Suspect]]
	// Lookback is how long changes are kept as candidate causes.
	Lookback time.Duration
	// Gap is how long an incident stays open after its last signal goes
	// quiet, so a fault that pauses (a crash loop's back-off, for one) stays
	// one incident.
	Gap time.Duration
	Now func() time.Time

	mu      sync.Mutex
	changes []watcher.Change
	// open holds incidents by the workload that is their root cause; route
	// maps every workload in an incident, root cause or symptom, to it.
	open  map[string]*incident
	route map[string]*incident
	// resolvedAt holds when each workload's last incident resolved.
	resolvedAt map[string]time.Time
	evidence   map[string][]*pendingEvidence
}

type incident struct {
	rec model.Incident
	// primary is the workload the incident is about; its facts drive the
	// class, the attribution and the decision.
	primary  model.ObjectRef
	episodes map[model.ID]bool
	facts    map[string]int
	// symptomFacts are the facts of workloads whose trouble this incident
	// explains, by workload.
	symptomFacts map[string]map[string]int
	revisions    map[string]int
	lastAction   string
	// quietSince is when the last active signal ended; zero while any is active.
	quietSince time.Time
	// evidenceDigests identify the content of the evidence already attached.
	evidenceDigests map[string]bool
}

// factsOf returns the facts an incident holds for one of its workloads.
func (inc *incident) factsOf(ref model.ObjectRef) map[string]int {
	if ref == inc.primary {
		return inc.facts
	}
	return inc.symptomFacts[ref.String()]
}

// detectionFacts are the fact kinds a detection saw in its window.
func detectionFacts(d watcher.Detection) map[string]int {
	facts := map[string]int{string(d.Kind): d.Count}
	for kind, n := range d.Window {
		facts[string(kind)] = max(facts[string(kind)], n)
	}
	return facts
}

func mergeFacts(into, from map[string]int) {
	for kind, n := range from {
		into[kind] = max(into[kind], n)
	}
}

func (b *Brain) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

// Restore takes back the incidents left open when the controller last
// stopped. Their episodes ended with the old process, so each starts quiet:
// it resolves after the gap unless its workloads' trouble returns, and then
// joins it.
func (b *Brain) Restore(ctx context.Context) (int, error) {
	incidents, err := ledger.List[model.Incident](ctx, b.Ledger, model.KindIncident, "")
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		b.open, b.route = map[string]*incident{}, map[string]*incident{}
	}
	restored := 0
	for _, rec := range incidents {
		if rec.MergedInto != "" || rec.State == model.IncidentResolved || rec.State == model.IncidentClosed || len(rec.Objects) == 0 {
			continue
		}
		inc := &incident{rec: rec, primary: rec.Objects[0], episodes: map[model.ID]bool{}, facts: map[string]int{},
			revisions: map[string]int{}, symptomFacts: map[string]map[string]int{}, quietSince: b.now()}
		if rec.Assessment != nil {
			inc.lastAction = rec.Assessment.Decision
		}
		b.open[inc.primary.String()] = inc
		for _, o := range rec.Objects {
			b.route[o.String()] = inc
		}
		restored++
	}
	return restored, nil
}

// OnChange records a change and keeps it as a candidate cause.
func (b *Brain) OnChange(ctx context.Context, c watcher.Change) error {
	if _, err := b.Ledger.Append(ctx, ledger.Draft{Type: ChangeEventType, Source: "/watcher", Subject: c.Object.String(), Data: c}); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.changes = append(b.changes, c)
	cutoff := b.now().Add(-b.Lookback)
	kept := b.changes[:0]
	for _, ch := range b.changes {
		if ch.At.After(cutoff) {
			kept = append(kept, ch)
		}
	}
	b.changes = kept
	return nil
}

// OnDetection files a detection under the incident it belongs to, opening
// one if none fits, and reassesses the incident. A symptom of a fault in a
// workload it depends on joins that fault's incident.
func (b *Brain) OnDetection(ctx context.Context, d watcher.Detection) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		b.open, b.route = map[string]*incident{}, map[string]*incident{}
	}
	key := d.Workload.String()
	inc := b.route[key]
	now := b.now()

	if d.Ended {
		if inc == nil {
			return nil
		}
		delete(inc.episodes, d.ID)
		if len(inc.episodes) == 0 {
			inc.quietSince = now
		}
		return nil
	}

	verb := "updated"
	if inc == nil {
		if up := b.upstreamIncident(ctx, d); up != nil {
			inc, verb = up, "joined"
			b.route[key] = inc
			inc.rec.Objects = append(inc.rec.Objects, d.Workload)
		} else {
			verb = "opened"
			inc = &incident{
				rec: model.Incident{ID: model.NewID(), Fingerprint: key, State: model.IncidentOpen,
					Title: fmt.Sprintf("%s on %s", d.Kind, key), OpenedAt: d.Onset, Objects: []model.ObjectRef{d.Workload}},
				primary: d.Workload, episodes: map[model.ID]bool{}, facts: map[string]int{}, revisions: map[string]int{},
				symptomFacts: map[string]map[string]int{},
			}
			b.open[key], b.route[key] = inc, inc
		}
	}
	inc.quietSince = time.Time{}
	sig := model.Signal{
		ID: d.ID, Type: string(d.Kind), Object: d.Workload, ObservedAt: d.At, Onset: d.Onset,
		TailProbability: d.TailProbability, Detector: "counts/gamma-poisson-v1", Trust: model.TrustObserved,
		Attributes: map[string]string{"count": fmt.Sprint(d.Count), "alpha": fmt.Sprintf("%.3g", d.Alpha)},
	}
	inc.episodes[d.ID] = true
	symptom := d.Workload != inc.primary
	if symptom {
		if inc.symptomFacts[key] == nil {
			inc.symptomFacts[key] = map[string]int{}
		}
		mergeFacts(inc.symptomFacts[key], detectionFacts(d))
	} else {
		mergeFacts(inc.facts, detectionFacts(d))
		for r, n := range d.Revisions {
			inc.revisions[r] += n
		}
	}
	inc.rec.SignalIDs = append(inc.rec.SignalIDs, sig.ID)
	inc.rec.UpdatedAt = now
	if b.Dependents != nil {
		inc.rec.Impacted = b.Dependents(inc.primary)
	}

	recorded := verb
	if verb == "joined" {
		recorded = "updated"
	}
	evidence := b.evidenceDrafts(inc, key)
	var more []ledger.Draft
	var summary []any
	if !symptom {
		m, s, err := b.assess(ctx, inc, inc.primary)
		if err != nil {
			b.Log.Error("assessment failed; the incident is recorded without it", "incident", inc.rec.ID, "error", err)
		}
		more, summary = append(m, b.absorbDependents(ctx, inc)...), s
	}
	// The incident is recorded after the assessment, which it carries.
	drafts := []ledger.Draft{
		ledger.RecordDraft(source, model.KindSignal, "observed", sig.ID, inc.rec.ID, sig),
		ledger.RecordDraft(source, model.KindIncident, recorded, inc.rec.ID, inc.rec.ID, inc.rec),
	}
	drafts = append(append(drafts, evidence...), more...)
	if _, err := b.Ledger.Append(ctx, drafts...); err != nil {
		return err
	}
	impacted := make([]string, 0, len(inc.rec.Impacted))
	for _, r := range inc.rec.Impacted {
		impacted = append(impacted, r.Name)
	}
	b.Log.Info("incident "+verb, append([]any{"incident", inc.rec.ID, "workload", key, "signal", d.Kind, "impacted", impacted}, summary...)...)
	return nil
}

// upstreamIncident returns the open incident of a workload that d's workload
// depends on, if localization ranks that workload as the likelier cause of
// d's symptoms. Callers hold b.mu.
func (b *Brain) upstreamIncident(ctx context.Context, d watcher.Detection) *incident {
	if b.Dependencies == nil || b.Localize == nil {
		return nil
	}
	troubled := false
	for _, dep := range b.Dependencies(d.Workload) {
		troubled = troubled || b.route[dep.Workload.String()] != nil
	}
	if !troubled {
		return nil
	}
	ranking, err := b.Localize.Decide(ctx, b.rootCauseInput(d.Workload, detectionFacts(d), d.Onset))
	if err != nil || len(ranking.Items) == 0 {
		return nil
	}
	top := ranking.Items[0].Item.Workload
	if top == d.Workload {
		return nil
	}
	return b.route[top.String()]
}

// absorbDependents folds into inc the open incidents of workloads that depend
// on its workload, when localization, re-ranked with inc's evidence, blames
// inc's workload for their symptoms. Callers hold b.mu.
func (b *Brain) absorbDependents(ctx context.Context, inc *incident) []ledger.Draft {
	if b.Dependents == nil || b.Localize == nil {
		return nil
	}
	var drafts []ledger.Draft
	for _, dep := range b.Dependents(inc.primary) {
		other := b.open[dep.String()]
		if other == nil || other == inc {
			continue
		}
		ranking, err := b.Localize.Decide(ctx, b.rootCauseInput(other.primary, other.facts, other.rec.OpenedAt))
		if err != nil || len(ranking.Items) == 0 || b.route[ranking.Items[0].Item.Workload.String()] != inc {
			continue
		}
		drafts = append(drafts, b.merge(other, inc)...)
	}
	return drafts
}

// merge folds one incident into another: its workloads, facts, signals,
// evidence and active episodes move, and it closes, naming where it went.
// Callers hold b.mu.
func (b *Brain) merge(from, into *incident) []ledger.Draft {
	now := b.now()
	for key, inc := range b.route {
		if inc == from {
			b.route[key] = into
		}
	}
	into.rec.Objects = append(into.rec.Objects, from.rec.Objects...)
	if into.symptomFacts[from.primary.String()] == nil {
		into.symptomFacts[from.primary.String()] = map[string]int{}
	}
	mergeFacts(into.symptomFacts[from.primary.String()], from.facts)
	for key, facts := range from.symptomFacts {
		if into.symptomFacts[key] == nil {
			into.symptomFacts[key] = map[string]int{}
		}
		mergeFacts(into.symptomFacts[key], facts)
	}
	for id := range from.episodes {
		into.episodes[id] = true
	}
	if len(into.episodes) > 0 {
		into.quietSince = time.Time{}
	}
	into.rec.SignalIDs = append(into.rec.SignalIDs, from.rec.SignalIDs...)
	into.rec.EvidenceIDs = append(into.rec.EvidenceIDs, from.rec.EvidenceIDs...)
	into.rec.UpdatedAt = now
	delete(b.open, from.primary.String())
	from.rec.State, from.rec.MergedInto, from.rec.UpdatedAt = model.IncidentClosed, into.rec.ID, now
	b.Log.Info("incident merged", "incident", from.rec.ID, "into", into.rec.ID, "workload", from.primary.String(),
		"cause", into.primary.String())
	return []ledger.Draft{
		ledger.RecordDraft(source, model.KindIncident, "merged", from.rec.ID, from.rec.ID, from.rec),
		ledger.RecordDraft(source, model.KindIncident, "updated", into.rec.ID, into.rec.ID, into.rec),
	}
}

// Sweep resolves incidents that have been quiet for the whole gap.
func (b *Brain) Sweep(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneEvidence()
	now := b.now()
	var drafts []ledger.Draft
	for key, inc := range b.open {
		if inc.quietSince.IsZero() || now.Sub(inc.quietSince) < b.Gap {
			continue
		}
		inc.rec.State, inc.rec.UpdatedAt = model.IncidentResolved, now
		drafts = append(drafts, ledger.RecordDraft(source, model.KindIncident, "resolved", inc.rec.ID, inc.rec.ID, inc.rec))
		delete(b.open, key)
		if b.resolvedAt == nil {
			b.resolvedAt = map[string]time.Time{}
		}
		for member, routed := range b.route {
			if routed == inc {
				delete(b.route, member)
				b.resolvedAt[member] = now
			}
		}
		b.Log.Info("incident resolved", "incident", inc.rec.ID, "workload", key)
	}
	if len(drafts) == 0 {
		return nil
	}
	_, err := b.Ledger.Append(ctx, drafts...)
	return err
}

// assess runs the three slots and returns the records to append and a log summary.
func (b *Brain) assess(ctx context.Context, inc *incident, workload model.ObjectRef) ([]ledger.Draft, []any, error) {
	var candidates []CandidateChange
	var groups []string
	seen := map[string]bool{}
	latestTemplate := -1
	for _, c := range b.changes {
		related := c.Object == workload || (c.Kind == watcher.ChangeConfigMap && c.Object.Namespace == workload.Namespace)
		if !related || c.At.After(inc.rec.OpenedAt.Add(time.Minute)) {
			continue
		}
		if c.Kind == watcher.ChangeTemplate && c.Object == workload {
			if latestTemplate < 0 || c.At.After(candidates[latestTemplate].At) {
				latestTemplate = len(candidates)
			}
		}
		candidates = append(candidates, CandidateChange{ID: c.ID, Kind: string(c.Kind), Object: c.Object, Fields: c.Fields, At: c.At, Actor: c.Actor})
		// A change that was already in place when the workload last recovered
		// says little about what kind of fault this is. It stays a candidate,
		// and attribution's timing evidence weighs its age.
		if !c.At.After(b.resolvedAt[workload.String()]) {
			continue
		}
		for _, f := range c.Fields {
			if g := FieldGroup(string(c.Kind), f); g != "other" && !seen[g] {
				seen[g] = true
				groups = append(groups, g)
			}
		}
	}
	if latestTemplate >= 0 {
		candidates[latestTemplate].ProducedNewestRevision = true
	}
	classes, err := b.Classify.Decide(ctx, ClassifyInput{Facts: inc.facts, ChangeFields: groups})
	if err != nil {
		return nil, nil, err
	}
	share := -1.0
	if rev := b.revision(workload); rev != "" {
		total := 0
		for _, n := range inc.revisions {
			total += n
		}
		if total > 0 {
			share = float64(inc.revisions[rev]) / float64(total)
		}
	}
	ranking, err := b.Attribute.Decide(ctx, AttributeInput{Onset: inc.rec.OpenedAt, Classes: classes, Candidates: candidates, NewRevisionShare: share})
	if err != nil {
		return nil, nil, err
	}
	var top *RankedChange
	if len(ranking.Items) > 0 {
		top = &ranking.Items[0].Item
	}
	decision, err := b.Decide.Decide(ctx, DecideInput{Workload: workload, Classes: classes, TopChange: top})
	if err != nil {
		return nil, nil, err
	}

	var drafts []ledger.Draft
	for i, w := range classes {
		if i == 3 {
			break
		}
		h := model.Hypothesis{ID: model.NewID(), IncidentID: inc.rec.ID, FaultClass: w.Value, Object: workload,
			Prior: 1 / float64(len(Classes)), Posterior: w.P, Engine: b.Classify.Champion()}
		if i == 0 && top != nil {
			h.Ratios = top.Ratios
		}
		drafts = append(drafts, ledger.RecordDraft(source, model.KindHypothesis, "proposed", h.ID, inc.rec.ID, h))
	}
	if decision.Action != ActionNone && decision.Action != inc.lastAction {
		a := model.Action{ID: model.NewID(), IncidentID: inc.rec.ID, Type: decision.Action, Target: decision.Target,
			Params: map[string]string{"reason": decision.Reason}, Owner: "kubernetes", State: model.ActionProposed,
			Reversible: slices.Contains(ReversibleActions, decision.Action), DecidedBy: b.Decide.Champion(), Shadow: true}
		drafts = append(drafts, ledger.RecordDraft(source, model.KindAction, "proposed", a.ID, inc.rec.ID, a))
	}
	inc.lastAction = decision.Action

	a := &model.Assessment{Class: classes.Top().Value, ClassP: round(classes.Top().P), Decision: decision.Action}
	summary := []any{"class", a.Class, "classP", a.ClassP, "decision", decision.Action}
	if top != nil {
		a.Culprit = &model.Culprit{ChangeID: top.Change.ID, Kind: top.Change.Kind, Object: top.Change.Object, Fields: top.Change.Fields, P: round(top.P)}
		summary = append(summary, "culprit", top.Change.Object.String(), "culpritFields", top.Change.Fields, "culpritP", a.Culprit.P)
	}
	inc.rec.Assessment = a
	if b.Localize != nil {
		suspects, err := b.Localize.Decide(ctx, b.rootCauseInput(workload, inc.facts, inc.rec.OpenedAt))
		if err != nil {
			return drafts, summary, err
		}
		for _, it := range suspects.Items {
			a.RootCauses = append(a.RootCauses, model.RankedObject{Object: it.Item.Workload, P: round(it.Item.P), InSet: it.InSet})
		}
		if len(a.RootCauses) > 0 {
			summary = append(summary, "suspect", a.RootCauses[0].Object.String(), "suspectP", a.RootCauses[0].P)
		}
	}
	return drafts, summary, nil
}

// rootCauseInput gathers the possible causes of symptoms on a workload: the
// workload, with its own facts, and what it depends on, each with the
// anomalies of whatever incident it is in, its latest change before the
// onset and its resource deviations. Callers hold b.mu.
func (b *Brain) rootCauseInput(workload model.ObjectRef, own map[string]int, onset time.Time) LocalizeInput {
	in := LocalizeInput{Workload: workload}
	candidates := []topology.Reach{{Workload: workload}}
	if b.Dependencies != nil {
		candidates = append(candidates, b.Dependencies(workload)...)
	}
	for _, c := range candidates {
		lc := LocalizeCandidate{Workload: c.Workload, Hops: c.Hops, ChangedBefore: -1}
		if b.Deviations != nil {
			lc.Deviations = b.Deviations(c.Workload)
		}
		facts := own
		if c.Hops > 0 {
			facts = nil
			if other := b.route[c.Workload.String()]; other != nil {
				facts = other.factsOf(c.Workload)
			}
		}
		for kind, n := range facts {
			if n > 0 {
				lc.Anomalous = append(lc.Anomalous, kind)
			}
		}
		sort.Strings(lc.Anomalous)
		for _, ch := range b.changes {
			if ch.Object != c.Workload || ch.At.After(onset.Add(time.Minute)) {
				continue
			}
			if before := onset.Sub(ch.At).Seconds(); lc.ChangedBefore < 0 || before < lc.ChangedBefore {
				lc.ChangedBefore = math.Max(before, 0)
			}
		}
		in.Candidates = append(in.Candidates, lc)
	}
	return in
}

func (b *Brain) revision(w model.ObjectRef) string {
	if b.Revisions == nil {
		return ""
	}
	return b.Revisions(w)
}

func round(p float64) float64 { return float64(int(p*1000+0.5)) / 1000 }

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
	open    map[string]*incident
	// resolvedAt holds when each workload's last incident resolved.
	resolvedAt map[string]time.Time
	evidence   map[string][]*pendingEvidence
}

type incident struct {
	rec        model.Incident
	episodes   map[model.ID]bool
	facts      map[string]int
	revisions  map[string]int
	lastAction string
	// quietSince is when the last active signal ended; zero while any is active.
	quietSince time.Time
	// evidenceDigests identify the content of the evidence already attached.
	evidenceDigests map[string]bool
}

func (b *Brain) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
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

// OnDetection updates the workload's incident and reassesses it.
func (b *Brain) OnDetection(ctx context.Context, d watcher.Detection) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		b.open = map[string]*incident{}
	}
	key := d.Workload.String()
	inc := b.open[key]
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
	if inc != nil {
		inc.quietSince = time.Time{}
	}

	verb := "updated"
	if inc == nil {
		verb = "opened"
		inc = &incident{
			rec: model.Incident{ID: model.NewID(), Fingerprint: key, State: model.IncidentOpen,
				Title: fmt.Sprintf("%s on %s", d.Kind, key), OpenedAt: d.Onset, Objects: []model.ObjectRef{d.Workload}},
			episodes: map[model.ID]bool{}, facts: map[string]int{}, revisions: map[string]int{},
		}
		b.open[key] = inc
	}
	sig := model.Signal{
		ID: d.ID, Type: string(d.Kind), Object: d.Workload, ObservedAt: d.At, Onset: d.Onset,
		TailProbability: d.TailProbability, Detector: "counts/gamma-poisson-v1", Trust: model.TrustObserved,
		Attributes: map[string]string{"count": fmt.Sprint(d.Count), "alpha": fmt.Sprintf("%.3g", d.Alpha)},
	}
	inc.episodes[d.ID] = true
	inc.facts[string(d.Kind)] = max(inc.facts[string(d.Kind)], d.Count)
	for kind, n := range d.Window {
		inc.facts[string(kind)] = max(inc.facts[string(kind)], n)
	}
	for r, n := range d.Revisions {
		inc.revisions[r] += n
	}
	inc.rec.SignalIDs = append(inc.rec.SignalIDs, sig.ID)
	inc.rec.UpdatedAt = now
	if b.Dependents != nil {
		inc.rec.Impacted = b.Dependents(d.Workload)
	}

	evidence := b.evidenceDrafts(inc, key)
	drafts := []ledger.Draft{
		ledger.RecordDraft(source, model.KindSignal, "observed", sig.ID, inc.rec.ID, sig),
		ledger.RecordDraft(source, model.KindIncident, verb, inc.rec.ID, inc.rec.ID, inc.rec),
	}
	drafts = append(drafts, evidence...)
	more, summary, err := b.assess(ctx, inc, d.Workload)
	if err != nil {
		b.Log.Error("assessment failed; the incident is recorded without it", "incident", inc.rec.ID, "error", err)
	}
	if _, err := b.Ledger.Append(ctx, append(drafts, more...)...); err != nil {
		return err
	}
	impacted := make([]string, 0, len(inc.rec.Impacted))
	for _, r := range inc.rec.Impacted {
		impacted = append(impacted, r.Name)
	}
	b.Log.Info("incident "+verb, append([]any{"incident", inc.rec.ID, "workload", key, "signal", d.Kind, "impacted", impacted}, summary...)...)
	return nil
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
		b.resolvedAt[key] = now
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

	summary := []any{"class", classes.Top().Value, "classP", round(classes.Top().P), "decision", decision.Action}
	if top != nil {
		summary = append(summary, "culprit", top.Change.Object.String(), "culpritFields", top.Change.Fields, "culpritP", round(top.P))
	}
	if b.Localize != nil {
		suspects, err := b.Localize.Decide(ctx, b.localizeInput(inc, workload))
		if err != nil {
			return drafts, summary, err
		}
		if len(suspects.Items) > 0 {
			s := suspects.Items[0].Item
			summary = append(summary, "suspect", s.Workload.String(), "suspectP", round(s.P))
		}
	}
	return drafts, summary, nil
}

// localizeInput gathers the root-cause candidates of an incident: its
// workload and what it depends on, each with its own anomalies and latest
// change. Callers hold b.mu.
func (b *Brain) localizeInput(inc *incident, workload model.ObjectRef) LocalizeInput {
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
		facts := inc.facts
		if c.Hops > 0 {
			facts = nil
			if other := b.open[c.Workload.String()]; other != nil {
				facts = other.facts
			}
		}
		for kind, n := range facts {
			if n > 0 {
				lc.Anomalous = append(lc.Anomalous, kind)
			}
		}
		sort.Strings(lc.Anomalous)
		for _, ch := range b.changes {
			if ch.Object != c.Workload || ch.At.After(inc.rec.OpenedAt.Add(time.Minute)) {
				continue
			}
			if before := inc.rec.OpenedAt.Sub(ch.At).Seconds(); lc.ChangedBefore < 0 || before < lc.ChangedBefore {
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

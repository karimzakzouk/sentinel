// Package brain explains incidents and decides what to do about them. Every
// decision point is a slot; the engines here are the deterministic defaults.
package brain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
)

// Fault classes the classifier distinguishes.
const (
	ClassBadImage      = "deploy.bad-image"
	ClassCrashOnStart  = "deploy.crash-on-start"
	ClassMemoryLimit   = "resource.memory-limit"
	ClassProbe         = "config.probe"
	ClassConfigError   = "config.error"
	ClassUnschedulable = "capacity.unschedulable"
	ClassRuntimeCrash  = "runtime.crash"
)

// Classes lists the fault classes in a stable order.
var Classes = []string{ClassBadImage, ClassCrashOnStart, ClassMemoryLimit, ClassProbe, ClassConfigError, ClassUnschedulable, ClassRuntimeCrash}

// ChangeInducedClasses are the classes a rollback can mitigate.
var ChangeInducedClasses = []string{ClassBadImage, ClassCrashOnStart, ClassMemoryLimit, ClassProbe, ClassConfigError}

// ClassifyInput is the evidence for the fault-class decision.
type ClassifyInput struct {
	// Facts holds the fact kinds seen in the incident, each with the largest
	// count seen in one window, whether or not the kind was significant.
	Facts map[string]int `json:"facts"`
	// ChangeFields holds the field groups of recent changes on the
	// incident's workloads, such as "image", "resources", "env" or "configmap".
	ChangeFields []string `json:"changeFields,omitempty"`
}

// likelihoods[class][feature] is P(feature is present | class). They are
// expert estimates, to be calibrated against the scenario pool's outcomes.
var likelihoods = map[string]map[string]float64{
	ClassBadImage: {"pod.image-pull-error": 0.95, "pod.crash-loop": 0.05, "pod.oom-killed": 0.02, "pod.not-ready": 0.1, "pod.probe-failed": 0.1,
		"pod.config-error": 0.05, "pod.unschedulable": 0.02, "pod.restarted": 0.05,
		"image": 0.9, "resources": 0.1, "probes": 0.1, "config": 0.2, "scale": 0.05, "none": 0.05},
	ClassCrashOnStart: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.95, "pod.oom-killed": 0.05, "pod.not-ready": 0.3, "pod.probe-failed": 0.3,
		"pod.config-error": 0.05, "pod.unschedulable": 0.02, "pod.restarted": 0.9,
		"image": 0.5, "resources": 0.1, "probes": 0.1, "config": 0.6, "scale": 0.05, "none": 0.1},
	ClassMemoryLimit: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.8, "pod.oom-killed": 0.95, "pod.not-ready": 0.3, "pod.probe-failed": 0.2,
		"pod.config-error": 0.02, "pod.unschedulable": 0.05, "pod.restarted": 0.3,
		"image": 0.2, "resources": 0.7, "probes": 0.05, "config": 0.2, "scale": 0.05, "none": 0.3},
	ClassProbe: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.1, "pod.oom-killed": 0.02, "pod.not-ready": 0.95, "pod.probe-failed": 0.9,
		"pod.config-error": 0.02, "pod.unschedulable": 0.02, "pod.restarted": 0.2,
		"image": 0.2, "resources": 0.05, "probes": 0.8, "config": 0.2, "scale": 0.05, "none": 0.1},
	ClassConfigError: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.1, "pod.oom-killed": 0.02, "pod.not-ready": 0.1, "pod.probe-failed": 0.05,
		"pod.config-error": 0.95, "pod.unschedulable": 0.02, "pod.restarted": 0.05,
		"image": 0.2, "resources": 0.05, "probes": 0.05, "config": 0.7, "scale": 0.05, "none": 0.2},
	ClassUnschedulable: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.02, "pod.oom-killed": 0.02, "pod.not-ready": 0.1, "pod.probe-failed": 0.05,
		"pod.config-error": 0.02, "pod.unschedulable": 0.95, "pod.restarted": 0.02,
		"image": 0.1, "resources": 0.5, "probes": 0.05, "config": 0.1, "scale": 0.6, "none": 0.3},
	ClassRuntimeCrash: {"pod.image-pull-error": 0.02, "pod.crash-loop": 0.7, "pod.oom-killed": 0.1, "pod.not-ready": 0.4, "pod.probe-failed": 0.4,
		"pod.config-error": 0.02, "pod.unschedulable": 0.02, "pod.restarted": 0.9,
		"image": 0.1, "resources": 0.05, "probes": 0.05, "config": 0.1, "scale": 0.05, "none": 0.8},
}

var factFeatures = []string{"pod.image-pull-error", "pod.crash-loop", "pod.oom-killed", "pod.not-ready", "pod.probe-failed", "pod.config-error", "pod.unschedulable", "pod.restarted"}

var changeFeatures = []string{"image", "resources", "probes", "config", "scale"}

// NaiveBayes is the default classifier: a naive-Bayes model over which facts
// and which kinds of recent change are present, with uniform class priors.
type NaiveBayes struct{}

func (NaiveBayes) ID() string { return "naive-bayes-v1" }

func (NaiveBayes) Decide(_ context.Context, in ClassifyInput) (slot.Distribution[string], error) {
	present := map[string]bool{}
	for k, n := range in.Facts {
		present[k] = n > 0
	}
	for _, f := range in.ChangeFields {
		present[f] = true
	}
	anyChange := len(in.ChangeFields) > 0
	logPost := make([]float64, len(Classes))
	for i, c := range Classes {
		lp := 0.0
		for _, f := range factFeatures {
			lp += logBernoulli(likelihoods[c][f], present[f])
		}
		if anyChange {
			for _, f := range changeFeatures {
				lp += logBernoulli(likelihoods[c][f], present[f])
			}
		} else {
			lp += math.Log(likelihoods[c]["none"])
		}
		logPost[i] = lp
	}
	probs := softmax(logPost)
	out := make(slot.Distribution[string], len(Classes))
	for i, c := range Classes {
		out[i] = slot.Weighted[string]{Value: c, P: probs[i]}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].P > out[j].P })
	return out, nil
}

func logBernoulli(p float64, present bool) float64 {
	if present {
		return math.Log(p)
	}
	return math.Log(1 - p)
}

func softmax(logs []float64) []float64 {
	maxLog := math.Inf(-1)
	for _, l := range logs {
		maxLog = math.Max(maxLog, l)
	}
	var sum float64
	out := make([]float64, len(logs))
	for i, l := range logs {
		out[i] = math.Exp(l - maxLog)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

// FieldGroup maps a change field such as "image:web" or "key:app.conf" to the
// group the classifier and attribution use.
func FieldGroup(kind, field string) string {
	if kind == "configmap" {
		return "config"
	}
	if kind == "scale" {
		return "scale"
	}
	name := strings.TrimPrefix(field, "init:")
	switch {
	case strings.HasPrefix(name, "image:"):
		return "image"
	case strings.HasPrefix(name, "resources:"):
		return "resources"
	case strings.HasPrefix(name, "probes:"):
		return "probes"
	case strings.HasPrefix(name, "env:"), strings.HasPrefix(name, "args:"), name == "volumes", strings.HasPrefix(name, "mounts:"):
		return "config"
	default:
		return "other"
	}
}

// CandidateChange is a recent change that may have caused an incident.
type CandidateChange struct {
	ID     model.ID        `json:"id"`
	Kind   string          `json:"kind"`
	Object model.ObjectRef `json:"object"`
	Fields []string        `json:"fields,omitempty"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor,omitempty"`
	// ProducedNewestRevision marks the latest template change of the failing
	// workload: the one that produced the revision its new pods run.
	ProducedNewestRevision bool `json:"producedNewestRevision,omitempty"`
}

// AttributeInput is the evidence for the "what changed?" decision.
type AttributeInput struct {
	Onset      time.Time                 `json:"onset"`
	Classes    slot.Distribution[string] `json:"classes"`
	Candidates []CandidateChange         `json:"candidates,omitempty"`
	// NewRevisionShare is the share of the incident's facts that come from
	// pods on the workload's newest revision; negative when unknown.
	NewRevisionShare float64 `json:"newRevisionShare"`
}

// RankedChange is one attributed change with its readable evidence.
type RankedChange struct {
	Change CandidateChange         `json:"change"`
	Ratios []model.LikelihoodRatio `json:"ratios"`
	// LogOdds is the log of the product of the ratios.
	LogOdds float64 `json:"logOdds"`
	// P is the posterior probability that this change caused the incident.
	P float64 `json:"p"`
}

// LikelihoodRatios is the default attribution engine. Each candidate's odds of
// being the cause multiply readable likelihood ratios: how relevant its type
// is to the likely fault classes, how soon the trouble started after it, and,
// for template changes, how concentrated the failing pods are on the new
// revision. The closest change in time alone is a poor guess (9.8% correct at
// ING), so time is only one factor of three.
type LikelihoodRatios struct {
	// Lookback is how far back changes are considered; Typical is the usual
	// delay between a bad change and its first symptoms.
	Lookback, Typical time.Duration
}

func (e LikelihoodRatios) ID() string { return "likelihood-ratios-v1" }

// Coverage is the confidence set's target probability of containing the cause.
const Coverage = 0.9

func (e LikelihoodRatios) Decide(_ context.Context, in AttributeInput) (slot.Ranking[RankedChange], error) {
	var ranked []RankedChange
	for _, c := range in.Candidates {
		gap := in.Onset.Sub(c.At)
		if gap > e.Lookback {
			continue
		}
		var ratios []model.LikelihoodRatio
		// Relevance: how much more likely this kind of change is under the
		// incident's classes than under an arbitrary class.
		groups := map[string]bool{}
		for _, f := range c.Fields {
			groups[FieldGroup(c.Kind, f)] = true
		}
		rel := 0.0
		for g := range groups {
			rel = math.Max(rel, relevance(in.Classes, g))
		}
		ratios = append(ratios, model.LikelihoodRatio{Factor: "change type fits the fault class", Ratio: math.Max(rel, 0.05)})
		// Timing: exponential delay with the typical scale if this is the
		// cause, uniform over the lookback otherwise. A change after the onset
		// can only be the cause within clock skew.
		var timing float64
		switch {
		case gap < -30*time.Second:
			timing = 0.01
		case gap < 0:
			timing = e.Lookback.Seconds() / e.Typical.Seconds()
		default:
			timing = e.Lookback.Seconds() / e.Typical.Seconds() * math.Exp(-gap.Seconds()/e.Typical.Seconds())
		}
		ratios = append(ratios, model.LikelihoodRatio{Factor: fmt.Sprintf("symptoms began %s after it", gap.Round(time.Second)), Ratio: math.Max(timing, 1e-6)})
		if c.Kind == "template" && in.NewRevisionShare >= 0 {
			// Only the latest template change produced the revision the new pods
			// run. Facts on it implicate that change and clear the older ones.
			share, factor := in.NewRevisionShare, "%.0f%% of failing facts are on the revision this change produced"
			if !c.ProducedNewestRevision {
				share, factor = 1-in.NewRevisionShare, "%.0f%% of failing facts are on older revisions than the newest"
			}
			ratios = append(ratios, model.LikelihoodRatio{Factor: fmt.Sprintf(factor, 100*share), Ratio: (share*0.9 + 0.05) / 0.5})
		}
		logOdds := 0.0
		for _, r := range ratios {
			logOdds += math.Log(r.Ratio)
		}
		ranked = append(ranked, RankedChange{Change: c, Ratios: ratios, LogOdds: logOdds})
	}
	if len(ranked) == 0 {
		return slot.Ranking[RankedChange]{Coverage: Coverage}, nil
	}
	// Posteriors include "none of these changes", with prior odds 1.
	total := 1.0
	for _, r := range ranked {
		total += math.Exp(r.LogOdds)
	}
	for i := range ranked {
		ranked[i].P = math.Exp(ranked[i].LogOdds) / total
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].LogOdds > ranked[j].LogOdds })
	out := slot.Ranking[RankedChange]{Coverage: Coverage}
	covered := 0.0
	for _, r := range ranked {
		out.Items = append(out.Items, slot.Ranked[RankedChange]{Item: r, Score: r.LogOdds, InSet: covered < Coverage})
		covered += r.P
	}
	return out, nil
}

func relevance(classes slot.Distribution[string], group string) float64 {
	if group == "other" {
		return 0.5
	}
	var mixed, base float64
	for _, w := range classes {
		mixed += w.P * likelihoods[w.Value][group]
	}
	for _, c := range Classes {
		base += likelihoods[c][group] / float64(len(Classes))
	}
	if base == 0 {
		return 1
	}
	return mixed / base
}

// Action types the Brain chooses among.
const (
	ActionRolloutUndo = "rollout.undo"
	ActionNone        = "none"
	ActionEscalate    = "escalate"
)

// DecideInput is what the expected-loss decision needs.
type DecideInput struct {
	Workload model.ObjectRef           `json:"workload"`
	Classes  slot.Distribution[string] `json:"classes"`
	// TopChange is the most likely culprit, if any.
	TopChange *RankedChange `json:"topChange,omitempty"`
}

// Decision is the chosen action with the expected loss of every option.
type Decision struct {
	Action       string             `json:"action"`
	Target       model.ObjectRef    `json:"target"`
	ExpectedLoss map[string]float64 `json:"expectedLoss"`
	Reason       string             `json:"reason"`
}

// Validate checks the decision names a known action with finite losses.
func (d Decision) Validate() error {
	switch d.Action {
	case ActionRolloutUndo, ActionNone, ActionEscalate:
	default:
		return fmt.Errorf("unknown action %q", d.Action)
	}
	for a, l := range d.ExpectedLoss {
		if math.IsNaN(l) || math.IsInf(l, 0) {
			return fmt.Errorf("expected loss of %s is not finite", a)
		}
	}
	if len(d.ExpectedLoss) == 0 {
		return errors.New("no expected losses")
	}
	return nil
}

// ExpectedLoss is the default decision engine. Losses are in minutes of
// user-facing impact: an unmitigated incident costs Impact, a person's
// involvement costs Escalation plus slower mitigation, and a rollback costs
// Disruption, plus the unmitigated impact when it does not help.
type ExpectedLoss struct {
	Impact, Escalation, Slower, Disruption float64
	// PReal is the probability that a detected incident is real; detection
	// already required a significant count.
	PReal float64
}

func (ExpectedLoss) ID() string { return "expected-loss-v1" }

func (e ExpectedLoss) Decide(_ context.Context, in DecideInput) (Decision, error) {
	pHelps := 0.0
	if in.TopChange != nil && in.TopChange.Change.Kind == "template" && in.TopChange.Change.Object == in.Workload {
		induced := 0.0
		for _, c := range ChangeInducedClasses {
			induced += in.Classes.P(c)
		}
		pHelps = induced * in.TopChange.P
	}
	losses := map[string]float64{
		ActionNone:     e.PReal * e.Impact,
		ActionEscalate: e.Escalation + e.PReal*e.Slower,
	}
	if pHelps > 0 {
		losses[ActionRolloutUndo] = e.Disruption + (1-pHelps)*e.PReal*e.Impact
	}
	best := ActionNone
	for _, a := range []string{ActionRolloutUndo, ActionEscalate, ActionNone} {
		if l, ok := losses[a]; ok && l < losses[best] {
			best = a
		}
	}
	reason := fmt.Sprintf("a rollback would help with probability %.2f", pHelps)
	if pHelps == 0 {
		reason = "no recent template change on this workload explains it"
	}
	return Decision{Action: best, Target: in.Workload, ExpectedLoss: losses, Reason: reason}, nil
}

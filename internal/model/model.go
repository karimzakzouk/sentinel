// Package model defines Sentinel's core records. Every module speaks in these
// types, and the Ledger stores every change to them as an event.
//
// The chain is: Signal → Incident ← Evidence → Hypothesis → Action → Verdict
// → Fix → TrackRecord.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// ActorFieldManager names the actor's writes in managedFields, so that a
// change Sentinel made is told from the changes it explains.
const ActorFieldManager = "sentinel-actor"

// ID identifies a record. IDs are UUIDv7, so they sort by creation time.
type ID string

// NewID returns a fresh time-ordered ID.
func NewID() ID {
	return ID(uuid.Must(uuid.NewV7()).String())
}

// Kind names a record type. It is also the entity kind in the Ledger.
type Kind string

const (
	KindSignal      Kind = "signal"
	KindIncident    Kind = "incident"
	KindEvidence    Kind = "evidence"
	KindHypothesis  Kind = "hypothesis"
	KindAction      Kind = "action"
	KindVerdict     Kind = "verdict"
	KindFix         Kind = "fix"
	KindTrackRecord Kind = "trackrecord"
)

// ObjectRef points at a Kubernetes object.
type ObjectRef struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

func (r ObjectRef) String() string {
	kind := r.Kind
	if r.Group != "" {
		kind += "." + r.Group
	}
	if r.Namespace == "" {
		return kind + "/" + r.Name
	}
	return kind + "/" + r.Namespace + "/" + r.Name
}

// Trust says how far a piece of data can be believed. Text written by
// workloads (log lines, event messages) is untrusted: it can carry injected
// instructions and must never drive a decision on its own.
type Trust string

const (
	TrustObserved  Trust = "observed"
	TrustDerived   Trust = "derived"
	TrustUntrusted Trust = "untrusted"
)

// Signal is one anomaly or fact a detector reported.
type Signal struct {
	ID         ID        `json:"id"`
	Type       string    `json:"type"`
	Object     ObjectRef `json:"object"`
	ObservedAt time.Time `json:"observedAt"`
	Onset      time.Time `json:"onset"`
	// TailProbability is the chance of seeing something at least this extreme
	// under normal behavior. Smaller means more anomalous.
	TailProbability float64           `json:"tailProbability"`
	Detector        string            `json:"detector"`
	Trust           Trust             `json:"trust"`
	Attributes      map[string]string `json:"attributes,omitempty"`
}

// IncidentState is a step in the incident state machine.
type IncidentState string

const (
	IncidentOpen       IncidentState = "open"
	IncidentMitigating IncidentState = "mitigating"
	IncidentMitigated  IncidentState = "mitigated"
	IncidentFixing     IncidentState = "fixing"
	IncidentResolved   IncidentState = "resolved"
	IncidentEscalated  IncidentState = "escalated"
	IncidentClosed     IncidentState = "closed"
)

var incidentTransitions = map[IncidentState][]IncidentState{
	IncidentOpen:       {IncidentMitigating, IncidentFixing, IncidentResolved, IncidentEscalated, IncidentClosed},
	IncidentMitigating: {IncidentMitigated, IncidentOpen, IncidentEscalated},
	IncidentMitigated:  {IncidentFixing, IncidentResolved, IncidentOpen, IncidentEscalated},
	IncidentFixing:     {IncidentResolved, IncidentMitigated, IncidentEscalated},
	IncidentResolved:   {IncidentClosed, IncidentOpen},
	IncidentEscalated:  {IncidentMitigating, IncidentFixing, IncidentResolved, IncidentClosed},
	IncidentClosed:     {},
}

// CanTransition reports whether the state machine allows moving from one
// incident state to another.
func CanTransition(from, to IncidentState) bool {
	for _, s := range incidentTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Incident groups the signals that share one underlying problem.
type Incident struct {
	ID          ID            `json:"id"`
	Fingerprint string        `json:"fingerprint"`
	State       IncidentState `json:"state"`
	Title       string        `json:"title"`
	OpenedAt    time.Time     `json:"openedAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	Objects     []ObjectRef   `json:"objects,omitempty"`
	// Impacted are the workloads that depend on the incident's objects,
	// directly or through others.
	Impacted    []ObjectRef `json:"impacted,omitempty"`
	SignalIDs   []ID        `json:"signalIds,omitempty"`
	EvidenceIDs []ID        `json:"evidenceIds,omitempty"`
	// MergedInto names the incident this one was folded into when its
	// symptoms turned out to share that incident's cause.
	MergedInto ID `json:"mergedInto,omitempty"`
	// Assessment is the Brain's latest explanation of the incident.
	Assessment *Assessment `json:"assessment,omitempty"`
}

// Assessment is what the Brain concluded about an incident at one moment:
// what kind of fault, which change caused it, which workload is the root
// cause, and what it would do.
type Assessment struct {
	Class  string  `json:"class"`
	ClassP float64 `json:"classP"`
	// Culprit is the likeliest causing change, if any.
	Culprit *Culprit `json:"culprit,omitempty"`
	// RootCauses ranks the workloads that may be the root cause, most likely
	// first; InSet marks the confidence set.
	RootCauses []RankedObject `json:"rootCauses,omitempty"`
	Decision   string         `json:"decision"`
}

// Culprit is a change blamed for an incident.
type Culprit struct {
	ChangeID ID        `json:"changeId"`
	Kind     string    `json:"kind"`
	Object   ObjectRef `json:"object"`
	Fields   []string  `json:"fields,omitempty"`
	P        float64   `json:"p"`
}

// RankedObject is one entry of a ranking with its probability.
type RankedObject struct {
	Object ObjectRef `json:"object"`
	P      float64   `json:"p"`
	InSet  bool      `json:"inSet"`
}

// Evidence is one piece of collected data attached to an incident. Only the
// hash and a redacted summary live here; raw content stays out of the Ledger.
type Evidence struct {
	ID         ID     `json:"id"`
	IncidentID ID     `json:"incidentId"`
	Source     string `json:"source"`
	// Object is what the evidence is about, such as a pod.
	Object  ObjectRef `json:"object,omitzero"`
	Trust   Trust     `json:"trust"`
	SHA256  string    `json:"sha256"`
	Summary string    `json:"summary"`
	// Data is the structured, redacted content, such as a log's templates
	// and exceptions.
	Data        json.RawMessage `json:"data,omitempty"`
	CollectedAt time.Time       `json:"collectedAt"`
	Redacted    bool            `json:"redacted"`
}

// LikelihoodRatio is one readable factor in a hypothesis's score: how much
// more likely a piece of evidence is if the hypothesis is true than if not.
type LikelihoodRatio struct {
	EvidenceID ID      `json:"evidenceId,omitempty"`
	Factor     string  `json:"factor"`
	Ratio      float64 `json:"ratio"`
}

// Hypothesis is one candidate explanation for an incident.
type Hypothesis struct {
	ID         ID                `json:"id"`
	IncidentID ID                `json:"incidentId"`
	FaultClass string            `json:"faultClass"`
	Object     ObjectRef         `json:"object"`
	Prior      float64           `json:"prior"`
	Posterior  float64           `json:"posterior"`
	Ratios     []LikelihoodRatio `json:"ratios,omitempty"`
	Engine     string            `json:"engine"`
}

// ActionState is a step in an action's contract lifecycle.
type ActionState string

const (
	ActionProposed     ActionState = "proposed"
	ActionApproved     ActionState = "approved"
	ActionRejected     ActionState = "rejected"
	ActionDryRunPassed ActionState = "dry-run-passed"
	ActionExecuting    ActionState = "executing"
	ActionExecuted     ActionState = "executed"
	ActionVerified     ActionState = "verified"
	ActionUndone       ActionState = "undone"
	ActionFailed       ActionState = "failed"
)

// Action is a mitigation Sentinel proposed or took.
type Action struct {
	ID           ID                `json:"id"`
	IncidentID   ID                `json:"incidentId"`
	HypothesisID ID                `json:"hypothesisId,omitempty"`
	Type         string            `json:"type"`
	Target       ObjectRef         `json:"target"`
	Params       map[string]string `json:"params,omitempty"`
	Owner        string            `json:"owner"`
	State        ActionState       `json:"state"`
	Reversible   bool              `json:"reversible"`
	DecidedBy    string            `json:"decidedBy"`
	// Shadow marks an action Sentinel would have taken but did not, because
	// its autonomy level for this action is observe.
	Shadow bool `json:"shadow,omitempty"`
}

// Outcome is the result of a verification or a Lab experiment.
type Outcome string

const (
	OutcomePass    Outcome = "pass"
	OutcomeFail    Outcome = "fail"
	OutcomeNotSure Outcome = "not-sure"
)

// Verdict is a statistical judgment with its error bounds.
type Verdict struct {
	ID        ID        `json:"id"`
	SubjectID ID        `json:"subjectId"`
	Kind      string    `json:"kind"`
	Outcome   Outcome   `json:"outcome"`
	Alpha     float64   `json:"alpha"`
	Estimate  float64   `json:"estimate"`
	Lower     float64   `json:"lower"`
	Upper     float64   `json:"upper"`
	Method    string    `json:"method"`
	DecidedAt time.Time `json:"decidedAt"`
}

// Validate checks that the verdict carries a usable error level and interval.
func (v Verdict) Validate() error {
	var errs []error
	if v.Alpha <= 0 || v.Alpha >= 1 {
		errs = append(errs, fmt.Errorf("alpha %v is outside (0, 1)", v.Alpha))
	}
	if math.IsNaN(v.Lower) || math.IsNaN(v.Upper) || v.Lower > v.Upper {
		errs = append(errs, fmt.Errorf("interval [%v, %v] is invalid", v.Lower, v.Upper))
	}
	switch v.Outcome {
	case OutcomePass, OutcomeFail, OutcomeNotSure:
	default:
		errs = append(errs, fmt.Errorf("unknown outcome %q", v.Outcome))
	}
	return errors.Join(errs...)
}

// FixState is a step in a fix's delivery.
type FixState string

const (
	FixProposed  FixState = "proposed"
	FixVerified  FixState = "verified"
	FixRejected  FixState = "rejected"
	FixPROpen    FixState = "pr-open"
	FixMerged    FixState = "merged"
	FixRolledOut FixState = "rolled-out"
	FixReverted  FixState = "reverted"
)

// Fix is a config or code change that addresses an incident's cause.
type Fix struct {
	ID          ID       `json:"id"`
	IncidentID  ID       `json:"incidentId"`
	Kind        string   `json:"kind"`
	Repository  string   `json:"repository,omitempty"`
	BaseCommit  string   `json:"baseCommit,omitempty"`
	PatchSHA256 string   `json:"patchSha256,omitempty"`
	VerdictID   ID       `json:"verdictId,omitempty"`
	PullRequest string   `json:"pullRequest,omitempty"`
	State       FixState `json:"state"`
}

// AutonomyLevel is one rung of the per-namespace autonomy ladder.
type AutonomyLevel string

const (
	AutonomyObserve         AutonomyLevel = "observe"
	AutonomyRecommend       AutonomyLevel = "recommend"
	AutonomyApprove         AutonomyLevel = "approve"
	AutonomyActUnlessVetoed AutonomyLevel = "act-unless-vetoed"
	AutonomyAutonomous      AutonomyLevel = "autonomous"
)

// TrackKey identifies one cell of the track-record table.
type TrackKey struct {
	Action     string `json:"action"`
	FaultClass string `json:"faultClass"`
	Service    string `json:"service"`
}

func (k TrackKey) String() string {
	return k.Action + "|" + k.FaultClass + "|" + k.Service
}

// TrackRecord is the Beta posterior of an action's success rate in one cell.
type TrackRecord struct {
	Key       TrackKey      `json:"key"`
	Alpha     float64       `json:"alpha"`
	Beta      float64       `json:"beta"`
	Level     AutonomyLevel `json:"level"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// Mean is the posterior mean success rate.
func (t TrackRecord) Mean() float64 {
	if t.Alpha+t.Beta == 0 {
		return 0
	}
	return t.Alpha / (t.Alpha + t.Beta)
}

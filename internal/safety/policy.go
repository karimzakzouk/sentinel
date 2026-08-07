package safety

import "github.com/karimzakzouk/sentinel/internal/schemas"

// Policy is the embedded safety policy. Fields are public so the API layer
// can render them in /policy responses, but they should only be mutated via
// the constructors and WithHighRiskServices.
type Policy struct {
	ProductionNamespaces map[string]bool
	HighRiskServices     map[string]bool

	// ConfidenceAutonomous is the threshold above which a production fix is
	// auto-applied without human approval (when all other gates pass).
	ConfidenceAutonomous float64

	// ConfidenceHumanReview is the threshold above which a production fix
	// is sent to RequireHumanApproval. Between ConfidenceHumanReview and
	// ConfidenceAutonomous, a prod fix waits for a human to approve.
	ConfidenceHumanReview float64

	// MaxReplicaChangePct caps the percent change in replicas that may be
	// auto-applied. Larger changes route to RequireHumanApproval.
	MaxReplicaChangePct int
}

// DefaultPolicy returns the built-in defaults. Operators override individual
// fields via NewPolicy or the With* builders.
func DefaultPolicy() *Policy {
	return &Policy{
		ProductionNamespaces: map[string]bool{
			"prod": true, "production": true,
		},
		HighRiskServices:      map[string]bool{},
		ConfidenceAutonomous:  0.92,
		ConfidenceHumanReview: 0.70,
		MaxReplicaChangePct:   20,
	}
}

// NewPolicy returns a policy with the given production namespaces and
// confidence thresholds. Pass 0 for either threshold to keep the default.
// HighRiskServices starts empty — use NewPolicyWithHighRisk or
// WithHighRiskServices to populate it.
func NewPolicy(productionNamespaces []string, confidenceAutonomous, confidenceHumanReview float64) *Policy {
	p := DefaultPolicy()
	p.ProductionNamespaces = map[string]bool{}
	for _, ns := range productionNamespaces {
		p.ProductionNamespaces[ns] = true
	}
	p.HighRiskServices = map[string]bool{}
	if confidenceAutonomous > 0 {
		p.ConfidenceAutonomous = confidenceAutonomous
	}
	if confidenceHumanReview > 0 {
		p.ConfidenceHumanReview = confidenceHumanReview
	}
	return p
}

// NewPolicyWithHighRisk returns a policy with the given production
// namespaces, confidence thresholds, and high-risk services. This is the
// constructor operators should use when they want RequireHumanApproval on
// specific services (e.g. payment-api, billing-api) regardless of
// confidence.
func NewPolicyWithHighRisk(productionNamespaces []string, confidenceAutonomous, confidenceHumanReview float64, highRiskServices []string) *Policy {
	p := NewPolicy(productionNamespaces, confidenceAutonomous, confidenceHumanReview)
	for _, svc := range highRiskServices {
		p.HighRiskServices[svc] = true
	}
	return p
}

// WithHighRiskServices replaces the high-risk services list. Pass nil/empty
// to clear it (every service can be auto-fixed if confidence is high
// enough).
func (p *Policy) WithHighRiskServices(services []string) *Policy {
	p.HighRiskServices = map[string]bool{}
	for _, svc := range services {
		p.HighRiskServices[svc] = true
	}
	return p
}

// Input is the data the policy evaluates.
type Input struct {
	FixType            schemas.FixType
	TargetService      string
	TargetNamespace    string
	Confidence         float64
	HasSchemaMigration bool
	ReplicaChangePct   int
}

// Decision is the policy verdict.
type Decision struct {
	Verdict               schemas.GovernanceVerdict
	ConfidenceAtDecision  float64
	RulesEvaluated        []map[string]any
	RollbackBlocked       bool
	RollbackBlockedReason string

	// HumanReviewReason is populated when Verdict == RequireHumanApproval
	// so callers can surface why human review is required
	// ("high_risk_service", "blast_radius_exceeded", "low_confidence_in_prod").
	HumanReviewReason string
}

// Evaluate runs the policy rules and returns a Decision. See the package
// doc for the verdict matrix.
func (p *Policy) Evaluate(in Input) *Decision {
	isProd := p.ProductionNamespaces[in.TargetNamespace]
	isHighRisk := p.HighRiskServices[in.TargetService]
	exceedsBlast := in.ReplicaChangePct > p.MaxReplicaChangePct
	rollbackBlocked := in.FixType == schemas.FixRollback && in.HasSchemaMigration
	safeType := isSafeFixType(in.FixType)

	var verdict schemas.GovernanceVerdict
	var humanReviewReason string

	switch {
	case rollbackBlocked:
		verdict = schemas.VerdictDeny
	case !isProd:
		verdict = schemas.VerdictAllow
	case !safeType:
		verdict = schemas.VerdictDeny
	case isHighRisk:
		verdict = schemas.VerdictRequireHumanApproval
		humanReviewReason = "high_risk_service"
	case exceedsBlast:
		verdict = schemas.VerdictRequireHumanApproval
		humanReviewReason = "blast_radius_exceeded"
	case in.Confidence >= p.ConfidenceAutonomous:
		verdict = schemas.VerdictAllow
	case in.Confidence >= p.ConfidenceHumanReview:
		verdict = schemas.VerdictRequireHumanApproval
		humanReviewReason = "low_confidence_in_prod"
	default:
		verdict = schemas.VerdictDeny
	}

	rules := []map[string]any{
		{"rule": "is_production_namespace", "result": isProd},
		{"rule": "is_high_risk_service", "result": isHighRisk},
		{"rule": "exceeds_replica_blast_radius", "result": exceedsBlast},
		{"rule": "rollback_blocked", "result": rollbackBlocked},
		{"rule": "safe_fix_type", "result": safeType},
		{"rule": "confidence_autonomous", "threshold": p.ConfidenceAutonomous, "value": in.Confidence, "passed": in.Confidence >= p.ConfidenceAutonomous},
		{"rule": "confidence_human_review", "threshold": p.ConfidenceHumanReview, "value": in.Confidence, "passed": in.Confidence >= p.ConfidenceHumanReview},
	}

	d := &Decision{
		Verdict:              verdict,
		ConfidenceAtDecision: in.Confidence,
		RulesEvaluated:       rules,
		RollbackBlocked:      rollbackBlocked,
		HumanReviewReason:    humanReviewReason,
	}
	if rollbackBlocked {
		d.RollbackBlockedReason = "schema_migration_present"
	}
	return d
}

// isSafeFixType reports whether the fix type may be auto-applied in
// production (subject to the other gates). Rollback is intentionally
// excluded — rollbacks in production route to RequireHumanApproval via the
// !safeType rule, except when explicitly allowed by the non-prod path.
// Infrastructure fixes are excluded because their blast radius is open-ended.
func isSafeFixType(t schemas.FixType) bool {
	switch t {
	case schemas.FixRestart, schemas.FixScale, schemas.FixCodeFix, schemas.FixRollForward:
		return true
	}
	return false
}

// IsAutoMergeAllowed reports whether a fix with the given properties is
// eligible for auto-merge, subject to the merger's other gates (sandbox,
// CI, operator approval). It is a stricter subset of Evaluate: schema
// migrations and rollbacks always require human review.
//
// isHighRisk does NOT block here — the merger checks the explicit
// /sentinel approve label separately, so the safety policy returns true
// and lets the merger gate on the label.
func (p *Policy) IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool {
	if hasSchemaMigration {
		return false
	}
	if fixType == "rollback" {
		return false
	}
	ft := schemas.FixType(fixType)
	return isSafeFixType(ft)
}

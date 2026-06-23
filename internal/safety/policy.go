// Package safety implements the governance layer.
// v3: replaces the OPA Rego sidecar with 5 plain Go if-statements.
// Same rules as services/safety/policies/sentinel.rego — just no JVM sidecar.
package safety

import (
	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// Policy is the embedded safety policy.
type Policy struct {
	ProductionNamespaces     map[string]bool
	HighRiskServices         map[string]bool
	ConfidenceAutonomous     float64
	MaxReplicaChangePct      int
}

// DefaultPolicy returns the default policy (matches the Rego defaults).
func DefaultPolicy() *Policy {
	return &Policy{
		ProductionNamespaces: map[string]bool{
			"prod": true, "production": true, "payments": true,
		},
		HighRiskServices: map[string]bool{
			"payment-api": true, "billing-api": true, "auth-service": true,
		},
		ConfidenceAutonomous: 0.92,
		MaxReplicaChangePct:  20,
	}
}

// NewPolicy creates a policy with custom namespaces + thresholds.
func NewPolicy(productionNamespaces []string, confidenceAutonomous float64) *Policy {
	p := DefaultPolicy()
	p.ProductionNamespaces = map[string]bool{}
	for _, ns := range productionNamespaces {
		p.ProductionNamespaces[ns] = true
	}
	if confidenceAutonomous > 0 {
		p.ConfidenceAutonomous = confidenceAutonomous
	}
	return p
}

// Input is the data the policy evaluates.
type Input struct {
	FixType              schemas.FixType
	TargetService        string
	TargetNamespace      string
	Confidence           float64
	HasSchemaMigration   bool
	ReplicaChangePct     int
}

// Decision is the policy verdict.
type Decision struct {
	Verdict                schemas.GovernanceVerdict
	ConfidenceAtDecision  float64
	RulesEvaluated         []map[string]any
	RollbackBlocked        bool
	RollbackBlockedReason  string
}

// Evaluate runs the 5 rules and returns a Decision.
//
// Rules (matching the Rego policy exactly):
//   1. is_production_namespace  — input.Namespace in ProductionNamespaces
//   2. is_high_risk_service     — input.TargetService in HighRiskServices
//   3. exceeds_replica_blast_radius — input.ReplicaChangePct > MaxReplicaChangePct
//   4. rollback_blocked         — input.FixType == rollback && input.HasSchemaMigration
//   5. allow_fix                — production: high conf + safe type + not high risk + not blast radius + not rollback blocked
//                                 non-production: everything except rollback blocked
func (p *Policy) Evaluate(in Input) *Decision {
	isProd := p.ProductionNamespaces[in.TargetNamespace]
	isHighRisk := p.HighRiskServices[in.TargetService]
	exceedsBlast := in.ReplicaChangePct > p.MaxReplicaChangePct
	rollbackBlocked := in.FixType == schemas.FixRollback && in.HasSchemaMigration

	allowFix := false
	if isProd {
		// Production: require high confidence + safe fix type + not high risk + not blast + not rollback blocked
		if in.Confidence >= p.ConfidenceAutonomous &&
			!isHighRisk &&
			!exceedsBlast &&
			!rollbackBlocked &&
			isSafeFixType(in.FixType) {
			allowFix = true
		}
	} else {
		// Non-production: allow everything except rollback blocked
		if !rollbackBlocked {
			allowFix = true
		}
	}

	// Determine verdict
	var verdict schemas.GovernanceVerdict
	switch {
	case rollbackBlocked:
		verdict = schemas.VerdictDeny
	case allowFix:
		verdict = schemas.VerdictAllow
	default:
		verdict = schemas.VerdictDeny
	}

	rules := []map[string]any{
		{"rule": "is_production_namespace", "result": isProd},
		{"rule": "is_high_risk_service", "result": isHighRisk},
		{"rule": "exceeds_replica_blast_radius", "result": exceedsBlast},
		{"rule": "rollback_blocked", "result": rollbackBlocked},
		{"rule": "allow_fix", "result": allowFix},
	}

	d := &Decision{
		Verdict:               verdict,
		ConfidenceAtDecision: in.Confidence,
		RulesEvaluated:        rules,
		RollbackBlocked:       rollbackBlocked,
	}
	if rollbackBlocked {
		d.RollbackBlockedReason = "schema_migration_present"
	}
	return d
}

func isSafeFixType(t schemas.FixType) bool {
	switch t {
	case schemas.FixRestart, schemas.FixScale, schemas.FixCodeFix, schemas.FixRollForward:
		return true
	}
	return false
}

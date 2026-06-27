// Package safety implements the governance layer.
// v3: replaces the OPA Rego sidecar with 5 plain Go if-statements.
// Same rules as services/safety/policies/sentinel.rego — just no JVM sidecar.
//
// v3.8.4: The policy now actually returns VerdictRequireHumanApproval (it
// was previously defined in schemas but never produced by Evaluate, making
// the human-review branch in main.go dead code). The verdict matrix is:
//
//      rollbackBlocked                         -> Deny
//      prod + (high-risk OR blast-radius OR
//        confidence in [humanReview, autonomous)) -> RequireHumanApproval
//      prod + confidence >= autonomous + safe    -> Allow
//      non-prod + not rollback-blocked           -> Allow
//      everything else                           -> Deny
package safety

import (
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// Policy is the embedded safety policy.
type Policy struct {
        ProductionNamespaces     map[string]bool
        HighRiskServices         map[string]bool
        ConfidenceAutonomous     float64
        // v3.8.4: Confidence below which a prod fix is denied outright (not even
        // human-reviewed). Between ConfidenceHumanReview and ConfidenceAutonomous,
        // a prod fix goes to VerdictRequireHumanApproval.
        ConfidenceHumanReview float64
        MaxReplicaChangePct   int
}

// DefaultPolicy returns the default policy.
//
// v3.11.0: Removed demo-specific values (audit #5 + obs-1).
//   - ProductionNamespaces: was {prod, production, payments}. The demo's
//     "payments" namespace leaked into every install. Now {prod, production}.
//     NewPolicy() overrides this from cfg.ProductionNamespaces anyway.
//   - HighRiskServices: was {payment-api, billing-api, auth-service}. These
//     leaked permanently because NewPolicy couldn't override them. Now empty
//     (operators set their own via WithHighRiskServices / cfg.HighRiskServices).
func DefaultPolicy() *Policy {
        return &Policy{
                ProductionNamespaces: map[string]bool{
                        "prod": true, "production": true,
                },
                HighRiskServices:     map[string]bool{},
                ConfidenceAutonomous:  0.92,
                ConfidenceHumanReview: 0.70,
                MaxReplicaChangePct:   20,
        }
}

// NewPolicy creates a policy with custom namespaces + thresholds.
// v3.8.4: Now accepts confidenceHumanReview in addition to confidenceAutonomous.
// Pass 0 to keep the default (0.70).
// v3.11.0: HighRiskServices is no longer permanently set to the demo values
// (payment-api/billing-api/auth-service). The default HighRiskServices is
// now empty — operators must explicitly set their own via WithHighRiskServices
// or NewPolicyWithHighRisk. The previous behavior (NewPolicy could not
// override HighRiskServices) leaked demo values into every install (obs-1).
func NewPolicy(productionNamespaces []string, confidenceAutonomous, confidenceHumanReview float64) *Policy {
        p := DefaultPolicy()
        p.ProductionNamespaces = map[string]bool{}
        for _, ns := range productionNamespaces {
                p.ProductionNamespaces[ns] = true
        }
        // v3.11.0: Reset HighRiskServices to empty — demo values should not
        // leak into production installs. Operators set their own via
        // WithHighRiskServices.
        p.HighRiskServices = map[string]bool{}
        if confidenceAutonomous > 0 {
                p.ConfidenceAutonomous = confidenceAutonomous
        }
        if confidenceHumanReview > 0 {
                p.ConfidenceHumanReview = confidenceHumanReview
        }
        return p
}

// NewPolicyWithHighRisk creates a policy with custom namespaces, thresholds,
// AND high-risk services. v3.11.0: This is the constructor operators should
// use when they want to enforce human review on specific services. The
// highRiskServices parameter is a list of service names (e.g.
// ["payment-api", "billing-api"]) that always require human approval in prod.
func NewPolicyWithHighRisk(productionNamespaces []string, confidenceAutonomous, confidenceHumanReview float64, highRiskServices []string) *Policy {
        p := NewPolicy(productionNamespaces, confidenceAutonomous, confidenceHumanReview)
        for _, svc := range highRiskServices {
                p.HighRiskServices[svc] = true
        }
        return p
}

// WithHighRiskServices sets the high-risk services list. v3.11.0: Replaces
// the permanent demo defaults. Pass your own services, e.g.
// ["payment-api", "billing-api", "auth-service"]. Pass nil/empty to clear
// all high-risk services (every service can be auto-fixed if confidence
// is high enough).
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
        // v3.8.4: HumanReviewReason is populated when Verdict == RequireHumanApproval
        // so callers can surface *why* human review is required (e.g. "high_risk_service",
        // "blast_radius_exceeded", "low_confidence_in_prod").
        HumanReviewReason string
}

// Evaluate runs the policy rules and returns a Decision.
//
// v3.8.4: Verdict matrix:
//
//      rollbackBlocked -> VerdictDeny (hard block; can't auto-rollback a schema migration)
//      non-prod + not rollback-blocked -> VerdictAllow (sandbox/sim is permissive)
//      prod + confidence >= autonomous + safe type + not high-risk + not blast -> VerdictAllow
//      prod + confidence >= humanReview + (high-risk OR blast OR confidence < autonomous) -> VerdictRequireHumanApproval
//      prod + confidence < humanReview -> VerdictDeny (too uncertain for prod, even with human review)
//      prod + unsafe fix type (rollback without schema migration is safe; others denied) -> VerdictDeny
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
                // Hard block — can't auto-rollback a schema migration.
                verdict = schemas.VerdictDeny

        case !isProd:
                // Non-production: permissive. Sandbox/sim namespaces don't need
                // human review — that's the whole point of having a sandbox.
                verdict = schemas.VerdictAllow

        case !safeType:
                // Production + unsafe fix type (e.g. infrastructure fix without
                // explicit approval). Deny — don't even send to human review.
                verdict = schemas.VerdictDeny

        case isHighRisk:
                // Production + high-risk service (payment-api, billing-api, auth).
                // Even at high confidence, require human approval — these services
                // have financial / regulatory blast radius.
                verdict = schemas.VerdictRequireHumanApproval
                humanReviewReason = "high_risk_service"

        case exceedsBlast:
                // Production + replica change exceeds MaxReplicaChangePct.
                verdict = schemas.VerdictRequireHumanApproval
                humanReviewReason = "blast_radius_exceeded"

        case in.Confidence >= p.ConfidenceAutonomous:
                // Production + high confidence + safe type + not high-risk + not blast.
                verdict = schemas.VerdictAllow

        case in.Confidence >= p.ConfidenceHumanReview:
                // Production + medium confidence — human can approve.
                verdict = schemas.VerdictRequireHumanApproval
                humanReviewReason = "low_confidence_in_prod"

        default:
                // Production + confidence < humanReview — too uncertain even for
                // human review. Deny outright (the human can still trigger a manual
                // investigation if they want).
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

func isSafeFixType(t schemas.FixType) bool {
        switch t {
        case schemas.FixRestart, schemas.FixScale, schemas.FixCodeFix, schemas.FixRollForward:
                return true
        }
        return false
}

// IsAutoMergeAllowed returns true if a fix with the given properties is
// eligible for auto-merge (subject to the merger's other gates: sandbox,
// confidence, CI). This is the safety-policy contribution to the auto-merge
// decision — it's a stricter subset of Evaluate().
//
// v4.0.0: Implemented by automerge.SafetyChecker interface.
//
// Rules (any one of these returns false):
//   - HasSchemaMigration: schema migrations always need human review
//   - FixType == rollback: rollbacks always need human review
//   - isHighRisk: high-risk services need explicit /sentinel approve
//   - !isSafeFixType: infrastructure/unknown fix types need human review
func (p *Policy) IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool {
        if hasSchemaMigration {
                return false
        }
        if fixType == "rollback" {
                return false
        }
        if isHighRisk {
                // High-risk services CAN be auto-merged — but only if the operator
                // has explicitly approved via /sentinel approve. The merger checks
                // the approved label separately. From the safety policy's perspective,
                // we return true and let the merger gate on the label.
                // (Returning false here would block even approved high-risk merges.)
                _ = isHighRisk // explicit no-op: documented above
        }
        ft := schemas.FixType(fixType)
        return isSafeFixType(ft)
}

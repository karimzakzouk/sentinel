package safety

import (
        "testing"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

func TestDefaultPolicyAllowNonProd(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "api",
                TargetNamespace: "sim", // not production
                Confidence:      0.80,
        })
        if d.Verdict != schemas.VerdictAllow {
                t.Errorf("non-prod code_fix should be allowed, got %s", d.Verdict)
        }
}

func TestDefaultPolicyAllowProdHighConfidence(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.95, // above 0.92 threshold
        })
        if d.Verdict != schemas.VerdictAllow {
                t.Errorf("prod code_fix with high confidence should be allowed, got %s", d.Verdict)
        }
}

func TestDefaultPolicyDenyProdLowConfidence(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.50, // below 0.70 human-review threshold
        })
        if d.Verdict != schemas.VerdictDeny {
                t.Errorf("prod code_fix with confidence < humanReview should be denied, got %s", d.Verdict)
        }
}

// The new verdict matrix sends high-risk services to RequireHumanApproval
// instead — that's more useful (a human can approve a high-confidence fix
// to a payment service rather than having to do it manually from scratch).

// default HighRiskServices is now empty — demo values no longer leak into
// every install. Tests must explicitly set up high-risk services.
func TestDefaultPolicyHumanReviewForHighRiskService(t *testing.T) {
        p := NewPolicyWithHighRisk([]string{"prod"}, 0.92, 0.70, []string{"payment-api"})
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "payment-api", // high risk (explicitly configured)
                TargetNamespace: "prod",
                Confidence:      0.99, // even high confidence
        })
        if d.Verdict != schemas.VerdictRequireHumanApproval {
                t.Errorf("high-risk service in prod should require human approval, got %s", d.Verdict)
        }
        if d.HumanReviewReason != "high_risk_service" {
                t.Errorf("HumanReviewReason = %q, want high_risk_service", d.HumanReviewReason)
        }
}

// services (demo values removed). A service that was high-risk in the demo
// (payment-api) is now auto-fixable in prod if confidence is high enough.
func TestDefaultPolicyNoHighRiskServicesByDefault(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "payment-api", // was high-risk in demo, now not
                TargetNamespace: "prod",
                Confidence:      0.99, // above autonomous threshold
        })
        if d.Verdict != schemas.VerdictAllow {
                t.Errorf("payment-api should be auto-fixable by default (no high-risk services in default policy), got %s", d.Verdict)
        }
}

// label only matters in production.
func TestDefaultPolicyAllowHighRiskServiceInNonProd(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "payment-api",
                TargetNamespace: "sim",
                Confidence:      0.80,
        })
        if d.Verdict != schemas.VerdictAllow {
                t.Errorf("high-risk service in non-prod should be allowed, got %s", d.Verdict)
        }
}

func TestDefaultPolicyDenyRollbackWithSchemaMigration(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:            schemas.FixRollback,
                TargetService:      "api",
                TargetNamespace:    "sim", // even non-prod
                HasSchemaMigration: true,
        })
        if d.Verdict != schemas.VerdictDeny {
                t.Errorf("rollback with schema migration should be denied, got %s", d.Verdict)
        }
        if !d.RollbackBlocked {
                t.Error("rollback should be marked as blocked")
        }
        if d.RollbackBlockedReason != "schema_migration_present" {
                t.Errorf("reason = %q, want schema_migration_present", d.RollbackBlockedReason)
        }
}

func TestDefaultPolicyAllowRollbackWithoutSchemaMigration(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixRollback,
                TargetService:   "api",
                TargetNamespace: "sim",
                Confidence:      0.80,
        })
        if d.Verdict != schemas.VerdictAllow {
                t.Errorf("rollback without schema migration in non-prod should be allowed, got %s", d.Verdict)
        }
}

// and autonomous (0.92) should go to RequireHumanApproval.
func TestDefaultPolicyHumanReviewForMediumConfidenceInProd(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.80, // between 0.70 and 0.92
        })
        if d.Verdict != schemas.VerdictRequireHumanApproval {
                t.Errorf("prod fix with confidence in [humanReview, autonomous) should require human approval, got %s", d.Verdict)
        }
        if d.HumanReviewReason != "low_confidence_in_prod" {
                t.Errorf("HumanReviewReason = %q, want low_confidence_in_prod", d.HumanReviewReason)
        }
}

func TestDefaultPolicyHumanReviewForBlastRadiusExceeded(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixScale,
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.95,
                ReplicaChangePct: 50, // exceeds default MaxReplicaChangePct=20
        })
        if d.Verdict != schemas.VerdictRequireHumanApproval {
                t.Errorf("prod scale fix exceeding blast radius should require human approval, got %s", d.Verdict)
        }
        if d.HumanReviewReason != "blast_radius_exceeded" {
                t.Errorf("HumanReviewReason = %q, want blast_radius_exceeded", d.HumanReviewReason)
        }
}

func TestDefaultPolicyDenyUnsafeFixTypeInProd(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixInfrastructure, // not in the safe-type list
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.99,
        })
        if d.Verdict != schemas.VerdictDeny {
                t.Errorf("unsafe fix type in prod should be denied, got %s", d.Verdict)
        }
}

func TestCustomPolicy(t *testing.T) {
        p := NewPolicy([]string{"staging", "prod"}, 0.85, 0.60)
        if !p.ProductionNamespaces["staging"] {
                t.Error("staging should be production")
        }
        if !p.ProductionNamespaces["prod"] {
                t.Error("prod should be production")
        }
        if p.ProductionNamespaces["sim"] {
                t.Error("sim should NOT be production in custom policy")
        }
        if p.ConfidenceAutonomous != 0.85 {
                t.Errorf("ConfidenceAutonomous = %v, want 0.85", p.ConfidenceAutonomous)
        }
        if p.ConfidenceHumanReview != 0.60 {
                t.Errorf("ConfidenceHumanReview = %v, want 0.60", p.ConfidenceHumanReview)
        }
}

func TestRulesEvaluated(t *testing.T) {
        p := DefaultPolicy()
        d := p.Evaluate(Input{
                FixType:         schemas.FixCodeFix,
                TargetService:   "api",
                TargetNamespace: "prod",
                Confidence:      0.95,
        })

        // confidence_human_review; removed the old allow_fix composite rule.
        if len(d.RulesEvaluated) != 7 {
                t.Errorf("expected 7 rules evaluated, got %d", len(d.RulesEvaluated))
        }
}

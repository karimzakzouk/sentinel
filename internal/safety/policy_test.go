package safety

import (
	"testing"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestDefaultPolicyAllowNonProd verifies the sandbox is permissive.
func TestDefaultPolicyAllowNonProd(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "sim",
		Confidence:      0.80,
	})
	if d.Verdict != schemas.VerdictAllow {
		t.Errorf("non-prod code_fix should be allowed, got %s", d.Verdict)
	}
}

// TestDefaultPolicyAllowProdHighConfidence verifies that a high-confidence
// production fix is auto-allowed.
func TestDefaultPolicyAllowProdHighConfidence(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.95,
	})
	if d.Verdict != schemas.VerdictAllow {
		t.Errorf("prod code_fix with high confidence should be allowed, got %s", d.Verdict)
	}
}

// TestDefaultPolicyDenyProdLowConfidence verifies that a low-confidence
// production fix is denied outright (not even sent to human review).
func TestDefaultPolicyDenyProdLowConfidence(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.50,
	})
	if d.Verdict != schemas.VerdictDeny {
		t.Errorf("prod code_fix with confidence < humanReview should be denied, got %s", d.Verdict)
	}
}

// TestDefaultPolicyHumanReviewForHighRiskService verifies that an explicitly
// configured high-risk service in production always routes to human approval.
func TestDefaultPolicyHumanReviewForHighRiskService(t *testing.T) {
	p := NewPolicyWithHighRisk([]string{"prod"}, 0.92, 0.70, []string{"payment-api"})
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "payment-api",
		TargetNamespace: "prod",
		Confidence:      0.99,
	})
	if d.Verdict != schemas.VerdictRequireHumanApproval {
		t.Errorf("high-risk service in prod should require human approval, got %s", d.Verdict)
	}
	if d.HumanReviewReason != "high_risk_service" {
		t.Errorf("HumanReviewReason = %q, want high_risk_service", d.HumanReviewReason)
	}
}

// TestDefaultPolicyNoHighRiskServicesByDefault verifies the demo defaults
// (payment-api/billing-api/auth-service) are NOT carried forward — a fresh
// DefaultPolicy allows payment-api to be auto-fixed if confidence is high.
func TestDefaultPolicyNoHighRiskServicesByDefault(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "payment-api",
		TargetNamespace: "prod",
		Confidence:      0.99,
	})
	if d.Verdict != schemas.VerdictAllow {
		t.Errorf("payment-api should be auto-fixable by default, got %s", d.Verdict)
	}
}

// TestDefaultPolicyAllowHighRiskServiceInNonProd verifies the high-risk gate
// only applies in production namespaces.
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

// TestDefaultPolicyDenyRollbackWithSchemaMigration verifies the hard block
// on rolling back a schema migration, even in non-prod.
func TestDefaultPolicyDenyRollbackWithSchemaMigration(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:            schemas.FixRollback,
		TargetService:      "api",
		TargetNamespace:    "sim",
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

// TestDefaultPolicyAllowRollbackWithoutSchemaMigration verifies that a
// rollback in non-prod without a schema migration is allowed.
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

// TestDefaultPolicyHumanReviewForMediumConfidenceInProd verifies that a
// prod fix with confidence in [humanReview, autonomous) is sent to human
// approval, not auto-applied.
func TestDefaultPolicyHumanReviewForMediumConfidenceInProd(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.80,
	})
	if d.Verdict != schemas.VerdictRequireHumanApproval {
		t.Errorf("prod fix with confidence in [humanReview, autonomous) should require human approval, got %s", d.Verdict)
	}
	if d.HumanReviewReason != "low_confidence_in_prod" {
		t.Errorf("HumanReviewReason = %q, want low_confidence_in_prod", d.HumanReviewReason)
	}
}

// TestDefaultPolicyHumanReviewForBlastRadiusExceeded verifies that a scale
// fix in prod exceeding MaxReplicaChangePct routes to human approval.
func TestDefaultPolicyHumanReviewForBlastRadiusExceeded(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:          schemas.FixScale,
		TargetService:    "api",
		TargetNamespace:  "prod",
		Confidence:       0.95,
		ReplicaChangePct: 50,
	})
	if d.Verdict != schemas.VerdictRequireHumanApproval {
		t.Errorf("prod scale fix exceeding blast radius should require human approval, got %s", d.Verdict)
	}
	if d.HumanReviewReason != "blast_radius_exceeded" {
		t.Errorf("HumanReviewReason = %q, want blast_radius_exceeded", d.HumanReviewReason)
	}
}

// TestDefaultPolicyDenyUnsafeFixTypeInProd verifies that an infrastructure
// fix in production is denied.
func TestDefaultPolicyDenyUnsafeFixTypeInProd(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixInfrastructure,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.99,
	})
	if d.Verdict != schemas.VerdictDeny {
		t.Errorf("unsafe fix type in prod should be denied, got %s", d.Verdict)
	}
}

// TestCustomPolicy verifies NewPolicy overrides production namespaces and
// confidence thresholds.
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

// TestRulesEvaluated verifies the audit trail has the expected rule count.
func TestRulesEvaluated(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.95,
	})
	if len(d.RulesEvaluated) != 7 {
		t.Errorf("expected 7 rules evaluated, got %d", len(d.RulesEvaluated))
	}
}

// TestSafety_ProductionNamespaceBlocksFix is the v1 regression test: a fix
// targeted at a production namespace must NOT be auto-allowed when
// confidence is below the autonomous threshold, and the policy must report
// RequireHumanApproval (not Deny) so a human can still approve it.
func TestSafety_ProductionNamespaceBlocksFix(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.80, // between humanReview (0.70) and autonomous (0.92)
	})
	if d.Verdict != schemas.VerdictRequireHumanApproval {
		t.Errorf("prod fix below autonomous threshold should require human approval, got %s", d.Verdict)
	}
	if d.HumanReviewReason != "low_confidence_in_prod" {
		t.Errorf("HumanReviewReason = %q, want low_confidence_in_prod", d.HumanReviewReason)
	}
	// And a confidence below humanReview must be a hard Deny (no human path).
	d2 := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "api",
		TargetNamespace: "prod",
		Confidence:      0.40,
	})
	if d2.Verdict != schemas.VerdictDeny {
		t.Errorf("prod fix below humanReview threshold should be denied, got %s", d2.Verdict)
	}
}

// TestSafety_SchemaMigrationBlocksRollback is the v1 regression test: a
// rollback combined with a schema migration must be hard-denied regardless
// of namespace, confidence, or any other gate. The RollbackBlocked flag
// must be set so the caller can surface the reason.
func TestSafety_SchemaMigrationBlocksRollback(t *testing.T) {
	p := DefaultPolicy()
	for _, ns := range []string{"sim", "prod", "staging"} {
		d := p.Evaluate(Input{
			FixType:            schemas.FixRollback,
			TargetService:      "api",
			TargetNamespace:    ns,
			Confidence:         0.99,
			HasSchemaMigration: true,
		})
		if d.Verdict != schemas.VerdictDeny {
			t.Errorf("ns=%s: rollback+schema_migration should be Deny, got %s", ns, d.Verdict)
		}
		if !d.RollbackBlocked {
			t.Errorf("ns=%s: RollbackBlocked flag should be set", ns)
		}
		if d.RollbackBlockedReason != "schema_migration_present" {
			t.Errorf("ns=%s: reason = %q, want schema_migration_present", ns, d.RollbackBlockedReason)
		}
	}
	// Same fix without schema migration in non-prod must still be allowed.
	d := p.Evaluate(Input{
		FixType:         schemas.FixRollback,
		TargetService:   "api",
		TargetNamespace: "sim",
		Confidence:      0.99,
	})
	if d.Verdict != schemas.VerdictAllow {
		t.Errorf("rollback without schema migration in non-prod should be allowed, got %s", d.Verdict)
	}
	if d.RollbackBlocked {
		t.Error("RollbackBlocked flag should NOT be set when no schema migration is present")
	}
}

// TestIsAutoMergeAllowed verifies the auto-merge gating function.
func TestIsAutoMergeAllowed(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name        string
		fixType     string
		schemaMig   bool
		highRisk    bool
		wantAllowed bool
	}{
		{"code_fix no schema", "code_fix", false, false, true},
		{"code_fix with schema migration", "code_fix", true, false, false},
		{"rollback no schema", "rollback", false, false, false},
		{"rollback with schema migration", "rollback", true, false, false},
		{"restart no schema", "restart", false, false, true},
		{"scale no schema", "scale", false, false, true},
		{"roll_forward no schema", "roll_forward", false, false, true},
		{"infrastructure fix", "infrastructure", false, false, false},
		{"high-risk code_fix (label-gated by merger)", "code_fix", false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.IsAutoMergeAllowed(c.fixType, c.schemaMig, c.highRisk)
			if got != c.wantAllowed {
				t.Errorf("IsAutoMergeAllowed(%q, schema=%v, highRisk=%v) = %v, want %v",
					c.fixType, c.schemaMig, c.highRisk, got, c.wantAllowed)
			}
		})
	}
}

package safety

import (
	"testing"

	"github.com/karimzakzoukz/sentinel/v3/internal/schemas"
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
		Confidence:      0.50, // below 0.92 threshold
	})
	if d.Verdict != schemas.VerdictDeny {
		t.Errorf("prod code_fix with low confidence should be denied, got %s", d.Verdict)
	}
}

func TestDefaultPolicyDenyHighRiskService(t *testing.T) {
	p := DefaultPolicy()
	d := p.Evaluate(Input{
		FixType:         schemas.FixCodeFix,
		TargetService:   "payment-api", // high risk
		TargetNamespace: "prod",
		Confidence:      0.99, // even high confidence
	})
	if d.Verdict != schemas.VerdictDeny {
		t.Errorf("high-risk service should always be denied, got %s", d.Verdict)
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

func TestCustomPolicy(t *testing.T) {
	p := NewPolicy([]string{"staging", "prod"}, 0.85)
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
		t.Errorf("threshold = %v, want 0.85", p.ConfidenceAutonomous)
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
	if len(d.RulesEvaluated) != 5 {
		t.Errorf("expected 5 rules evaluated, got %d", len(d.RulesEvaluated))
	}
}

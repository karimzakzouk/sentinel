package automerge

import (
	"testing"
	"time"
)

// mockSafetyChecker implements SafetyChecker for tests.
type mockSafetyChecker struct {
	allowed bool
}

func (m *mockSafetyChecker) IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool {
	return m.allowed
}

func TestPolicy_DisabledGlobally(t *testing.T) {
	p := NewPolicy(Config{Enabled: false}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when globally disabled")
	}
}

func TestPolicy_NamespaceNotAllowed(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:    true,
		Namespaces: []string{"sim"},
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "prod",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when namespace not allowed")
	}
}

func TestPolicy_MissingAnnotation(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:    true,
		Namespaces: []string{"sim"},
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: false,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when annotation missing")
	}
}

func TestPolicy_SandboxNotVerified(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:    true,
		Namespaces: []string{"sim"},
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    false,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when sandbox not verified")
	}
}

func TestPolicy_LowConfidence(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.50,
		PolicyVerdict:      "allow",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when confidence below threshold")
	}
}

func TestPolicy_SchemaMigrationBlocked(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		HasSchemaMigration: true,
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false for schema migration")
	}
}

func TestPolicy_RollbackBlocked(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		FixType:            "rollback",
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false for rollback fix type")
	}
}

func TestPolicy_HoldLabelBlocks(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		HoldLabel:          true,
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when hold label set")
	}
}

func TestPolicy_NoMergeLabelBlocks(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		NoMergeLabel:       true,
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when no-merge label set")
	}
}

func TestPolicy_HighRiskRequiresApproval(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:       true,
		Namespaces:    []string{"sim"},
		MinConfidence: 0.85,
	}, &mockSafetyChecker{allowed: true})
	// Without approved label
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		IsHighRisk:         true,
		ApprovedLabel:      false,
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false for high-risk without approval")
	}
	// With approved label
	d2 := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		IsHighRisk:         true,
		ApprovedLabel:      true,
		CIStatus:           CIStatus{State: "success"},
	})
	if !d2.AutoMerge {
		t.Errorf("expected AutoMerge=true for high-risk with approval + CI green, got: %s", d2.Reason)
	}
}

func TestPolicy_AllGatesPass(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:         true,
		Namespaces:      []string{"sim"},
		MinConfidence:   0.85,
		RequireCIGreen:  true,
		MaxPerHour:      3,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		FixType:            "code_fix",
		CIStatus:           CIStatus{State: "success", Checks: 3, Passing: 3},
	})
	if !d.AutoMerge {
		t.Errorf("expected AutoMerge=true, got false. Reason: %s, GatesFailed: %v", d.Reason, d.GatesFailed)
	}
}

func TestPolicy_CIFailing(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:         true,
		Namespaces:      []string{"sim"},
		MinConfidence:   0.85,
		RequireCIGreen:  true,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(Input{
		Namespace:          "sim",
		AutoMergeAnnotation: true,
		SandboxVerified:    true,
		Confidence:         0.95,
		PolicyVerdict:      "allow",
		CIStatus:           CIStatus{State: "failure", Checks: 3, Failing: 1},
	})
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when CI failing")
	}
}

func TestRateLimiter_Basic(t *testing.T) {
	rl := NewRateLimiter(2, time.Hour)
	if !rl.Allow() {
		t.Error("expected first call to be allowed")
	}
	rl.Record()
	if !rl.Allow() {
		t.Error("expected second call to be allowed")
	}
	rl.Record()
	if rl.Allow() {
		t.Error("expected third call to be blocked (rate limit reached)")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)
	rl.Record()
	if rl.Allow() {
		t.Error("expected block after first record")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow() {
		t.Error("expected allow after window expired")
	}
}

func TestCooldown_Basic(t *testing.T) {
	c := NewCooldown(time.Hour)
	if c.IsPaused() {
		t.Error("expected not paused initially")
	}
	c.Trigger("test regression")
	if !c.IsPaused() {
		t.Error("expected paused after trigger")
	}
	if c.Reason() != "test regression" {
		t.Errorf("expected reason 'test regression', got %q", c.Reason())
	}
	c.Clear()
	if c.IsPaused() {
		t.Error("expected not paused after clear")
	}
}

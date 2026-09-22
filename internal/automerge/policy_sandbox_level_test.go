package automerge

import "testing"

// TestParseSandboxLevel verifies level-string parsing incl. legacy values.
func TestParseSandboxLevel(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"L1-L5", 5},
		{"L1-L4", 4},
		{"L1-L3", 3},
		{"", 3},        // legacy verified runs pre-date levels
		{"garbage", 3}, // unparseable → historical minimum
		{"L1-L9", 3},   // out of range → historical minimum
		{"L1-L0", 3},
	}
	for _, tc := range tests {
		if got := parseSandboxLevel(tc.in); got != tc.want {
			t.Errorf("parseSandboxLevel(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// gateInput returns an Input that passes every gate except the ones the
// test overrides. Sandbox-level specifics are set per-case.
func gateInput() Input {
	return Input{
		IncidentID:          "inc-1",
		Service:             "api",
		Namespace:           "sim",
		FixType:             "code_fix",
		Confidence:          0.95,
		SandboxVerified:     true,
		SandboxLevels:       "L1-L5",
		CIStatus:            CIStatus{State: "success", Checks: 3, Passing: 3},
		AutoMergeAnnotation: true,
		PolicyVerdict:       "allow",
	}
}

// allGatesConfig returns an enabled config where every gate can pass.
func allGatesConfig() Config {
	return Config{
		Enabled:              true,
		Namespaces:           []string{"sim"},
		MinConfidence:        0.85,
		RequireCIGreen:       true,
		CITimeoutSec:         900,
		MaxPerHour:           100,
		CooldownAfterFailMin: 0,
		MinSandboxLevel:      3,
	}
}

// TestPolicyEvaluateTruthTable walks the full gate chain: every case flips
// exactly one input and asserts the exact failing gate + that all-gates-
// pass merges.
func TestPolicyEvaluateTruthTable(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(in *Input, cfg *Config)
		wantMerge  bool
		wantFailed string // "" = expect merge; else the exact gate
	}{
		{
			name:      "all gates pass",
			mutate:    func(in *Input, cfg *Config) {},
			wantMerge: true,
		},
		{
			name:       "global switch off",
			mutate:     func(in *Input, cfg *Config) { cfg.Enabled = false },
			wantFailed: "",
			wantMerge:  false,
		},
		{
			name:       "no-merge label blocks",
			mutate:     func(in *Input, cfg *Config) { in.NoMergeLabel = true },
			wantFailed: "",
			wantMerge:  false,
		},
		{
			name:       "hold label blocks",
			mutate:     func(in *Input, cfg *Config) { in.HoldLabel = true },
			wantFailed: "",
			wantMerge:  false,
		},
		{
			name:       "namespace not allowlisted",
			mutate:     func(in *Input, cfg *Config) { in.Namespace = "prod" },
			wantFailed: "",
			wantMerge:  false,
		},
		{
			name:       "missing auto-merge annotation",
			mutate:     func(in *Input, cfg *Config) { in.AutoMergeAnnotation = false },
			wantFailed: "",
			wantMerge:  false,
		},
		{
			name:       "sandbox not verified",
			mutate:     func(in *Input, cfg *Config) { in.SandboxVerified = false },
			wantFailed: "sandbox",
			wantMerge:  false,
		},
		{
			name: "sandbox override bypasses verification",
			mutate: func(in *Input, cfg *Config) {
				in.SandboxVerified = false
				in.SandboxOverride = true
			},
			wantMerge: true,
		},
		{
			name: "sandbox level too shallow (L1-L3 when 5 required)",
			mutate: func(in *Input, cfg *Config) {
				cfg.MinSandboxLevel = 5
				in.SandboxLevels = "L1-L3"
			},
			wantFailed: "sandbox_level",
			wantMerge:  false,
		},
		{
			name: "sandbox level exact match passes (L1-L5 when 5 required)",
			mutate: func(in *Input, cfg *Config) {
				cfg.MinSandboxLevel = 5
				in.SandboxLevels = "L1-L5"
			},
			wantMerge: true,
		},
		{
			name: "sandbox level gate disabled (0)",
			mutate: func(in *Input, cfg *Config) {
				cfg.MinSandboxLevel = 0
				in.SandboxLevels = "L1-L3"
			},
			wantMerge: true,
		},
		{
			name:       "confidence below threshold",
			mutate:     func(in *Input, cfg *Config) { in.Confidence = 0.50 },
			wantFailed: "confidence",
			wantMerge:  false,
		},
		{
			name:       "safety policy denies",
			mutate:     func(in *Input, cfg *Config) { in.PolicyVerdict = "deny" },
			wantFailed: "safety",
			wantMerge:  false,
		},
		{
			name: "safety requires approval and it is missing",
			mutate: func(in *Input, cfg *Config) {
				in.PolicyVerdict = "require_human_approval"
				in.ApprovedLabel = false
			},
			wantFailed: "safety",
			wantMerge:  false,
		},
		{
			name: "safety requires approval and it is present",
			mutate: func(in *Input, cfg *Config) {
				in.PolicyVerdict = "require_human_approval"
				in.ApprovedLabel = true
			},
			wantMerge: true,
		},
		{
			name:       "schema migration never auto-merges",
			mutate:     func(in *Input, cfg *Config) { in.HasSchemaMigration = true },
			wantFailed: "schema_migration",
			wantMerge:  false,
		},
		{
			name:       "rollback never auto-merges",
			mutate:     func(in *Input, cfg *Config) { in.FixType = "rollback" },
			wantFailed: "rollback",
			wantMerge:  false,
		},
		{
			name: "high-risk service without approval",
			mutate: func(in *Input, cfg *Config) {
				in.IsHighRisk = true
				in.ApprovedLabel = false
			},
			wantFailed: "high_risk",
			wantMerge:  false,
		},
		{
			name: "high-risk service with approval",
			mutate: func(in *Input, cfg *Config) {
				in.IsHighRisk = true
				in.ApprovedLabel = true
			},
			wantMerge: true,
		},
		{
			name:       "CI failing",
			mutate:     func(in *Input, cfg *Config) { in.CIStatus = CIStatus{State: "failure", Checks: 3, Failing: 1} },
			wantFailed: "ci",
			wantMerge:  false,
		},
		{
			name:       "CI pending",
			mutate:     func(in *Input, cfg *Config) { in.CIStatus = CIStatus{State: "pending", Checks: 3, Pending: 3} },
			wantFailed: "ci",
			wantMerge:  false,
		},
		{
			name:       "CI unknown",
			mutate:     func(in *Input, cfg *Config) { in.CIStatus = CIStatus{State: "unknown"} },
			wantFailed: "ci",
			wantMerge:  false,
		},
		{
			name: "CI override bypasses failing CI",
			mutate: func(in *Input, cfg *Config) {
				in.CIStatus = CIStatus{State: "failure", Checks: 3, Failing: 1}
				in.CIOverride = true
			},
			wantMerge: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := allGatesConfig()
			in := gateInput()
			tc.mutate(&in, &cfg)
			p := NewPolicy(cfg, nil)
			d := p.Evaluate(in)

			if d.AutoMerge != tc.wantMerge {
				t.Errorf("AutoMerge = %v, want %v (reason: %s, failed: %v)",
					d.AutoMerge, tc.wantMerge, d.Reason, d.GatesFailed)
			}
			if tc.wantFailed != "" {
				if len(d.GatesFailed) == 0 || d.GatesFailed[0] != tc.wantFailed {
					t.Errorf("first failed gate = %v, want [%s]", d.GatesFailed, tc.wantFailed)
				}
			}
		})
	}
}

// TestPolicyRateLimit covers the stateful rate-limit gate.
func TestPolicyRateLimit(t *testing.T) {
	cfg := allGatesConfig()
	cfg.MaxPerHour = 1
	p := NewPolicy(cfg, nil)

	if d := p.Evaluate(gateInput()); !d.AutoMerge {
		t.Fatalf("first merge should pass, got %v (%s)", d.AutoMerge, d.Reason)
	}
	p.RecordMerge()
	d := p.Evaluate(gateInput())
	if d.AutoMerge || firstFailed(d) != "rate_limit" {
		t.Errorf("second merge should hit rate_limit, got merge=%v failed=%v", d.AutoMerge, d.GatesFailed)
	}
}

// TestPolicyCooldown covers the stateful cooldown gate (fresh rate limiter
// so rate_limit doesn't short-circuit first — it sits earlier in the chain).
func TestPolicyCooldown(t *testing.T) {
	p := NewPolicy(allGatesConfig(), nil)

	p.RecordRegression("test regression")
	d := p.Evaluate(gateInput())
	if d.AutoMerge || firstFailed(d) != "cooldown" {
		t.Errorf("merge during cooldown should hit cooldown, got merge=%v failed=%v", d.AutoMerge, d.GatesFailed)
	}
}

func firstFailed(d *Decision) string {
	if len(d.GatesFailed) == 0 {
		return ""
	}
	return d.GatesFailed[0]
}

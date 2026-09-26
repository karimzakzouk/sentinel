package automerge

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Config is the immutable auto-merge configuration. Built from
// config.Config.{AutoMergeEnabled, AutoMergeNamespaces, ...} in cmd/sentinel.
type Config struct {
	Enabled              bool
	Namespaces           []string
	MinConfidence        float64
	RequireCIGreen       bool
	CITimeoutSec         int
	MaxPerHour           int
	CooldownAfterFailMin int

	// MinSandboxLevel is the weakest sandbox-verification level that may
	// still auto-merge, expressed as the highest level that must have
	// passed. 3 = L1-L3 (pod ready + health endpoints + error gone) —
	// the historical minimum. 5 = require the full suite including
	// regression test + reproducer. 0 disables the gate (not
	// recommended — SandboxVerified alone doesn't say how far
	// verification got).
	MinSandboxLevel int

	// MaxCIRetries bounds the CI-failure feedback loop: when CI fails on a
	// tracked PR, the merger feeds the failing test output back to the fix
	// proposer, which pushes a corrected commit to the same branch (CI then
	// re-runs automatically). After MaxCIRetries revisions the PR is
	// untracked for human review. 0 disables the loop (old behavior).
	MaxCIRetries int
}

// DefaultConfig returns the safe defaults. Auto-merge is OFF by default —
// operators must explicitly enable it.
func DefaultConfig() Config {
	return Config{
		Enabled:              false,
		Namespaces:           nil,
		MinConfidence:        0.85,
		RequireCIGreen:       true,
		CITimeoutSec:         900,
		MaxPerHour:           3,
		CooldownAfterFailMin: 60,
		MinSandboxLevel:      3,
		MaxCIRetries:         1,
	}
}

// Policy evaluates whether a PR is eligible for auto-merge. It is safe for
// concurrent use: Evaluate takes an RLock; RecordMerge / RecordRegression /
// Status take the corresponding locks on their internal rate-limiter and
// cooldown.
type Policy struct {
	cfg           Config
	safetyChecker SafetyChecker
	rateLimiter   *RateLimiter
	cooldown      *Cooldown
	mu            sync.RWMutex
}

// SafetyChecker is implemented by safety.Policy. Decoupled via an interface
// to avoid an import cycle (the safety package does not know about
// automerge).
type SafetyChecker interface {
	IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool
}

// Decision is the result of Policy.Evaluate.
type Decision struct {
	AutoMerge   bool
	Reason      string
	GatesPassed []string
	GatesFailed []string
}

// NewPolicy creates a Policy wired to the given safety checker.
func NewPolicy(cfg Config, safety SafetyChecker) *Policy {
	return &Policy{
		cfg:           cfg,
		safetyChecker: safety,
		rateLimiter:   NewRateLimiter(cfg.MaxPerHour, time.Hour),
		cooldown:      NewCooldown(time.Duration(cfg.CooldownAfterFailMin) * time.Minute),
	}
}

// Input is the data the policy evaluates. CI status and label flags are
// refreshed by the merger on every tick; the rest comes from the incident
// state.
type Input struct {
	IncidentID         string
	Service            string
	Namespace          string
	FixType            string
	HasSchemaMigration bool
	IsHighRisk         bool
	Confidence         float64
	SandboxVerified    bool
	// SandboxLevels is how far sandbox verification got ("L1-L5",
	// "L1-L3", "" when not verified). Gate 3.5 enforces MinSandboxLevel.
	SandboxLevels       string
	CIStatus            CIStatus
	AutoMergeAnnotation bool
	HoldLabel           bool
	NoMergeLabel        bool
	ApprovedLabel       bool
	PolicyVerdict       string

	// SandboxOverride / CIOverride are set by /sentinel override. When true,
	// the corresponding gate is treated as passed.
	SandboxOverride bool
	CIOverride      bool

	// CIKnownPreexisting is set when the pre-PR repo-checks gate ran the
	// repo's own CI commands on BOTH the fix branch and the base branch and
	// established that every failure is preexisting on base (new_failures=0,
	// preexisting>0). GitHub CI on such a repo is permanently red through
	// no fault of the fix: run #30's shipping-api PR carried a correct,
	// sandbox-verified fix but was stuck in the CI-revision loop for the
	// whole eval because a base-branch test failure painted the PR red.
	// Gate 9 treats red as preexisting instead of blocking, and the merger
	// skips the CI-failure revision loop.
	CIKnownPreexisting bool
}

// CIStatus is the aggregated state of CI checks on the PR head SHA.
type CIStatus struct {
	State   string // "pending", "success", "failure", "neutral", "unknown"
	Checks  int
	Passing int
	Failing int
	Pending int
}

// Evaluate runs all 11 gates and returns a Decision. Gates are evaluated in
// order; the first failing gate short-circuits and produces a Reason.
func (p *Policy) Evaluate(in Input) *Decision {
	p.mu.RLock()
	defer p.mu.RUnlock()

	d := &Decision{}

	// Gate 0: master switch.
	if !p.cfg.Enabled {
		d.Reason = "auto-merge is disabled globally (SENTINEL_AUTO_MERGE_ENABLED=false)"
		return d
	}

	// Gate 0.5: hold / no-merge labels block immediately.
	if in.NoMergeLabel {
		d.Reason = "PR has `sentinel/no-merge` label — run `/sentinel no-merge cancel` to re-enable"
		return d
	}
	if in.HoldLabel {
		d.Reason = "PR has `sentinel/hold` label — run `/sentinel hold cancel` to re-enable"
		return d
	}

	// Gate 1: namespace allowlist.
	if !p.namespaceAllowed(in.Namespace) {
		d.Reason = fmt.Sprintf("namespace %q not in auto-merge allowlist %v", in.Namespace, p.cfg.Namespaces)
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "namespace")

	// Gate 2: per-service annotation.
	if !in.AutoMergeAnnotation {
		d.Reason = "service lacks `sentinel.io/auto-merge: \"true\"` annotation"
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "annotation")

	// Gate 3: sandbox verified (hard) — unless an admin override is set.
	if !in.SandboxVerified && !in.SandboxOverride {
		d.GatesFailed = append(d.GatesFailed, "sandbox")
		d.Reason = "sandbox not verified — auto-merge requires sandbox verification"
		return d
	}
	if in.SandboxOverride && !in.SandboxVerified {
		d.GatesPassed = append(d.GatesPassed, "sandbox (OVERRIDE)")
	} else {
		d.GatesPassed = append(d.GatesPassed, "sandbox")
	}

	// Gate 3.5: sandbox verification DEPTH. "sandbox-verified" alone
	// doesn't say how far verification got — L1-L3 (pod up, healthy,
	// error gone) vs L1-L5 (also regression test + reproducer). Enforce
	// the operator's minimum.
	if p.cfg.MinSandboxLevel > 0 && in.SandboxVerified && !in.SandboxOverride {
		level := parseSandboxLevel(in.SandboxLevels)
		if level < p.cfg.MinSandboxLevel {
			d.GatesFailed = append(d.GatesFailed, "sandbox_level")
			d.Reason = fmt.Sprintf("sandbox verified to %s but auto-merge requires L1-L%d (SENTINEL_AUTOMERGE_MIN_SANDBOX_LEVEL)",
				in.SandboxLevels, p.cfg.MinSandboxLevel)
			return d
		}
		d.GatesPassed = append(d.GatesPassed, "sandbox_level")
	}

	// Gate 4: confidence threshold (hard).
	if in.Confidence < p.cfg.MinConfidence {
		d.GatesFailed = append(d.GatesFailed, "confidence")
		d.Reason = fmt.Sprintf("confidence %.2f < threshold %.2f", in.Confidence, p.cfg.MinConfidence)
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "confidence")

	// Gate 5: safety policy verdict (hard).
	if in.PolicyVerdict == "deny" {
		d.GatesFailed = append(d.GatesFailed, "safety")
		d.Reason = "safety policy denied the fix"
		return d
	}
	if in.PolicyVerdict == "require_human_approval" && !in.ApprovedLabel {
		d.GatesFailed = append(d.GatesFailed, "safety")
		d.Reason = "safety policy requires human approval — run `/sentinel approve` to approve"
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "safety")

	// Gate 6: schema migration — never auto-merge.
	if in.HasSchemaMigration {
		d.GatesFailed = append(d.GatesFailed, "schema_migration")
		d.Reason = "fix involves a schema migration — always requires human review"
		return d
	}

	// Gate 7: rollback — never auto-merge.
	if in.FixType == "rollback" {
		d.GatesFailed = append(d.GatesFailed, "rollback")
		d.Reason = "rollback fixes always require human review"
		return d
	}

	// Gate 8: high-risk service — requires explicit /sentinel approve.
	if in.IsHighRisk && !in.ApprovedLabel {
		d.GatesFailed = append(d.GatesFailed, "high_risk")
		d.Reason = "high-risk service — run `/sentinel approve` to approve for auto-merge"
		return d
	}

	// Gate 9: CI green — unless an admin override is set.
	if p.cfg.RequireCIGreen {
		if in.CIOverride {
			d.GatesPassed = append(d.GatesPassed, "ci (OVERRIDE)")
		} else {
			switch in.CIStatus.State {
			case "failure":
				if in.CIKnownPreexisting {
					// Red CI that the repo-checks gate proved preexisting on the
					// base branch (same commands, sandbox-executed, base-relative
					// comparison): not attributable to this fix. Proceed with a
					// visible annotation so the merge record stays honest.
					d.GatesPassed = append(d.GatesPassed, "ci (preexisting on base)")
				} else {
					d.GatesFailed = append(d.GatesFailed, "ci")
					d.Reason = fmt.Sprintf("CI failing (%d/%d checks failing)", in.CIStatus.Failing, in.CIStatus.Checks)
					return d
				}
			case "success":
				d.GatesPassed = append(d.GatesPassed, "ci")
			case "pending":
				d.GatesFailed = append(d.GatesFailed, "ci")
				d.Reason = "CI pending — waiting for checks to complete"
				return d
			default:
				d.GatesFailed = append(d.GatesFailed, "ci")
				d.Reason = "CI status unknown — cannot auto-merge without check results"
				return d
			}
		}
	}

	// Gate 10: rate limiter.
	if !p.rateLimiter.Allow() {
		d.GatesFailed = append(d.GatesFailed, "rate_limit")
		d.Reason = fmt.Sprintf("rate limit reached (%d merges/hour) — safety pause to prevent cascade merges", p.cfg.MaxPerHour)
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "rate_limit")

	// Gate 11: cooldown.
	if p.cooldown.IsPaused() {
		d.GatesFailed = append(d.GatesFailed, "cooldown")
		d.Reason = fmt.Sprintf("auto-merge paused due to recent regression — resumes at %s", p.cooldown.PausedUntil().Format("15:04:05"))
		return d
	}
	d.GatesPassed = append(d.GatesPassed, "cooldown")

	d.AutoMerge = true
	d.Reason = "all gates passed"
	return d
}

// parseSandboxLevel extracts the highest verified level from a
// LevelsVerified string like "L1-L5". Returns 3 for legacy/empty values
// (L1-L3 was the historical minimum for a verified run).
func parseSandboxLevel(levels string) int {
	if levels == "" {
		return 3
	}
	var max int
	if _, err := fmt.Sscanf(strings.TrimPrefix(levels, "L1-L"), "%d", &max); err != nil {
		return 3
	}
	if max < 1 || max > 5 {
		return 3
	}
	return max
}

// namespaceAllowed returns true if the namespace is in the allowlist. An
// empty allowlist fails closed (no namespaces allowed).
func (p *Policy) namespaceAllowed(ns string) bool {
	for _, allowed := range p.cfg.Namespaces {
		if allowed == ns {
			return true
		}
	}
	return false
}

// RecordMerge records a successful merge (consumes a rate-limiter slot).
func (p *Policy) RecordMerge() {
	p.rateLimiter.Record()
}

// RecordRegression triggers the cooldown. Called by the post-merge outcome
// watcher when a regression is detected.
func (p *Policy) RecordRegression(reason string) {
	p.cooldown.Trigger(reason)
	logging.Info("regression detected — cooldown activated", "reason", reason)
}

// ClearCooldown manually clears the cooldown. Called by /sentinel override
// cooldown.
func (p *Policy) ClearCooldown() {
	p.cooldown.Clear()
}

// Status returns the current rate-limiter + cooldown state for the web UI.
func (p *Policy) Status() PolicyStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return PolicyStatus{
		Enabled:        p.cfg.Enabled,
		Namespaces:     p.cfg.Namespaces,
		MinConfidence:  p.cfg.MinConfidence,
		RequireCIGreen: p.cfg.RequireCIGreen,
		MaxPerHour:     p.cfg.MaxPerHour,
		MergesThisHour: p.rateLimiter.Count(),
		CooldownActive: p.cooldown.IsPaused(),
		CooldownUntil:  p.cooldown.PausedUntil(),
		CooldownReason: p.cooldown.Reason(),
	}
}

// PolicyStatus is the public state of the auto-merge policy.
type PolicyStatus struct {
	Enabled        bool      `json:"enabled"`
	Namespaces     []string  `json:"namespaces"`
	MinConfidence  float64   `json:"min_confidence"`
	RequireCIGreen bool      `json:"require_ci_green"`
	MaxPerHour     int       `json:"max_per_hour"`
	MergesThisHour int       `json:"merges_this_hour"`
	CooldownActive bool      `json:"cooldown_active"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
	CooldownReason string    `json:"cooldown_reason,omitempty"`
}

// formatGatesList renders a gates list as a markdown bullet list for PR
// comments. The marker is ✅ for passed gates, ❌ for failed gates.
func formatGatesList(gates []string, passed bool) string {
	if len(gates) == 0 {
		return "_(none)_"
	}
	marker := "✅"
	if !passed {
		marker = "❌"
	}
	out := make([]string, len(gates))
	for i, g := range gates {
		out[i] = fmt.Sprintf("- %s `%s`", marker, g)
	}
	return strings.Join(out, "\n")
}

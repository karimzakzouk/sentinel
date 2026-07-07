// Package automerge implements the auto-merge feature for Sentinel v4.0.0.
// When a Sentinel-tracked PR passes all gates (sandbox verified + confidence
// threshold + CI green), the merger can auto-merge it — closing the loop
// from detection to deployment without human intervention.
// Three opt-in layers (all must be true to auto-merge):
//   1. Global config: SENTINEL_AUTO_MERGE_ENABLED=true
//   2. Namespace allowlist: SENTINEL_AUTO_MERGE_NAMESPACES=sim,staging
//   3. Per-service annotation: sentinel.io/auto-merge: "true"
// Three gates (all must pass):
//   1. Sandbox verified (hard requirement)
//   2. Confidence >= 0.85 + safety policy allowed (hard)
//   3. CI green (soft by default, hard in strict mode)
// Safety valves:
//   - Rate limit: max 3 auto-merges per hour (prevents cascade → many bad merges)
//   - Cooldown: 1 hour pause after any regression detected
//   - Escape hatch: /sentinel no-merge (chatops)
// labels + gates and is the only thing that actually merges.
package automerge

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
        "fmt"
        "strings"
        "sync"
        "time"
)

// Config is the immutable auto-merge configuration.

type Config struct {
        Enabled              bool
        Namespaces           []string // namespaces where auto-merge is allowed
        MinConfidence        float64  // default 0.85
        RequireCIGreen       bool     // default true
        CITimeoutSec         int      // default 900 (15 min)
        MaxPerHour           int      // default 3
        CooldownAfterFailMin int      // default 60
}

// DefaultConfig returns sensible defaults. Auto-merge is OFF by default —
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
        }
}

// Policy evaluates whether a PR is eligible for auto-merge.
// Returns a Decision with AutoMerge=true if all gates pass, plus a human-
// readable Reason for the PR comment.

type Policy struct {
        cfg          Config
        safetyChecker SafetyChecker
        rateLimiter  *RateLimiter
        cooldown     *Cooldown
        mu           sync.RWMutex
}

// SafetyChecker is implemented by safety.Policy. Decoupled to avoid an
// import cycle (safety package doesn't know about automerge).

type SafetyChecker interface {
        IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool
}

// Decision is the result of Policy.Evaluate.

type Decision struct {
        AutoMerge   bool
        Reason      string   // for the PR comment if skipped
        GatesPassed []string // ["sandbox", "confidence", "ci_green", "namespace", "annotation"]
        GatesFailed []string
}

// NewPolicy creates a Policy.

func NewPolicy(cfg Config, safety SafetyChecker) *Policy {
        return &Policy{
                cfg:          cfg,
                safetyChecker: safety,
                rateLimiter:  NewRateLimiter(cfg.MaxPerHour, time.Hour),
                cooldown:     NewCooldown(time.Duration(cfg.CooldownAfterFailMin) * time.Minute),
        }
}

// Input is the data Policy evaluates.

type Input struct {
        IncidentID         string
        Service            string
        Namespace          string
        FixType            string
        HasSchemaMigration bool
        IsHighRisk         bool
        Confidence         float64
        SandboxVerified    bool
        CIStatus           CIStatus
        AutoMergeAnnotation bool // sentinel.io/auto-merge: "true"
        HoldLabel          bool  // sentinel/hold label set?
        NoMergeLabel       bool  // sentinel/no-merge label set?
        ApprovedLabel      bool  // sentinel/approved label set?
        PolicyVerdict      string // safety.Policy.Evaluate verdict: "allow"|"require_human_approval"|"deny"

        // Admin overrides — set by /sentinel override sandbox|ci.
        // When true, the corresponding gate is treated as passed.
        SandboxOverride bool
        CIOverride      bool
}

// CIStatus is the state of CI checks on the PR.

type CIStatus struct {
        State    string // "pending", "success", "failure", "neutral", "unknown"
        Checks   int
        Passing  int
        Failing  int
        Pending  int
}

// Evaluate runs all gates and returns a Decision.

func (p *Policy) Evaluate(in Input) *Decision {
        p.mu.RLock()
        defer p.mu.RUnlock()

        d := &Decision{}

        // Gate 0: master switch

        if !p.cfg.Enabled {
                d.Reason = "auto-merge is disabled globally (SENTINEL_AUTO_MERGE_ENABLED=false)"
                return d
        }

        // Gate 0.5: hold / no-merge labels block immediately

        if in.NoMergeLabel {
                d.Reason = "PR has `sentinel/no-merge` label — run `/sentinel no-merge cancel` to re-enable"
                return d
        }
        if in.HoldLabel {
                d.Reason = "PR has `sentinel/hold` label — run `/sentinel hold cancel` to re-enable"
                return d
        }

        // Gate 1: namespace allowlist

        if !p.namespaceAllowed(in.Namespace) {
                d.Reason = fmt.Sprintf("namespace %q not in auto-merge allowlist %v", in.Namespace, p.cfg.Namespaces)
                return d
        }
        d.GatesPassed = append(d.GatesPassed, "namespace")

        // Gate 2: per-service annotation

        if !in.AutoMergeAnnotation {
                d.Reason = "service lacks `sentinel.io/auto-merge: \"true\"` annotation"
                return d
        }
        d.GatesPassed = append(d.GatesPassed, "annotation")

        // Gate 3: sandbox verified (hard) — unless admin override is set

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

        // Gate 4: confidence threshold (hard)

        if in.Confidence < p.cfg.MinConfidence {
                d.GatesFailed = append(d.GatesFailed, "confidence")
                d.Reason = fmt.Sprintf("confidence %.2f < threshold %.2f", in.Confidence, p.cfg.MinConfidence)
                return d
        }
        d.GatesPassed = append(d.GatesPassed, "confidence")

        // Gate 5: safety policy verdict (hard)

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

        // Gate 6: schema migration (hard — never auto-merge schema migrations)

        if in.HasSchemaMigration {
                d.GatesFailed = append(d.GatesFailed, "schema_migration")
                d.Reason = "fix involves a schema migration — always requires human review"
                return d
        }

        // Gate 7: rollback (hard — never auto-merge rollbacks)

        if in.FixType == "rollback" {
                d.GatesFailed = append(d.GatesFailed, "rollback")
                d.Reason = "rollback fixes always require human review"
                return d
        }

        // Gate 8: high-risk service (hard — requires explicit /sentinel approve)

        if in.IsHighRisk && !in.ApprovedLabel {
                d.GatesFailed = append(d.GatesFailed, "high_risk")
                d.Reason = "high-risk service — run `/sentinel approve` to approve for auto-merge"
                return d
        }

        // Gate 9: CI green — unless admin override is set

        if p.cfg.RequireCIGreen {
                if in.CIOverride {
                        d.GatesPassed = append(d.GatesPassed, "ci (OVERRIDE)")
                } else {
                        switch in.CIStatus.State {
                        case "failure":
                                d.GatesFailed = append(d.GatesFailed, "ci")
                                d.Reason = fmt.Sprintf("CI failing (%d/%d checks failing)", in.CIStatus.Failing, in.CIStatus.Checks)
                                return d
                        case "pending":
                                // Don't fail — the merger will wait for CI to complete.
                                d.GatesPassed = append(d.GatesPassed, "ci_pending")
                        case "success":
                                d.GatesPassed = append(d.GatesPassed, "ci")
                        default:
                                d.GatesPassed = append(d.GatesPassed, "ci_unknown")
                        }
                }
        }

        // Gate 10: rate limiter

        if !p.rateLimiter.Allow() {
                d.GatesFailed = append(d.GatesFailed, "rate_limit")
                d.Reason = fmt.Sprintf("rate limit reached (%d merges/hour) — safety pause to prevent cascade merges", p.cfg.MaxPerHour)
                return d
        }
        d.GatesPassed = append(d.GatesPassed, "rate_limit")

        // Gate 11: cooldown

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

// namespaceAllowed returns true if the namespace is in the allowlist.
// Empty allowlist = no namespaces allowed (fail closed).

func (p *Policy) namespaceAllowed(ns string) bool {
        for _, allowed := range p.cfg.Namespaces {
                if allowed == ns {
                        return true
                }
        }
        return false
}

// RecordMerge records a successful merge (decrements the rate limiter).

func (p *Policy) RecordMerge() {
        p.rateLimiter.Record()
}

// RecordRegression triggers the cooldown (pause auto-merge for the configured duration).
// Called by the outcome watcher when a regression is detected post-merge.

func (p *Policy) RecordRegression(reason string) {
        p.cooldown.Trigger(reason)
        logging.Info("regression detected — cooldown activated", "reason", reason)
}

// Status returns the current rate-limiter + cooldown state for the web UI.

func (p *Policy) Status() PolicyStatus {
        p.mu.RLock()
        defer p.mu.RUnlock()
        return PolicyStatus{
                Enabled:           p.cfg.Enabled,
                Namespaces:        p.cfg.Namespaces,
                MinConfidence:     p.cfg.MinConfidence,
                RequireCIGreen:    p.cfg.RequireCIGreen,
                MaxPerHour:        p.cfg.MaxPerHour,
                MergesThisHour:    p.rateLimiter.Count(),
                CooldownActive:    p.cooldown.IsPaused(),
                CooldownUntil:     p.cooldown.PausedUntil(),
                CooldownReason:    p.cooldown.Reason(),
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

// formatGatesList renders the gates list as a markdown list for PR comments.

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

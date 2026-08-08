package automerge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockSafetyChecker implements SafetyChecker for tests.
type mockSafetyChecker struct{ allowed bool }

func (m *mockSafetyChecker) IsAutoMergeAllowed(fixType string, hasSchemaMigration bool, isHighRisk bool) bool {
	return m.allowed
}

// allGatesPassInput returns an Input that passes every gate (assuming a
// permissive config + allowlisted namespace).
func allGatesPassInput() Input {
	return Input{
		IncidentID:          "inc-1",
		Service:             "demo-api",
		Namespace:           "sim",
		FixType:             "code_fix",
		Confidence:          0.95,
		SandboxVerified:     true,
		CIStatus:            CIStatus{State: "success", Checks: 3, Passing: 3},
		AutoMergeAnnotation: true,
		PolicyVerdict:       "allow",
	}
}

func TestPolicy_DisabledGlobally(t *testing.T) {
	p := NewPolicy(Config{Enabled: false}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(allGatesPassInput())
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when globally disabled")
	}
}

func TestPolicy_NamespaceNotAllowed(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.Namespace = "prod"
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when namespace not allowed")
	}
}

func TestPolicy_MissingAnnotation(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.AutoMergeAnnotation = false
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when annotation missing")
	}
}

func TestPolicy_SandboxNotVerified(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.SandboxVerified = false
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when sandbox not verified")
	}
}

func TestPolicy_SandboxOverride(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.SandboxVerified = false
	in.SandboxOverride = true
	d := p.Evaluate(in)
	if !d.AutoMerge {
		t.Errorf("expected AutoMerge=true with sandbox override, got: %s", d.Reason)
	}
}

func TestPolicy_LowConfidence(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.Confidence = 0.50
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when confidence below threshold")
	}
}

func TestPolicy_SchemaMigrationBlocked(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.HasSchemaMigration = true
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false for schema migration")
	}
}

func TestPolicy_RollbackBlocked(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.FixType = "rollback"
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false for rollback fix type")
	}
}

func TestPolicy_HoldLabelBlocks(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.HoldLabel = true
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when hold label set")
	}
}

func TestPolicy_NoMergeLabelBlocks(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.NoMergeLabel = true
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when no-merge label set")
	}
}

func TestPolicy_HighRiskRequiresApproval(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85}, &mockSafetyChecker{allowed: true})

	in := allGatesPassInput()
	in.IsHighRisk = true
	in.ApprovedLabel = false
	if d := p.Evaluate(in); d.AutoMerge {
		t.Error("expected AutoMerge=false for high-risk without approval")
	}

	in.ApprovedLabel = true
	if d := p.Evaluate(in); !d.AutoMerge {
		t.Errorf("expected AutoMerge=true for high-risk with approval, got: %s", d.Reason)
	}
}

func TestPolicy_SafetyVerdictDeny(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: false})
	in := allGatesPassInput()
	in.PolicyVerdict = "deny"
	d := p.Evaluate(in)
	if d.AutoMerge {
		t.Error("expected AutoMerge=false when safety policy denies")
	}
}

func TestPolicy_SafetyVerdictRequireHumanApproval(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.PolicyVerdict = "require_human_approval"
	in.ApprovedLabel = false
	if d := p.Evaluate(in); d.AutoMerge {
		t.Error("expected AutoMerge=false without approval when safety requires human approval")
	}
	in.ApprovedLabel = true
	if d := p.Evaluate(in); !d.AutoMerge {
		t.Errorf("expected AutoMerge=true with approval label, got: %s", d.Reason)
	}
}

func TestPolicy_AllGatesPass(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		MaxPerHour:     3,
	}, &mockSafetyChecker{allowed: true})
	d := p.Evaluate(allGatesPassInput())
	if !d.AutoMerge {
		t.Errorf("expected AutoMerge=true, got false. Reason: %s, GatesFailed: %v", d.Reason, d.GatesFailed)
	}
}

// TestPolicy_All11Gates verifies every gate produces the correct gate name
// in GatesPassed/GatesFailed when triggered.
func TestPolicy_All11Gates(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		// High MaxPerHour so the rate-limit gate does not interfere with
		// the cooldown / CI-failure assertions below.
		MaxPerHour: 100,
	}, &mockSafetyChecker{allowed: true})

	// All gates pass on the baseline evaluation.
	d1 := p.Evaluate(allGatesPassInput())
	if !d1.AutoMerge {
		t.Fatalf("baseline: expected all gates to pass, got: %s", d1.Reason)
	}
	for _, g := range []string{"namespace", "annotation", "sandbox", "confidence", "safety", "ci", "rate_limit", "cooldown"} {
		if !contains(d1.GatesPassed, g) {
			t.Errorf("baseline: expected GatesPassed to contain %q, got %v", g, d1.GatesPassed)
		}
	}

	// Trigger a cooldown; the cooldown gate should fail.
	p.RecordRegression("simulated regression")
	d3 := p.Evaluate(allGatesPassInput())
	if d3.AutoMerge {
		t.Fatal("expected cooldown gate to block after a regression")
	}
	if !contains(d3.GatesFailed, "cooldown") {
		t.Errorf("expected GatesFailed to contain cooldown, got %v", d3.GatesFailed)
	}
	p.ClearCooldown()

	// Pending CI is not a hard failure — it appears in GatesPassed as
	// ci_pending.
	pendingIn := allGatesPassInput()
	pendingIn.CIStatus = CIStatus{State: "pending", Checks: 2, Pending: 2}
	dPending := p.Evaluate(pendingIn)
	if !contains(dPending.GatesPassed, "ci_pending") {
		t.Errorf("expected GatesPassed to contain ci_pending, got %v", dPending.GatesPassed)
	}

	// CI failure hard-fails at the ci gate.
	failIn := allGatesPassInput()
	failIn.CIStatus = CIStatus{State: "failure", Checks: 2, Failing: 1}
	dFail := p.Evaluate(failIn)
	if dFail.AutoMerge {
		t.Error("expected AutoMerge=false when CI is failing")
	}
	if !contains(dFail.GatesFailed, "ci") {
		t.Errorf("expected GatesFailed to contain ci, got %v", dFail.GatesFailed)
	}

	// Sandbox override path shows up as "sandbox (OVERRIDE)".
	overrideIn := allGatesPassInput()
	overrideIn.SandboxVerified = false
	overrideIn.SandboxOverride = true
	dOverride := p.Evaluate(overrideIn)
	if !dOverride.AutoMerge {
		t.Errorf("expected AutoMerge=true with sandbox override, got: %s", dOverride.Reason)
	}
	if !contains(dOverride.GatesPassed, "sandbox (OVERRIDE)") {
		t.Errorf("expected GatesPassed to contain sandbox (OVERRIDE), got %v", dOverride.GatesPassed)
	}

	// CI override path shows up as "ci (OVERRIDE)".
	ciOverrideIn := allGatesPassInput()
	ciOverrideIn.CIStatus = CIStatus{State: "failure", Checks: 1, Failing: 1}
	ciOverrideIn.CIOverride = true
	dCIOverride := p.Evaluate(ciOverrideIn)
	if !dCIOverride.AutoMerge {
		t.Errorf("expected AutoMerge=true with CI override, got: %s", dCIOverride.Reason)
	}
	if !contains(dCIOverride.GatesPassed, "ci (OVERRIDE)") {
		t.Errorf("expected GatesPassed to contain ci (OVERRIDE), got %v", dCIOverride.GatesPassed)
	}

	// Rate-limit gate: when MaxPerHour=1 and one merge has already happened,
	// the second evaluation must fail at the rate_limit gate.
	pLimited := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		MaxPerHour:     1,
	}, &mockSafetyChecker{allowed: true})
	if d := pLimited.Evaluate(allGatesPassInput()); !d.AutoMerge {
		t.Fatalf("limited policy: baseline should pass, got: %s", d.Reason)
	}
	pLimited.RecordMerge()
	dRL := pLimited.Evaluate(allGatesPassInput())
	if dRL.AutoMerge {
		t.Fatal("expected rate-limit gate to block the second merge")
	}
	if !contains(dRL.GatesFailed, "rate_limit") {
		t.Errorf("expected GatesFailed to contain rate_limit, got %v", dRL.GatesFailed)
	}
}

// TestPolicy_CIFailing is kept for parity with the v5 test suite.
func TestPolicy_CIFailing(t *testing.T) {
	p := NewPolicy(Config{Enabled: true, Namespaces: []string{"sim"}, MinConfidence: 0.85, RequireCIGreen: true}, &mockSafetyChecker{allowed: true})
	in := allGatesPassInput()
	in.CIStatus = CIStatus{State: "failure", Checks: 3, Failing: 1}
	d := p.Evaluate(in)
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

// TestRateLimiter_TokenBucket verifies that the limiter admits exactly max
// events in a window and rejects the (max+1)th.
func TestRateLimiter_TokenBucket(t *testing.T) {
	rl := NewRateLimiter(5, time.Hour)
	for i := 0; i < 5; i++ {
		if !rl.Record() {
			t.Fatalf("expected Record #%d to be admitted", i+1)
		}
	}
	if rl.Record() {
		t.Error("expected 6th Record to be rejected")
	}
	if rl.Count() != 5 {
		t.Errorf("expected Count=5, got %d", rl.Count())
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

// TestRateLimiter_CooldownAfterRegression verifies that a regression triggers
// the cooldown, blocking auto-merge, and that ClearCooldown releases it.
func TestRateLimiter_CooldownAfterRegression(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		MaxPerHour:     5,
	}, &mockSafetyChecker{allowed: true})

	// Baseline passes.
	if d := p.Evaluate(allGatesPassInput()); !d.AutoMerge {
		t.Fatalf("baseline should pass, got %s", d.Reason)
	}

	// After a regression, the cooldown gate should block.
	p.RecordRegression("regression in production")
	d := p.Evaluate(allGatesPassInput())
	if d.AutoMerge {
		t.Fatal("expected cooldown to block auto-merge after regression")
	}
	if !contains(d.GatesFailed, "cooldown") {
		t.Errorf("expected GatesFailed to contain cooldown, got %v", d.GatesFailed)
	}
	if d.Reason == "" {
		t.Error("expected non-empty reason")
	}

	// After ClearCooldown, the next evaluation should pass again.
	p.ClearCooldown()
	if d := p.Evaluate(allGatesPassInput()); !d.AutoMerge {
		t.Errorf("expected AutoMerge=true after clearing cooldown, got: %s", d.Reason)
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

// mockGitHubClient is a minimal GitHubClient used by the merger tests.
type mockGitHubClient struct {
	mu           sync.Mutex
	mergeCount   int
	mergeErr     error
	commentCount int
	labels       []string
	ciStatus     CIStatus
}

func (g *mockGitHubClient) GetCIStatus(ctx context.Context, repo string, prNumber int) (CIStatus, error) {
	return g.ciStatus, nil
}

func (g *mockGitHubClient) GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error) {
	return g.labels, nil
}

func (g *mockGitHubClient) Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mergeCount++
	if g.mergeErr != nil {
		return g.mergeErr
	}
	return nil
}

func (g *mockGitHubClient) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commentCount++
	return nil
}

func (g *mockGitHubClient) RevertMergeCommit(ctx context.Context, repo, sha, base, reason string) (int, error) {
	return 0, errors.New("not implemented in mock")
}

type mockStore struct {
	statuses []string
}

func (s *mockStore) UpdateAutoMergeStatus(ctx context.Context, incidentID, status string) error {
	s.statuses = append(s.statuses, status)
	return nil
}

// TestMerger_PerPRMutexPreventsDoubleMerge is the regression test for the
// double-merge bug: EvaluateNow and the poll loop must not both merge the
// same PR.
func TestMerger_PerPRMutexPreventsDoubleMerge(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		MaxPerHour:     100,
	}, &mockSafetyChecker{allowed: true})

	gh := &mockGitHubClient{
		ciStatus: CIStatus{State: "success", Checks: 1, Passing: 1},
	}
	store := &mockStore{}
	m := NewMerger(p, gh, store, nil)

	pr := &TrackedPR{
		IncidentID: "inc-1",
		Repo:       "org/repo",
		PRNumber:   42,
		BaseBranch: "main",
		Input:      allGatesPassInput(),
	}
	m.Track(pr)

	// Race EvaluateNow against itself from many goroutines. Only one merge
	// should happen.
	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			m.EvaluateNow(context.Background(), pr.Repo, pr.PRNumber)
		}()
	}
	wg.Wait()

	if gh.mergeCount != 1 {
		t.Errorf("expected exactly 1 merge, got %d (per-PR mutex failed)", gh.mergeCount)
	}
	if m.IsTracked(pr.Repo, pr.PRNumber) {
		t.Error("expected PR to be untracked after merge")
	}
}

// TestMerger_HardGateFailureUntracks verifies a hard gate failure untracks
// the PR + posts a comment.
func TestMerger_HardGateFailureUntracks(t *testing.T) {
	p := NewPolicy(Config{
		Enabled:        true,
		Namespaces:     []string{"sim"},
		MinConfidence:  0.85,
		RequireCIGreen: true,
		MaxPerHour:     100,
	}, &mockSafetyChecker{allowed: true})

	gh := &mockGitHubClient{ciStatus: CIStatus{State: "failure", Checks: 1, Failing: 1}}
	m := NewMerger(p, gh, &mockStore{}, nil)

	pr := &TrackedPR{
		IncidentID: "inc-1",
		Repo:       "org/repo",
		PRNumber:   1,
		BaseBranch: "main",
		Input:      allGatesPassInput(),
	}
	m.Track(pr)
	m.EvaluateNow(context.Background(), pr.Repo, pr.PRNumber)

	if m.IsTracked(pr.Repo, pr.PRNumber) {
		t.Error("expected PR to be untracked after CI failure")
	}
	if gh.mergeCount != 0 {
		t.Errorf("expected no merges on CI failure, got %d", gh.mergeCount)
	}
	if gh.commentCount == 0 {
		t.Error("expected a comment posting the failure reason")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- fixtures ---

// scenarioFixture writes a minimal valid scenario (scenario.yaml +
// cluster/deployment.yaml + ground-truth.diff + evidence.md) and a
// MANIFEST.yaml listing it, all under dir/scenarios/. Returns the path
// to dir/scenarios.
func scenarioFixture(t *testing.T, dir, id, slug string) string {
	t.Helper()
	scnDir := filepath.Join(dir, "scenarios", id+"-"+slug)
	clusterDir := filepath.Join(scnDir, "cluster")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	scenarioYAML := `id: ` + id + `
slug: ` + slug + `
title: "fixture"
category: crashloop
severity: sev2
difficulty: trivial
origin: synthetic

time_budget: 1s
token_budget: 1000
cost_budget_usd: 0.50

setup:
  cluster:
    kind_version: v0.24.0
    kubernetes_version: v1.30.0
    nodes: 1
  apply:
    - cluster/deployment.yaml
  source_repo: "https://github.com/sentinel-bench/fixture"
  source_ref: "main"
  source_path: "deployment/k8s.yaml"

trigger:
  type: alertmanager
  alert:
    alertname: KubePodCrashLooping
    namespace: prod
    pod: fixture-xxxxx-yyyyy
    container: app
    restarts: 5
    labels:
      severity: critical
      service: fixture
    annotations:
      summary: "fixture crash-looping"

assertions:
  - type: pr_opened
    weight: 0.25
  - type: pr_ci_green
    weight: 0.25
  - type: diff_similarity
    threshold: 0.80
    weight: 0.25
  - type: sentinel_automerge_approved
    weight: 0.25

ground_truth: ground-truth.diff
evidence: evidence.md
`
	if err := os.WriteFile(filepath.Join(scnDir, "scenario.yaml"), []byte(scenarioYAML), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}

	deploymentYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: fixture
  namespace: prod
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fixture
  template:
    metadata:
      labels:
        app: fixture
    spec:
      containers:
        - name: app
          image: ghcr.io/sentinel-bench/fixture:v1.0.0
`
	if err := os.WriteFile(filepath.Join(clusterDir, "deployment.yaml"), []byte(deploymentYAML), 0o644); err != nil {
		t.Fatalf("write deployment.yaml: %v", err)
	}

	groundTruth := `--- a/deployment/k8s.yaml
+++ b/deployment/k8s.yaml
@@ -12,7 +12,7 @@ spec:
     spec:
       containers:
         - name: app
-          image: ghcr.io/sentinel-bench/fixture:v1.0.0
+          image: ghcr.io/sentinel-bench/fixture:v1.0.1
`
	if err := os.WriteFile(filepath.Join(scnDir, "ground-truth.diff"), []byte(groundTruth), 0o644); err != nil {
		t.Fatalf("write ground-truth.diff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scnDir, "evidence.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatalf("write evidence.md: %v", err)
	}

	// Compute the SHA256 of the ground-truth.diff for the manifest.
	s := &Scenario{Dir: scnDir, GroundTruthPath: "ground-truth.diff"}
	hash, err := s.GroundTruthSHA256()
	if err != nil {
		t.Fatalf("sha256: %v", err)
	}

	manifest := `version: 1
scenarios:
  - id: ` + id + `
    slug: ` + slug + `
    category: crashloop
    severity: sev2
    difficulty: trivial
    origin: synthetic
    ground_truth_sha256: ` + hash + `
    time_budget: 1s
    token_budget: 1000
    cost_budget_usd: 0.50
`
	if err := os.WriteFile(filepath.Join(dir, "scenarios", "MANIFEST.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write MANIFEST.yaml: %v", err)
	}
	return filepath.Join(dir, "scenarios")
}

// --- fakes ---

// fakeProvisioner records Create/Delete calls without doing any work.
type fakeProvisioner struct {
	created []string
	deleted []string
}

func (f *fakeProvisioner) Create(_ context.Context, name string) error {
	f.created = append(f.created, name)
	return nil
}
func (f *fakeProvisioner) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

// fakeApplier records the manifests it was asked to apply.
type fakeApplier struct {
	applied [][]string
}

func (f *fakeApplier) Apply(_ context.Context, manifests []string) error {
	f.applied = append(f.applied, manifests)
	return nil
}

// fakeDiffFetcher returns a fixed diff for every PR.
type fakeDiffFetcher struct {
	diff string
	err  error
}

func (f fakeDiffFetcher) Fetch(_ context.Context, _ string, _ int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.diff, nil
}

// --- mock Sentinel API ---

// mockSentinelConfig configures a mockSentinel's behavior.
type mockSentinelConfig struct {
	// incidentStatus is returned by GET /incidents/{id}. If empty, the
	// mock returns 404.
	incidentStatus string
	// incidentPRNumber, if non-nil, makes the mock return a PR. nil = no PR.
	incidentPRNumber *int
	// incidentSandboxVerified controls the sandbox_verified field.
	incidentSandboxVerified bool
	// incidentAutoMergeStatus controls the auto_merge_status field.
	incidentAutoMergeStatus string
	// triggerStatus is the HTTP status returned by POST /admin/investigate.
	// 0 (default) = 200.
	triggerStatus int
	// pollAfter counts how many GET /incidents/{id} calls have been made.
	// When non-zero, the mock returns incidentStatus only after this many
	// polls; before that it returns "investigating". Lets us test the
	// polling loop.
	pollAfter int32
}

// mockSentinel returns a httptest.Server that emulates Sentinel's API
// according to cfg. The returned server is already started; tests should
// defer Close it.
func mockSentinel(t *testing.T, cfg mockSentinelConfig) *httptest.Server {
	t.Helper()
	var pollCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  "ok",
				"version": "test",
			})
		case r.URL.Path == "/admin/investigate" && r.Method == http.MethodPost:
			if cfg.triggerStatus != 0 {
				w.WriteHeader(cfg.triggerStatus)
				_, _ = w.Write([]byte(`{"error":"mock trigger failure"}`))
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req InvestigateRequest
			_ = json.Unmarshal(body, &req)
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(InvestigateResponse{
				Status:    "triggered",
				Service:   req.Service,
				ClusterID: "abc123def456",
			})
		case strings.HasPrefix(r.URL.Path, "/incidents/"):
			id := strings.TrimPrefix(r.URL.Path, "/incidents/")
			if cfg.incidentStatus == "" {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
				return
			}
			// atomic.AddInt32 is safe to call concurrently — no mutex needed.
			polls := atomic.AddInt32(&pollCount, 1)
			status := cfg.incidentStatus
			if cfg.pollAfter > 0 && polls <= cfg.pollAfter {
				status = "investigating"
			}
			inc := Incident{
				IncidentID:      id,
				Status:          status,
				PRRepo:          "sentinel-bench/fixture",
				PRNumber:        cfg.incidentPRNumber,
				PRURL:           "https://github.com/sentinel-bench/fixture/pull/1",
				SandboxVerified: cfg.incidentSandboxVerified,
				AutoMergeStatus: cfg.incidentAutoMergeStatus,
			}
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(inc)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"unknown path"}`))
		}
	}))
	return srv
}

// newTestRunner builds a Runner wired to the mock Sentinel + fakes.
func newTestRunner(t *testing.T, srv *httptest.Server, scenariosDir, resultsDir, prDiff string) *Runner {
	t.Helper()
	prNum := 1
	_ = prNum
	client := NewClient(srv.URL, "", 5*time.Second)
	return &Runner{
		Sentinel:     client,
		Provisioner:  &fakeProvisioner{},
		Applier:      &fakeApplier{},
		DiffFetcher:  fakeDiffFetcher{diff: prDiff},
		ScenariosDir: scenariosDir,
		ResultsDir:   resultsDir,
		PollInterval: 10 * time.Millisecond,
	}
}

// --- tests ---

// TestRunner_HappyPath verifies that when Sentinel returns a fully-
// successful incident (PR opened, sandbox verified, auto-merge approved)
// and the PR diff matches the ground truth, all four assertions pass and
// all four metrics are 100%.
func TestRunner_HappyPath(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T001", "fixture-happy")
	resultsDir := filepath.Join(dir, "results")

	prDiff := `--- a/deployment/k8s.yaml
+++ b/deployment/k8s.yaml
@@ -12,7 +12,7 @@ spec:
     spec:
       containers:
         - name: app
-          image: ghcr.io/sentinel-bench/fixture:v1.0.0
+          image: ghcr.io/sentinel-bench/fixture:v1.0.1
`
	prNum := 1
	srv := mockSentinel(t, mockSentinelConfig{
		incidentStatus:          "verified",
		incidentPRNumber:        &prNum,
		incidentSandboxVerified: true,
		incidentAutoMergeStatus: "approved",
	})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, prDiff)
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Scenarios) != 1 {
		t.Fatalf("want 1 scenario, got %d", len(report.Scenarios))
	}
	s := report.Scenarios[0]
	if s.Status != "pass" {
		t.Errorf("status: want pass, got %s (err=%s)", s.Status, s.Error)
	}
	if s.Score != 1.0 {
		t.Errorf("score: want 1.0, got %f", s.Score)
	}
	for _, a := range s.Assertions {
		if !a.Passed {
			t.Errorf("assertion %s: want pass, got fail (%s)", a.Type, a.Detail)
		}
	}
	if report.Metrics.DBR != 1.0 || report.Metrics.VPR != 1.0 ||
		report.Metrics.TAR != 1.0 || report.Metrics.FMR != 1.0 {
		t.Errorf("metrics: want all 1.0, got DBR=%f VPR=%f TAR=%f FMR=%f",
			report.Metrics.DBR, report.Metrics.VPR, report.Metrics.TAR, report.Metrics.FMR)
	}
	// Results files were written.
	if _, err := os.Stat(filepath.Join(resultsDir, "test", report.StartedAt.Format("2006-01-02"), "results.json")); err != nil {
		t.Errorf("results.json not written: %v", err)
	}
}

// TestRunner_NoPR verifies that when Sentinel fails to open a PR, all
// four assertions fail and all four metrics are 0%.
func TestRunner_NoPR(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T002", "fixture-nopr")
	resultsDir := filepath.Join(dir, "results")

	srv := mockSentinel(t, mockSentinelConfig{
		incidentStatus: "failed",
		// incidentPRNumber nil = no PR.
	})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, "")
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	if s.Status != "fail" {
		t.Errorf("status: want fail, got %s", s.Status)
	}
	if s.Score != 0.0 {
		t.Errorf("score: want 0.0, got %f", s.Score)
	}
	if report.Metrics.DBR != 0 || report.Metrics.VPR != 0 ||
		report.Metrics.TAR != 0 || report.Metrics.FMR != 0 {
		t.Errorf("metrics: want all 0, got DBR=%f VPR=%f TAR=%f FMR=%f",
			report.Metrics.DBR, report.Metrics.VPR, report.Metrics.TAR, report.Metrics.FMR)
	}
}

// TestRunner_PartialFailure verifies that when Sentinel opens a PR but
// the diff is wrong and CI didn't pass, only pr_opened passes.
func TestRunner_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T003", "fixture-partial")
	resultsDir := filepath.Join(dir, "results")

	// PR diff is disjoint from ground truth -> diff_similarity = 0.
	prDiff := `--- a/deployment/k8s.yaml
+++ b/deployment/k8s.yaml
@@ -1,1 +1,1 @@
-something else
+entirely different
`
	prNum := 7
	srv := mockSentinel(t, mockSentinelConfig{
		incidentStatus:          "verified",
		incidentPRNumber:        &prNum,
		incidentSandboxVerified: false, // CI not green.
		incidentAutoMergeStatus: "rejected",
	})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, prDiff)
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	if s.Status != "fail" {
		t.Errorf("status: want fail, got %s", s.Status)
	}
	// Only pr_opened passes.
	if report.Metrics.DBR != 1.0 {
		t.Errorf("DBR: want 1.0, got %f", report.Metrics.DBR)
	}
	if report.Metrics.VPR != 0 {
		t.Errorf("VPR: want 0, got %f", report.Metrics.VPR)
	}
	if report.Metrics.TAR != 0 {
		t.Errorf("TAR: want 0, got %f", report.Metrics.TAR)
	}
	if report.Metrics.FMR != 0 {
		t.Errorf("FMR: want 0, got %f", report.Metrics.FMR)
	}
}

// TestRunner_TriggerError verifies that when Sentinel's
// POST /admin/investigate returns an error, the scenario status is
// "error" and the error message is recorded.
func TestRunner_TriggerError(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T004", "fixture-trigger-err")
	resultsDir := filepath.Join(dir, "results")

	srv := mockSentinel(t, mockSentinelConfig{
		triggerStatus: 500,
	})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, "")
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	if s.Status != "error" {
		t.Errorf("status: want error, got %s", s.Status)
	}
	if !strings.Contains(s.Error, "trigger") {
		t.Errorf("error: want 'trigger', got %q", s.Error)
	}
	// Errors don't count in any metric numerator.
	if report.Metrics.DBR != 0 || report.Metrics.VPR != 0 ||
		report.Metrics.TAR != 0 || report.Metrics.FMR != 0 {
		t.Errorf("metrics: want all 0, got DBR=%f VPR=%f TAR=%f FMR=%f",
			report.Metrics.DBR, report.Metrics.VPR, report.Metrics.TAR, report.Metrics.FMR)
	}
}

// TestRunner_PollTimeout verifies that the runner stops polling after
// the per-scenario budget elapses and records the last-seen incident.
func TestRunner_PollTimeout(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T005", "fixture-poll-timeout")
	resultsDir := filepath.Join(dir, "results")

	srv := mockSentinel(t, mockSentinelConfig{
		// Always "investigating" — never reaches terminal.
		incidentStatus: "investigating",
		// pollAfter=0 means: return the configured status immediately. So
		// to force non-terminal, we set status to "investigating" itself
		// — IsTerminal() returns false for "investigating".
	})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, "")
	runner.PollInterval = 5 * time.Millisecond
	ctx := context.Background()
	start := time.Now()
	report, err := runner.Run(ctx, RunOptions{Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	// Status is "fail" (no assertions passed) but NOT "error" — the
	// poll timed out cleanly and we have a last-seen incident.
	if s.Status == "error" {
		t.Errorf("status: want fail or pass, got error (%s)", s.Error)
	}
	// The runner should have respected the timeout (within a generous
	// tolerance — the polling loop checks the deadline between ticks).
	if elapsed > 2*time.Second {
		t.Errorf("runner took too long: %v (timeout was 200ms)", elapsed)
	}
}

// TestRunner_PollWaitsForTerminal verifies that the runner keeps polling
// until the incident reaches a terminal state, even if the first few
// responses are "investigating".
func TestRunner_PollWaitsForTerminal(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T006", "fixture-poll-wait")
	resultsDir := filepath.Join(dir, "results")

	prNum := 1
	srv := mockSentinel(t, mockSentinelConfig{
		incidentStatus:          "verified",
		incidentPRNumber:        &prNum,
		incidentSandboxVerified: true,
		incidentAutoMergeStatus: "approved",
		// Return "investigating" for the first 2 polls, then "verified".
		pollAfter: 2,
	})
	defer srv.Close()

	prDiff := `--- a/deployment/k8s.yaml
+++ b/deployment/k8s.yaml
@@ -12,7 +12,7 @@ spec:
     spec:
       containers:
         - name: app
-          image: ghcr.io/sentinel-bench/fixture:v1.0.0
+          image: ghcr.io/sentinel-bench/fixture:v1.0.1
`
	runner := newTestRunner(t, srv, scenariosDir, resultsDir, prDiff)
	runner.PollInterval = 10 * time.Millisecond
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	if s.Status != "pass" {
		t.Errorf("status: want pass, got %s (err=%s)", s.Status, s.Error)
	}
}

// TestRunner_ValidateRejectsBrokenScenario verifies that a scenario with
// a SHA256 drift in the manifest is rejected before any I/O happens.
func TestRunner_ValidateRejectsBrokenScenario(t *testing.T) {
	dir := t.TempDir()
	scenariosDir := scenarioFixture(t, dir, "T007", "fixture-broken")
	resultsDir := filepath.Join(dir, "results")

	// Corrupt the manifest's sha256.
	manifestPath := filepath.Join(scenariosDir, "MANIFEST.yaml")
	data, _ := os.ReadFile(manifestPath)
	corrupted := strings.Replace(string(data), "ground_truth_sha256: ", "ground_truth_sha256: 0000", 1)
	if err := os.WriteFile(manifestPath, []byte(corrupted), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	srv := mockSentinel(t, mockSentinelConfig{incidentStatus: "verified"})
	defer srv.Close()

	runner := newTestRunner(t, srv, scenariosDir, resultsDir, "")
	ctx := context.Background()
	report, err := runner.Run(ctx, RunOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scenarios[0]
	if s.Status != "error" {
		t.Errorf("status: want error, got %s", s.Status)
	}
	if !strings.Contains(s.Error, "SHA256") && !strings.Contains(s.Error, "drift") {
		t.Errorf("error: want 'drift' or 'SHA256', got %q", s.Error)
	}
}

// TestEvaluateAssertions unit-tests the assertion evaluator directly.
func TestEvaluateAssertions(t *testing.T) {
	assertions := []Assertion{
		{Type: "pr_opened", Weight: 0.25},
		{Type: "pr_ci_green", Weight: 0.25},
		{Type: "diff_similarity", Weight: 0.25, Threshold: 0.80},
		{Type: "sentinel_automerge_approved", Weight: 0.25},
	}
	prNum := 1
	inc := Incident{
		Status:          "verified",
		PRNumber:        &prNum,
		SandboxVerified: true,
		AutoMergeStatus: "approved",
	}
	prDiff := `--- a/foo
+++ b/foo
@@ -1,1 +1,1 @@
-a
+b
`
	gtDiff := prDiff // identical -> similarity 1.0

	results := evaluateAssertions(assertions, inc, prDiff, gtDiff)
	if len(results) != 4 {
		t.Fatalf("want 4 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("assertion %s: want pass, got fail (%s)", r.Type, r.Detail)
		}
	}

	// diff_similarity assertion records observed + threshold.
	var ds *AssertionResult
	for i := range results {
		if results[i].Type == "diff_similarity" {
			ds = &results[i]
			break
		}
	}
	if ds == nil {
		t.Fatal("no diff_similarity result")
	}
	if ds.Observed != 1.0 {
		t.Errorf("observed: want 1.0, got %f", ds.Observed)
	}
	if ds.Threshold != 0.80 {
		t.Errorf("threshold: want 0.80, got %f", ds.Threshold)
	}
}

// TestScenarioScore verifies the weighted-sum scoring.
func TestScenarioScore(t *testing.T) {
	// 2 of 4 assertions pass with equal weight -> 0.5.
	results := []AssertionResult{
		{Type: "pr_opened", Weight: 0.25, Passed: true},
		{Type: "pr_ci_green", Weight: 0.25, Passed: false},
		{Type: "diff_similarity", Weight: 0.25, Passed: true},
		{Type: "sentinel_automerge_approved", Weight: 0.25, Passed: false},
	}
	if got := scenarioScore(results); got != 0.5 {
		t.Fatalf("score: want 0.5, got %f", got)
	}

	// All pass -> 1.0.
	for i := range results {
		results[i].Passed = true
	}
	if got := scenarioScore(results); got != 1.0 {
		t.Fatalf("score (all pass): want 1.0, got %f", got)
	}

	// Empty -> 0 (avoids divide-by-zero).
	if got := scenarioScore(nil); got != 0 {
		t.Fatalf("score (empty): want 0, got %f", got)
	}
}

// TestFilterScenarios verifies the ID filter.
func TestFilterScenarios(t *testing.T) {
	mk := func(id string) *Scenario { return &Scenario{ID: id} }
	all := []*Scenario{mk("A"), mk("B"), mk("C")}

	got := filterScenarios(all, nil)
	if len(got) != 3 {
		t.Fatalf("no filter: want 3, got %d", len(got))
	}

	got = filterScenarios(all, []string{"A", "C"})
	if len(got) != 2 {
		t.Fatalf("filter A,C: want 2, got %d", len(got))
	}
	if got[0].ID != "A" || got[1].ID != "C" {
		t.Errorf("filter order: want A,C, got %s,%s", got[0].ID, got[1].ID)
	}

	got = filterScenarios(all, []string{"X"})
	if len(got) != 0 {
		t.Fatalf("filter X: want 0, got %d", len(got))
	}
}

// TestIncident_IsTerminal covers the terminal-status switch.
func TestIncident_IsTerminal(t *testing.T) {
	terminals := []string{"verified", "resolved", "failed", "rejected", "escalated"}
	for _, s := range terminals {
		if !(Incident{Status: s}.IsTerminal()) {
			t.Errorf("%s: want terminal", s)
		}
	}
	nonTerminals := []string{"", "investigating", "detected", "root_caused", "fix_proposed", "approved", "executing"}
	for _, s := range nonTerminals {
		if (Incident{Status: s}).IsTerminal() {
			t.Errorf("%s: want non-terminal", s)
		}
	}
}

// TestIncident_HasPR / AutoMergeApproved / CIGreen cover the convenience
// predicates on Incident.
func TestIncident_Predicates(t *testing.T) {
	prNum := 0
	no := Incident{}
	if no.HasPR() {
		t.Error("no PR: want HasPR=false")
	}
	if no.AutoMergeApproved() {
		t.Error("empty automerge: want false")
	}
	if no.CIGreen() {
		t.Error("no sandbox: want false")
	}

	prNum = 5
	yes := Incident{
		PRNumber:        &prNum,
		SandboxVerified: true,
		AutoMergeStatus: "approved",
	}
	if !yes.HasPR() {
		t.Error("PR=5: want HasPR=true")
	}
	if !yes.AutoMergeApproved() {
		t.Error("approved: want true")
	}
	if !yes.CIGreen() {
		t.Error("sandbox verified: want true")
	}

	// PR number 0 / negative does not count as "has PR".
	zero := Incident{PRNumber: &prNum}
	*zero.PRNumber = 0
	if zero.HasPR() {
		t.Error("PR=0: want HasPR=false")
	}
}

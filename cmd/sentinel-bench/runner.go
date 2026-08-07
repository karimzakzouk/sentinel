// Package main — core runner logic for sentinel-bench.
//
// The runner orchestrates a benchmark run against a list of scenarios:
// for each scenario it provisions a KinD cluster (or uses an existing
// one), applies the cluster manifests, triggers Sentinel via HTTP, polls
// the incident to terminal, fetches the PR diff, checks assertions, and
// records a per-scenario score. Across all scenarios it computes the
// four headline metrics: DBR, VPR, TAR, FMR.
//
// The I/O surface (cluster lifecycle, kubectl apply, PR diff fetch) is
// abstracted behind small interfaces so the runner is testable end-to-end
// with httptest.Server + in-memory fakes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// ClusterProvisioner abstracts KinD cluster lifecycle. The default
// implementation shells out to `kind`; tests use a no-op fake.
type ClusterProvisioner interface {
	Create(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

// Applier abstracts `kubectl apply -f`. The default implementation
// shells out to `kubectl`; tests use a no-op fake.
type Applier interface {
	Apply(ctx context.Context, manifests []string) error
}

// DiffFetcher abstracts fetching the PR diff from GitHub. The default
// implementation calls the GitHub REST API; tests return canned diffs.
type DiffFetcher interface {
	Fetch(ctx context.Context, repo string, prNumber int) (string, error)
}

// Runner orchestrates a benchmark run.
type Runner struct {
	Sentinel     *Client
	Provisioner  ClusterProvisioner
	Applier      Applier
	DiffFetcher  DiffFetcher
	ScenariosDir string
	ResultsDir   string
	PollInterval time.Duration
}

// RunOptions tunes a single Run call.
type RunOptions struct {
	ScenarioIDs     []string      // empty = run all
	Timeout         time.Duration // per-scenario wall-clock budget (0 = scenario default)
	ClusterName     string        // empty = create ephemeral cluster per scenario
	KeepCluster     bool          // do not delete the cluster after each scenario
	SentinelVersion string        // empty = probe GET /healthz
}

// ScenarioResult is the per-scenario outcome.
type ScenarioResult struct {
	ScenarioID string            `json:"scenario_id"`
	Slug       string            `json:"slug"`
	Difficulty string            `json:"difficulty"`
	Status     string            `json:"status"` // "pass", "fail", "error"
	Score      float64           `json:"score"`  // weighted sum of assertion scores
	IncidentID string            `json:"incident_id,omitempty"`
	PRURL      string            `json:"pr_url,omitempty"`
	Duration   string            `json:"duration"`
	Error      string            `json:"error,omitempty"`
	Assertions []AssertionResult `json:"assertions"`
}

// AssertionResult is the outcome of one assertion check.
type AssertionResult struct {
	Type      string  `json:"type"`
	Passed    bool    `json:"passed"`
	Weight    float64 `json:"weight"`
	Threshold float64 `json:"threshold,omitempty"`
	Observed  float64 `json:"observed,omitempty"` // for diff_similarity
	Detail    string  `json:"detail,omitempty"`
}

// Report is the top-level result of a Run call.
type Report struct {
	SentinelVersion string           `json:"sentinel_version"`
	StartedAt       time.Time        `json:"started_at"`
	FinishedAt      time.Time        `json:"finished_at"`
	Scenarios       []ScenarioResult `json:"scenarios"`
	Metrics         Metrics          `json:"metrics"`
}

// Metrics is the four headline numbers.
type Metrics struct {
	Total int     `json:"total"`
	DBR   float64 `json:"dbr"` // Detection-to-Branch Rate
	VPR   float64 `json:"vpr"` // Verified-Pass Rate
	TAR   float64 `json:"tar"` // Truth-Aligned Rate
	FMR   float64 `json:"fmr"` // Full-Merge Rate
}

// Run executes the configured scenarios and writes a report to resultsDir.
// Returns the report even if individual scenarios errored — the report's
// per-scenario Status will be "error" with the error message in Error.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (*Report, error) {
	if r.PollInterval <= 0 {
		r.PollInterval = 5 * time.Second
	}

	scenarios, err := LoadAllScenarios(r.ScenariosDir)
	if err != nil {
		return nil, fmt.Errorf("load scenarios: %w", err)
	}
	scenarios = filterScenarios(scenarios, opts.ScenarioIDs)
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios matched (filter: %v)", opts.ScenarioIDs)
	}

	version := opts.SentinelVersion
	if version == "" {
		version = r.probeVersion(ctx)
	}

	report := &Report{
		SentinelVersion: version,
		StartedAt:       time.Now().UTC(),
	}

	var dbr, vpr, tar, fmr int
	for _, s := range scenarios {
		res := r.runOne(ctx, s, opts)
		report.Scenarios = append(report.Scenarios, res)
		if res.Status == "error" {
			// Errors don't count against any metric — they're infrastructure
			// failures (cluster didn't come up, Sentinel didn't respond),
			// not agent failures. They DO count against the denominator
			// of "total scenarios", so the metric is still penalized.
		} else {
			if assertionPassed(res, "pr_opened") {
				dbr++
			}
			if assertionPassed(res, "pr_ci_green") {
				vpr++
			}
			if assertionPassed(res, "diff_similarity") {
				tar++
			}
			if assertionPassed(res, "sentinel_automerge_approved") {
				fmr++
			}
		}
	}

	total := len(scenarios)
	report.Metrics = Metrics{
		Total: total,
		DBR:   ratio(dbr, total),
		VPR:   ratio(vpr, total),
		TAR:   ratio(tar, total),
		FMR:   ratio(fmr, total),
	}
	report.FinishedAt = time.Now().UTC()

	if err := r.writeReport(report, version); err != nil {
		return report, fmt.Errorf("write report: %w", err)
	}
	return report, nil
}

// runOne runs a single scenario end-to-end. Always returns a ScenarioResult
// (with Status="error" + Error set on infrastructure failure); never panics.
func (r *Runner) runOne(ctx context.Context, s *Scenario, opts RunOptions) ScenarioResult {
	start := time.Now().UTC()
	res := ScenarioResult{
		ScenarioID: s.ID,
		Slug:       s.Slug,
		Difficulty: s.Difficulty,
	}
	defer func() {
		res.Duration = time.Since(start).Round(time.Second).String()
	}()

	if err := s.Validate(); err != nil {
		res.Status = "error"
		res.Error = fmt.Sprintf("validate: %v", err)
		return res
	}

	// Cluster lifecycle. If opts.ClusterName is set we use it as-is and
	// never delete it; otherwise we create an ephemeral cluster per
	// scenario and delete it on the way out.
	clusterName := opts.ClusterName
	createdCluster := false
	if clusterName == "" && r.Provisioner != nil {
		clusterName = fmt.Sprintf("sentinel-bench-%s-%d", s.ID, start.Unix())
		if err := r.Provisioner.Create(ctx, clusterName); err != nil {
			res.Status = "error"
			res.Error = fmt.Sprintf("create cluster: %v", err)
			return res
		}
		createdCluster = true
	}
	if createdCluster && !opts.KeepCluster && r.Provisioner != nil {
		defer func() {
			// Use a fresh context — the parent may be canceled.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := r.Provisioner.Delete(cleanupCtx, clusterName); err != nil {
				logging.Warn("sentinel-bench: cleanup cluster failed", "cluster", clusterName, "err", err)
			}
		}()
	}

	// Apply cluster manifests.
	if r.Applier != nil {
		manifests, err := s.ClusterManifestPaths()
		if err != nil {
			res.Status = "error"
			res.Error = fmt.Sprintf("resolve manifests: %v", err)
			return res
		}
		if err := r.Applier.Apply(ctx, manifests); err != nil {
			res.Status = "error"
			res.Error = fmt.Sprintf("apply manifests: %v", err)
			return res
		}
	}

	// Trigger Sentinel.
	trig, err := r.Sentinel.Trigger(ctx, s.ServiceName(), s.Namespace(), s.Reason())
	if err != nil {
		res.Status = "error"
		res.Error = fmt.Sprintf("trigger: %v", err)
		return res
	}
	res.IncidentID = trig.ClusterID

	// Poll until terminal or timeout.
	budget := opts.Timeout
	if budget <= 0 {
		budget = s.TimeBudget.D()
	}
	incident, pollErr := r.pollIncident(ctx, trig.ClusterID, budget)
	if pollErr != nil {
		res.Status = "error"
		res.Error = fmt.Sprintf("poll: %v", pollErr)
		return res
	}
	res.PRURL = incident.PRURL

	// Fetch the PR diff (if a PR was opened).
	var prDiff string
	if incident.HasPR() && r.DiffFetcher != nil {
		diff, err := r.DiffFetcher.Fetch(ctx, incident.PRRepo, *incident.PRNumber)
		if err != nil {
			logging.Warn("sentinel-bench: fetch PR diff failed", "scenario", s.ID, "err", err)
		} else {
			prDiff = diff
		}
	}

	// Evaluate assertions.
	gtDiff, err := s.GroundTruth()
	if err != nil {
		res.Status = "error"
		res.Error = fmt.Sprintf("read ground truth: %v", err)
		return res
	}
	res.Assertions = evaluateAssertions(s.Assertions, incident, prDiff, gtDiff)
	res.Score = scenarioScore(res.Assertions)
	res.Status = "pass"
	if res.Score < 1.0 {
		res.Status = "fail"
	}
	return res
}

// pollIncident polls GET /incidents/{id} every PollInterval until the
// incident reaches a terminal state or the budget elapses. Returns the
// final incident (or the last-seen one on timeout).
func (r *Runner) pollIncident(ctx context.Context, id string, budget time.Duration) (Incident, error) {
	deadline := time.Now().UTC().Add(budget)
	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()

	var last Incident
	for {
		inc, err := r.Sentinel.GetIncident(ctx, id)
		if err == nil {
			last = *inc
			if inc.IsTerminal() {
				return *inc, nil
			}
		}
		if time.Now().UTC().After(deadline) {
			if last.IncidentID == "" {
				return Incident{}, fmt.Errorf("timeout after %s (incident %s never observed)", budget, id)
			}
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}

// probeVersion fetches the Sentinel version from GET /healthz. Falls back
// to "unknown" if the call fails (the report still gets written).
func (r *Runner) probeVersion(ctx context.Context) string {
	body, err := r.Sentinel.do(ctx, "GET", "/healthz", nil, false)
	if err != nil {
		return "unknown"
	}
	var resp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "unknown"
	}
	if resp.Version == "" {
		return "unknown"
	}
	return resp.Version
}

// writeReport writes results.json + summary.md under
// resultsDir/<version>/<date>/.
func (r *Runner) writeReport(report *Report, version string) error {
	date := report.StartedAt.Format("2006-01-02")
	dir := filepath.Join(r.ResultsDir, version, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	jsonPath := filepath.Join(dir, "results.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}
	mdPath := filepath.Join(dir, "summary.md")
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", mdPath, err)
	}
	return nil
}

// renderMarkdown returns a human-readable summary of the report.
func renderMarkdown(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sentinel-Bench — %s\n\n", r.SentinelVersion)
	fmt.Fprintf(&b, "Started: %s\nFinished: %s\n\n", r.StartedAt.Format(time.RFC3339), r.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "## Metrics\n\n")
	fmt.Fprintf(&b, "| Total | DBR | VPR | TAR | FMR |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %d | %.0f%% | %.0f%% | %.0f%% | %.0f%% |\n\n",
		r.Metrics.Total,
		r.Metrics.DBR*100, r.Metrics.VPR*100, r.Metrics.TAR*100, r.Metrics.FMR*100)
	fmt.Fprintf(&b, "## Scenarios\n\n")
	fmt.Fprintf(&b, "| ID | Slug | Difficulty | Status | Score | PR |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
	for _, s := range r.Scenarios {
		pr := "—"
		if s.PRURL != "" {
			pr = "[PR](" + s.PRURL + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.0f%% | %s |\n",
			s.ScenarioID, s.Slug, s.Difficulty, s.Status, s.Score*100, pr)
	}
	fmt.Fprintf(&b, "\n")
	for _, s := range r.Scenarios {
		fmt.Fprintf(&b, "### %s — %s\n\n", s.ScenarioID, s.Slug)
		fmt.Fprintf(&b, "- Status: %s\n", s.Status)
		fmt.Fprintf(&b, "- Score: %.0f%%\n", s.Score*100)
		fmt.Fprintf(&b, "- Duration: %s\n", s.Duration)
		if s.Error != "" {
			fmt.Fprintf(&b, "- Error: %s\n", s.Error)
		}
		if len(s.Assertions) > 0 {
			fmt.Fprintf(&b, "- Assertions:\n")
			for _, a := range s.Assertions {
				pass := "FAIL"
				if a.Passed {
					pass = "PASS"
				}
				fmt.Fprintf(&b, "  - %s: %s", a.Type, pass)
				if a.Type == "diff_similarity" {
					fmt.Fprintf(&b, " (observed=%.2f, threshold=%.2f)", a.Observed, a.Threshold)
				}
				fmt.Fprintf(&b, "\n")
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

// evaluateAssertions checks every assertion in the scenario's assertions
// list against the incident + diffs. Returns one AssertionResult per
// assertion, in the same order as the input.
func evaluateAssertions(assertions []Assertion, inc Incident, prDiff, gtDiff string) []AssertionResult {
	out := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		res := AssertionResult{
			Type:      a.Type,
			Weight:    a.Weight,
			Threshold: a.Threshold,
		}
		switch a.Type {
		case "pr_opened":
			res.Passed = inc.HasPR()
			if !res.Passed {
				res.Detail = "no PR opened"
			}
		case "pr_ci_green":
			res.Passed = inc.CIGreen()
			if !res.Passed {
				res.Detail = "CI not green (sandbox not verified)"
			}
		case "diff_similarity":
			res.Observed = DiffSimilarity(prDiff, gtDiff)
			res.Passed = res.Observed >= a.Threshold
			if !res.Passed {
				res.Detail = fmt.Sprintf("line-recall %.2f < threshold %.2f", res.Observed, a.Threshold)
			}
		case "sentinel_automerge_approved":
			res.Passed = inc.AutoMergeApproved()
			if !res.Passed {
				res.Detail = fmt.Sprintf("auto_merge_status=%q", inc.AutoMergeStatus)
			}
		default:
			res.Detail = fmt.Sprintf("unknown assertion type %q", a.Type)
		}
		out = append(out, res)
	}
	return out
}

// scenarioScore returns the weighted sum of assertion results (0.0 to 1.0).
func scenarioScore(assertions []AssertionResult) float64 {
	var total, passed float64
	for _, a := range assertions {
		total += a.Weight
		if a.Passed {
			passed += a.Weight
		}
	}
	if total == 0 {
		return 0
	}
	return passed / total
}

// assertionPassed reports whether the named assertion passed in the result.
func assertionPassed(res ScenarioResult, typ string) bool {
	for _, a := range res.Assertions {
		if a.Type == typ {
			return a.Passed
		}
	}
	return false
}

// ratio returns num/denom as a float64, or 0 if denom is 0.
func ratio(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom)
}

// filterScenarios returns the scenarios whose IDs are in ids, or all
// scenarios if ids is empty.
func filterScenarios(scenarios []*Scenario, ids []string) []*Scenario {
	if len(ids) == 0 {
		return scenarios
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make([]*Scenario, 0, len(scenarios))
	for _, s := range scenarios {
		if wanted[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// --- default provisioner / applier / diff fetcher implementations ---

// kindProvisioner shells out to `kind` to create and delete clusters.
type kindProvisioner struct{}

// Create runs `kind create cluster --name <name>`.
func (kindProvisioner) Create(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "kind", "create", "cluster", "--name", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kind create cluster: %w: %s", err, string(out))
	}
	return nil
}

// Delete runs `kind delete cluster --name <name>`.
func (kindProvisioner) Delete(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kind delete cluster: %w: %s", err, string(out))
	}
	return nil
}

// kubectlApplier shells out to `kubectl apply -f`.
type kubectlApplier struct{}

// Apply runs `kubectl apply -f <manifest>...`.
func (kubectlApplier) Apply(ctx context.Context, manifests []string) error {
	args := append([]string{"apply", "-f"}, manifests...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply: %w: %s", err, string(out))
	}
	return nil
}

// githubDiffFetcher fetches a PR diff from the GitHub REST API. repo is
// "owner/name" (e.g. "sentinel-bench/sample-python-app"); the GitHub API
// returns the unified diff when Accept: application/vnd.github.v3.diff
// is set.
type githubDiffFetcher struct {
	token  string
	client *http.Client
}

// Fetch returns the unified diff for PR #prNumber in repo.
func (g githubDiffFetcher) Fetch(ctx context.Context, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build github request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	client := g.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("github api returned %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read github response: %w", err)
	}
	return string(body), nil
}

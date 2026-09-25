package automerge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	githubapp "github.com/karimzakzouk/sentinel/internal/github"
)

// GitHubClientImpl is the default GitHubClient implementation. It embeds
// github.BaseClient for PostComment / GetLabels / Merge / SetLabels /
// RemoveLabel / RevertMergeCommit and adds GetCIStatus, which aggregates
// check_runs + commit statuses for a PR head SHA.
type GitHubClientImpl struct {
	*githubapp.BaseClient
}

// TokenProvider returns a fresh GitHub installation token. Implemented by
// *github.App.
type TokenProvider = githubapp.TokenProvider

// NewGitHubClient creates a GitHubClientImpl with the given PAT and API URL.
func NewGitHubClient(token, apiURL string) *GitHubClientImpl {
	return &GitHubClientImpl{
		BaseClient: githubapp.NewBaseClient(token, apiURL),
	}
}

// WithTokenProvider wires a GitHub App token provider so all API calls use
// the installation token (appearing as the verified Sentinel bot).
func (g *GitHubClientImpl) WithTokenProvider(tp TokenProvider) *GitHubClientImpl {
	g.BaseClient.WithTokenProvider(tp)
	return g
}

// IsPROpen reports whether the PR exists and is in the "open" state. Used by
// restart reconciliation and the poll loop to detect PRs that were merged or
// closed by a human while Sentinel wasn't watching.
func (g *GitHubClientImpl) IsPROpen(ctx context.Context, repo string, prNumber int) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/pulls/%d", g.APIURL, repo, prNumber), nil)
	if err != nil {
		return false, err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return false, fmt.Errorf("is_pr_open: %d %s", resp.StatusCode, string(body))
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.State == "open", nil
}

// GetCIStatus aggregates the CI check state for a PR. Combines check_runs
// (GitHub Actions, Checks API) and statuses (older commit status API).
// State is "failure" if any check fails, "pending" if any pending,
// "success" if all pass, "neutral" if no checks found.
func (g *GitHubClientImpl) GetCIStatus(ctx context.Context, repo string, prNumber int) (CIStatus, error) {
	prSHA, err := g.getPRHeadSHA(ctx, repo, prNumber)
	if err != nil {
		return CIStatus{}, err
	}
	checks, err := g.getCheckRuns(ctx, repo, prSHA)
	if err != nil {
		return CIStatus{}, err
	}
	statuses, err := g.getStatuses(ctx, repo, prSHA)
	if err != nil {
		return CIStatus{}, err
	}

	status := CIStatus{}
	totalPass, totalFail, totalPending := 0, 0, 0

	for _, c := range checks {
		status.Checks++
		switch c.Conclusion {
		case "success":
			totalPass++
		case "failure", "cancelled", "timed_out", "action_required":
			totalFail++
		default:
			if c.Status != "completed" {
				totalPending++
			} else {
				// Neutral or skipped counts as pass.
				totalPass++
			}
		}
	}

	for _, s := range statuses {
		status.Checks++
		switch s.State {
		case "success":
			totalPass++
		case "failure", "error":
			totalFail++
		case "pending":
			totalPending++
		}
	}

	status.Passing = totalPass
	status.Failing = totalFail
	status.Pending = totalPending

	switch {
	case totalFail > 0:
		status.State = "failure"
	case totalPending > 0:
		status.State = "pending"
	case totalPass > 0:
		status.State = "success"
	default:
		status.State = "neutral"
	}

	return status, nil
}

// getPRHeadSHA fetches the PR's head commit SHA, needed to query CI status.
func (g *GitHubClientImpl) getPRHeadSHA(ctx context.Context, repo string, prNumber int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/pulls/%d", g.APIURL, repo, prNumber), nil)
	if err != nil {
		return "", err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get PR: %d", resp.StatusCode)
	}
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", err
	}
	return pr.Head.SHA, nil
}

type checkRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

func (g *GitHubClientImpl) getCheckRuns(ctx context.Context, repo, sha string) ([]checkRun, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", g.APIURL, repo, sha), nil)
	if err != nil {
		return nil, err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get check-runs: %d", resp.StatusCode)
	}
	var result struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.CheckRuns, nil
}

type commitStatus struct {
	State string `json:"state"`
}

func (g *GitHubClientImpl) getStatuses(ctx context.Context, repo, sha string) ([]commitStatus, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/commits/%s/statuses?per_page=100", g.APIURL, repo, sha), nil)
	if err != nil {
		return nil, err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get statuses: %d", resp.StatusCode)
	}
	var result []commitStatus
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// ─── CI-failure feedback support (v1.7.12) ──────────────────────────────────
// These methods power the CI-feedback loop: when CI fails on a tracked PR,
// the merger fetches WHAT failed (failing check names + failing test output)
// so the fix proposer can revise the fix against the repository's own test
// suite instead of abandoning the PR.

// CIFailureDetails is the actionable digest of a red CI run.
type CIFailureDetails struct {
	HeadSHA      string
	FailedChecks []string // check-run names with failure conclusions
	// Details holds failing-test excerpts: test names, assertion messages,
	// and surrounding log context from failed GitHub Actions jobs (or
	// check-run annotations when Actions logs are unavailable).
	Details string
}

// GetPRMergeCommitSHA returns the merge commit SHA of a merged PR (empty
// string while unmerged). Needed to revert a bad merge after deployment.
func (g *GitHubClientImpl) GetPRMergeCommitSHA(ctx context.Context, repo string, prNumber int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/pulls/%d", g.APIURL, repo, prNumber), nil)
	if err != nil {
		return "", err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get PR merge commit: %d", resp.StatusCode)
	}
	var pr struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", err
	}
	return pr.MergeCommitSHA, nil
}

// checkRunFull mirrors the check-run fields needed for failure details.
type checkRunFull struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// getCheckRunsFull lists ALL check-run fields (incl. IDs) for a SHA.
func (g *GitHubClientImpl) getCheckRunsFull(ctx context.Context, repo, sha string) ([]checkRunFull, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", g.APIURL, repo, sha), nil)
	if err != nil {
		return nil, err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get check-runs: %d", resp.StatusCode)
	}
	var result struct {
		TotalCount int            `json:"total_count"`
		CheckRuns  []checkRunFull `json:"check_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.CheckRuns, nil
}

// annotation is one check-run annotation (often carries the failing test
// name + assertion message for test frameworks that publish them).
type annotation struct {
	Message string `json:"message"`
}

// getCheckRunAnnotations fetches annotations for one check run.
func (g *GitHubClientImpl) getCheckRunAnnotations(ctx context.Context, repo string, checkRunID int64) []string {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/check-runs/%d/annotations", g.APIURL, repo, checkRunID), nil)
	if err != nil {
		return nil
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var anns []annotation
	if err := json.NewDecoder(resp.Body).Decode(&anns); err != nil {
		return nil
	}
	out := make([]string, 0, len(anns))
	for _, a := range anns {
		if a.Message != "" {
			out = append(out, a.Message)
		}
	}
	return out
}

// workflowJob is one GitHub Actions job.
type workflowJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// getFailedJobsForSHA lists failed GitHub Actions jobs whose workflow run
// targeted the given commit SHA.
func (g *GitHubClientImpl) getFailedJobsForSHA(ctx context.Context, repo, sha string) []workflowJob {
	// workflow runs for this head SHA
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/actions/runs?head_sha=%s&per_page=20", g.APIURL, repo, sha), nil)
	if err != nil {
		return nil
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil
	}
	var runs struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		resp.Body.Close()
		return nil
	}
	resp.Body.Close()

	var failed []workflowJob
	for _, run := range runs.WorkflowRuns {
		if run.Conclusion != "failure" && run.Conclusion != "cancelled" {
			continue
		}
		jobsReq, err := http.NewRequestWithContext(ctx, "GET",
			fmt.Sprintf("%s/repos/%s/actions/runs/%d/jobs?per_page=50", g.APIURL, repo, run.ID), nil)
		if err != nil {
			continue
		}
		g.SetHeaders(jobsReq)
		jobsResp, err := g.HTTP.Do(jobsReq)
		if err != nil {
			continue
		}
		var jobs struct {
			Jobs []workflowJob `json:"jobs"`
		}
		if err := json.NewDecoder(jobsResp.Body).Decode(&jobs); err != nil {
			jobsResp.Body.Close()
			continue
		}
		jobsResp.Body.Close()
		for _, j := range jobs.Jobs {
			if j.Conclusion == "failure" {
				failed = append(failed, j)
			}
		}
	}
	return failed
}

// fetchJobLogs downloads a job's plain-text logs. The endpoint 302-redirects
// to a pre-signed blob URL; Go's client follows the redirect and drops the
// Authorization header cross-host (the blob URL is already signed).
func (g *GitHubClientImpl) fetchJobLogs(ctx context.Context, repo string, jobID int64) string {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", g.APIURL, repo, jobID), nil)
	if err != nil {
		return ""
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10)) // 256KB cap
	return string(buf)
}

// ciFailureLogPatterns match the lines that matter in test-runner output:
// failing test identifiers and assertion context across ecosystems.
var ciFailureLogPatterns = []string{
	"--- FAIL:",            // go test
	"--- SKIP:",            // go test (skipped due to failure)
	"FAIL",                 // go test summary / make failures
	"FAILED",               // pytest / junit summary
	"AssertionError",       // python
	"AssertionFailedError", // java
	"ExpectationNotMet",    // misc
	"expected:",            // assertion diff
	"actual:",              // assertion diff
	"Error:",               // generic error line
	"error:",               // generic error line
	"exit status 1",        // go build/test exit
	"test failed",          // generic
	"Compilation failed",   // build errors
	"undefined:",           // go compile error
	"cannot find",          // compile error
	"No such file",         // missing file
}

// extractCIFailureExcerpt pulls the interesting lines (plus 2 lines of
// trailing context) out of a job log. Capped so the fix-proposer prompt
// stays bounded.
func extractCIFailureExcerpt(log string) string {
	if log == "" {
		return ""
	}
	lines := strings.Split(log, "\n")
	var out []string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		matched := false
		for _, p := range ciFailureLogPatterns {
			if strings.Contains(line, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		// Skip GitHub Actions timestamp noise on matched lines.
		// Take this line + up to 2 following lines for context.
		end := i + 3
		if end > len(lines) {
			end = len(lines)
		}
		for j := i; j < end; j++ {
			ctx := strings.TrimSpace(lines[j])
			if ctx != "" {
				out = append(out, ctx)
			}
		}
		if len(out) > 60 { // hard cap on extracted lines
			out = out[:60]
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

// GetCIFailureDetails assembles the failing-check digest for a PR whose CI
// is red. Best effort: any part that cannot be fetched is simply omitted.
func (g *GitHubClientImpl) GetCIFailureDetails(ctx context.Context, repo string, prNumber int) (*CIFailureDetails, error) {
	sha, err := g.getPRHeadSHA(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("get PR head: %w", err)
	}
	details := &CIFailureDetails{HeadSHA: sha}

	checks, err := g.getCheckRunsFull(ctx, repo, sha)
	if err == nil {
		for _, c := range checks {
			if c.Conclusion == "failure" || c.Conclusion == "timed_out" || c.Conclusion == "cancelled" {
				details.FailedChecks = append(details.FailedChecks, c.Name)
			}
		}
	}

	var sb strings.Builder

	// Primary source: failed Actions jobs' logs (contains the actual test
	// output — test names, assertion diffs, compile errors).
	for _, job := range g.getFailedJobsForSHA(ctx, repo, sha) {
		log := g.fetchJobLogs(ctx, repo, job.ID)
		excerpt := extractCIFailureExcerpt(log)
		if excerpt != "" {
			fmt.Fprintf(&sb, "### Failed job: %s (%s)\n```\n%s\n```\n\n", job.Name, job.HTMLURL, excerpt)
		} else {
			fmt.Fprintf(&sb, "### Failed job: %s (%s)\n(job logs unavailable)\n\n", job.Name, job.HTMLURL)
		}
	}

	// Secondary source: check-run annotations (test frameworks that publish
	// them surface failing test names + assertion messages here).
	if sb.Len() == 0 {
		for _, c := range checks {
			if c.Conclusion != "failure" {
				continue
			}
			for _, msg := range g.getCheckRunAnnotations(ctx, repo, c.ID) {
				fmt.Fprintf(&sb, "### Check %s failed\n- %s\n\n", c.Name, msg)
			}
		}
	}

	details.Details = strings.TrimSpace(sb.String())
	return details, nil
}

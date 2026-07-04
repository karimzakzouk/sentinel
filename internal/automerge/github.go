// Package automerge — GitHub client implementation.
// Wraps the GitHub REST API for the operations the merger needs:
//   - GetCIStatus (check_runs + status contexts)
//   - GetLabels (list labels on a PR)
//   - Merge (PUT /pulls/{n}/merge)
//   - PostComment (for status updates)
package automerge

import (
        "context"
        "encoding/json"
        "fmt"
        "net/http"

        githubapp "github.com/karimzakzouk/sentinel/internal/github"
)

// GitHubClientImpl is the default GitHubClient implementation.

type GitHubClientImpl struct {
        *githubapp.BaseClient
}

// TokenProvider returns a fresh GitHub API token. v4.0.0.

type TokenProvider = githubapp.TokenProvider

// NewGitHubClient creates a GitHubClientImpl.

func NewGitHubClient(token, apiURL string) *GitHubClientImpl {
        return &GitHubClientImpl{
                BaseClient: githubapp.NewBaseClient(token, apiURL),
        }
}

// WithTokenProvider wires a GitHub App token provider. v4.0.0.

func (g *GitHubClientImpl) WithTokenProvider(tp TokenProvider) *GitHubClientImpl {
        g.BaseClient.WithTokenProvider(tp)
        return g
}

// authToken returns the token to use for an API call. v4.0.0.

// GetCIStatus aggregates the CI check state for a PR. Combines:
//   - check_runs (GitHub Actions, CI systems that use Checks API)
//   - statuses (older commit status API)
// Returns a summary: state = "success" if all pass, "failure" if any fail,
// "pending" if any pending, "neutral" if no checks found.

func (g *GitHubClientImpl) GetCIStatus(ctx context.Context, repo string, prNumber int) (CIStatus, error) {
        // Get the PR's head SHA.

        prSHA, err := g.getPRHeadSHA(ctx, repo, prNumber)
        if err != nil {
                return CIStatus{}, err
        }

        // Query check_runs for the SHA.

        checks, err := g.getCheckRuns(ctx, repo, prSHA)
        if err != nil {
                return CIStatus{}, err
        }

        // Query statuses (older API) for the SHA.

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
                                totalPass++ // neutral or skipped counts as pass
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

// getPRHeadSHA fetches the PR's head commit SHA (needed to query CI status).

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
        Status      string `json:"status"`
        Conclusion  string `json:"conclusion"`
        Name        string `json:"name"`
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

type status struct {
        State string `json:"state"`
}

func (g *GitHubClientImpl) getStatuses(ctx context.Context, repo, sha string) ([]status, error) {
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
        var result []status
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
                return nil, err
        }
        return result, nil
}

// GetLabels returns the labels on a PR.

// Merge merges a PR via the GitHub API.

// PostComment posts a comment on a PR.

// SetLabels adds labels to a PR (used by /sentinel approve, /sentinel hold).

// RemoveLabel removes a label from a PR.

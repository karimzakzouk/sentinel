// Package automerge — GitHub client implementation.
//
// Wraps the GitHub REST API for the operations the merger needs:
//   - GetCIStatus (check_runs + status contexts)
//   - GetLabels (list labels on a PR)
//   - Merge (PUT /pulls/{n}/merge)
//   - PostComment (for status updates)
package automerge

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "strings"
        "time"
)

// GitHubClientImpl is the default GitHubClient implementation.
type GitHubClientImpl struct {
        token  string
        apiURL string
        http   *http.Client
        // Optional GitHub App token provider. When set, all API calls
        // (CI status, labels, merge, comments) use the App's installation token,
        // making them appear as the "Sentinel" bot identity.
        tokenProvider TokenProvider
}

// TokenProvider returns a fresh GitHub API token. v4.0.0.
type TokenProvider interface {
        GetInstallToken(ctx context.Context) (string, error)
}

// NewGitHubClient creates a GitHubClientImpl.
func NewGitHubClient(token, apiURL string) *GitHubClientImpl {
        if apiURL == "" {
                apiURL = "https://api.github.com"
        }
        apiURL = strings.TrimRight(apiURL, "/")
        return &GitHubClientImpl{
                token:  token,
                apiURL: apiURL,
                http:   &http.Client{Timeout: 30 * time.Second},
        }
}

// WithTokenProvider wires a GitHub App token provider. v4.0.0.
func (g *GitHubClientImpl) WithTokenProvider(tp TokenProvider) *GitHubClientImpl {
        g.tokenProvider = tp
        return g
}

// authToken returns the token to use for an API call. v4.0.0.
func (g *GitHubClientImpl) authToken(ctx context.Context) string {
        if g.tokenProvider != nil {
                if token, err := g.tokenProvider.GetInstallToken(ctx); err == nil && token != "" {
                        return token
                }
        }
        return g.token
}

// GetCIStatus aggregates the CI check state for a PR. Combines:
//   - check_runs (GitHub Actions, CI systems that use Checks API)
//   - statuses (older commit status API)
//
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
                fmt.Sprintf("%s/repos/%s/pulls/%d", g.apiURL, repo, prNumber), nil)
        if err != nil {
                return "", err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
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
                fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", g.apiURL, repo, sha), nil)
        if err != nil {
                return nil, err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
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
                fmt.Sprintf("%s/repos/%s/commits/%s/statuses?per_page=100", g.apiURL, repo, sha), nil)
        if err != nil {
                return nil, err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
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
func (g *GitHubClientImpl) GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels?per_page=100", g.apiURL, repo, prNumber), nil)
        if err != nil {
                return nil, err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                return nil, fmt.Errorf("get labels: %d", resp.StatusCode)
        }
        var labels []struct {
                Name string `json:"name"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&labels); err != nil {
                return nil, err
        }
        out := make([]string, 0, len(labels))
        for _, l := range labels {
                out = append(out, l.Name)
        }
        return out, nil
}

// Merge merges a PR via the GitHub API.
func (g *GitHubClientImpl) Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error {
        body, _ := json.Marshal(map[string]string{
                "commit_message": commitMessage,
                "merge_method":   "squash", // squash is the safest default — single commit on main
        })
        req, err := http.NewRequestWithContext(ctx, "PUT",
                fmt.Sprintf("%s/repos/%s/pulls/%d/merge", g.apiURL, repo, prNumber),
                bytes.NewReader(body))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("merge: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

// PostComment posts a comment on a PR.
func (g *GitHubClientImpl) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
        payload, _ := json.Marshal(map[string]string{"body": body})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/comments", g.apiURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("post comment: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

// SetLabels adds labels to a PR (used by /sentinel approve, /sentinel hold).
func (g *GitHubClientImpl) SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
        payload, _ := json.Marshal(map[string][]string{"labels": labels})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels", g.apiURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("set labels: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

// RemoveLabel removes a label from a PR.
func (g *GitHubClientImpl) RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error {
        req, err := http.NewRequestWithContext(ctx, "DELETE",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", g.apiURL, repo, prNumber, label), nil)
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        // 200 = removed, 404 = label wasn't there (treat as success)
        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("remove label: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

func (g *GitHubClientImpl) setHeaders(req *http.Request) {
        ctx := req.Context()
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("Authorization", "Bearer "+g.authToken(ctx)) // v4.0.0: GitHub App token when configured
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")
}

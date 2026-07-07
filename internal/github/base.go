// Package githubapp — shared base client for GitHub API operations.
// BaseClient provides the common auth + HTTP + retry logic used by all
// GitHub clients in Sentinel:
//   - internal/chatops/github.go (comments, reactions, labels)
//   - internal/automerge/github.go (CI status, merge)
//   - internal/executors/github.go (branch, file, PR)
// Each client embeds BaseClient and adds its domain-specific methods.
package githubapp

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "strings"
        "time"
)

// TokenProvider returns a fresh GitHub installation token for GitHub App auth.
// When set, all API calls use the installation token instead of the PAT.
type TokenProvider interface {
        GetInstallToken(ctx context.Context) (string, error)
}

// BaseClient handles auth + HTTP + retry for GitHub API calls.
// Embed it in domain-specific clients (chatops, automerge, executors).
type BaseClient struct {
        Token         string
        APIURL        string
        HTTP          *http.Client
        TokenProvider TokenProvider
}

// NewBaseClient creates a BaseClient with sensible defaults.
func NewBaseClient(token, apiURL string) *BaseClient {
        if apiURL == "" {
                apiURL = "https://api.github.com"
        }
        return &BaseClient{
                Token:  token,
                APIURL: apiURL,
                HTTP:   &http.Client{Timeout: 30 * time.Second},
        }
}

// WithTokenProvider sets a GitHub App token provider for installation-token auth.
func (b *BaseClient) WithTokenProvider(tp TokenProvider) {
        b.TokenProvider = tp
}

// AuthToken returns the current auth token — installation token if a
// TokenProvider is set, otherwise the PAT.
func (b *BaseClient) AuthToken(ctx context.Context) string {
        if b.TokenProvider != nil {
                if token, err := b.TokenProvider.GetInstallToken(ctx); err == nil && token != "" {
                        return token
                }
        }
        return b.Token
}

func (b *BaseClient) SetHeaders(req *http.Request) {
        ctx := req.Context()
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("Authorization", "Bearer "+b.AuthToken(ctx))
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")
}

// Do executes an authenticated GitHub API request with retry + rate-limit handling.
// `out` is populated from the response body (nil for no response parsing).
// Retries up to 3 times on 403/429 with exponential backoff.
func (b *BaseClient) Do(ctx context.Context, method, url string, body any, out any) error {
        const maxRetries = 3
        var lastErr error

        for attempt := 0; attempt <= maxRetries; attempt++ {
                if attempt > 0 {
                        backoff := time.Duration(1<<uint(attempt-1)) * time.Second
                        backoff += time.Duration(time.Now().UnixNano()%500) * time.Millisecond
                        select {
                        case <-ctx.Done():
                                return ctx.Err()
                        case <-time.After(backoff):
                        }
                }

                var bodyReader io.Reader
                if body != nil {
                        bBytes, err := json.Marshal(body)
                        if err != nil {
                                return err
                        }
                        bodyReader = bytes.NewReader(bBytes)
                }

                req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
                if err != nil {
                        return err
                }
                req.Header.Set("Authorization", "Bearer "+b.AuthToken(ctx))
                req.Header.Set("Accept", "application/vnd.github+json")
                req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
                if body != nil {
                        req.Header.Set("Content-Type", "application/json")
                }

                resp, err := b.HTTP.Do(req)
                if err != nil {
                        lastErr = err
                        continue
                }

                // Rate limit / secondary rate limit
                if resp.StatusCode == 403 || resp.StatusCode == 429 {
                        body, _ := io.ReadAll(resp.Body)
                        resp.Body.Close()
                        if isRateLimited(resp, body) {
                                logging.Info("rate limited", "value", attempt+1, maxRetries+1, string(body[:min(len(body), 200)]))
                                lastErr = fmt.Errorf("rate limited: %s", string(body[:min(len(body), 200)]))
                                continue
                        }
                        // Non-rate-limit 403 = auth error, don't retry
                        resp.Body = io.NopCloser(bytes.NewReader(body))
                }

                defer resp.Body.Close()

                if resp.StatusCode >= 400 {
                        respBody, _ := io.ReadAll(resp.Body)
                        return fmt.Errorf("github API %s %s: %d %s", method, url, resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
                }

                if out != nil && resp.StatusCode != 204 {
                        return json.NewDecoder(resp.Body).Decode(out)
                }
                return nil
        }

        return fmt.Errorf("github API %s %s: max retries exceeded: %w", method, url, lastErr)
}

// isRateLimited checks if a 403/429 response is due to rate limiting.
func isRateLimited(resp *http.Response, body []byte) bool {
        remaining := resp.Header.Get("X-RateLimit-Remaining")
        if remaining == "0" {
                return true
        }
        bodyStr := string(body)
        return strings.Contains(bodyStr, "rate limit") || strings.Contains(bodyStr, "secondary rate limit")
}

// PostComment posts a comment on a PR.
func (b *BaseClient) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
        payload, _ := json.Marshal(map[string]string{"body": body})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/comments", b.APIURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
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

// SetLabels adds labels to a PR.
func (b *BaseClient) SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
        payload, _ := json.Marshal(map[string][]string{"labels": labels})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels", b.APIURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
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
func (b *BaseClient) RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error {
        req, err := http.NewRequestWithContext(ctx, "DELETE",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", b.APIURL, repo, prNumber, label), nil)
        if err != nil {
                return err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("remove label: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

// GetLabels returns the labels on a PR.
func (b *BaseClient) GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels", b.APIURL, repo, prNumber), nil)
        if err != nil {
                return nil, err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
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
        out := make([]string, len(labels))
        for i, l := range labels {
                out[i] = l.Name
        }
        return out, nil
}

// Merge merges a PR via the GitHub API (squash).
func (b *BaseClient) Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error {
        payload, _ := json.Marshal(map[string]string{
                "commit_message": commitMessage,
                "merge_method":   "squash",
        })
        req, err := http.NewRequestWithContext(ctx, "PUT",
                fmt.Sprintf("%s/repos/%s/pulls/%d/merge", b.APIURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
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

// RevertMergeCommit creates a revert PR for the given merge commit SHA.
// It uses the GitHub "create a pull request" API after creating a revert
// branch via the refs API. The revert branch is named
// "sentinel/revert/<short-sha>".
//
// Flow:
//   1. GET /repos/{repo}/git/commits/{sha}  — verify the commit exists + get tree
//   2. POST /repos/{repo}/git/refs          — create branch "sentinel/revert/<short-sha>" pointing at the commit's parent
//   3. POST /repos/{repo}/pulls             — open a PR from the revert branch to baseBranch
//
// Note: this creates a branch pointing at the PARENT of the merge commit,
// effectively reverting the merge. For squash merges the parent is the
// previous main HEAD. For merge commits the parent[0] is the previous main.
//
// The actual revert diff is produced by the PR diff — operators review it
// before merging, exactly like a manual revert.
func (b *BaseClient) RevertMergeCommit(ctx context.Context, repo, mergeCommitSHA, baseBranch, reason string) (int, error) {
        shortSHA := mergeCommitSHA
        if len(shortSHA) > 8 {
                shortSHA = shortSHA[:8]
        }
        branchName := fmt.Sprintf("sentinel/revert/%s", shortSHA)

        // 1. Get the commit to find its parent.
        var commit struct {
                SHA     string   `json:"sha"`
                Parents []struct {
                        SHA string `json:"sha"`
                } `json:"parents"`
        }
        if err := b.Do(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/git/commits/%s", b.APIURL, repo, mergeCommitSHA), nil, &commit); err != nil {
                return 0, fmt.Errorf("get commit: %w", err)
        }
        if len(commit.Parents) == 0 {
                return 0, fmt.Errorf("commit %s has no parent — cannot revert (is this the initial commit?)", mergeCommitSHA)
        }
        parentSHA := commit.Parents[0].SHA

        // 2. Create a branch at the parent.
        refPayload, _ := json.Marshal(map[string]string{
                "ref": fmt.Sprintf("refs/heads/%s", branchName),
                "sha": parentSHA,
        })
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/git/refs", b.APIURL, repo),
                bytes.NewReader(refPayload))
        if err != nil {
                return 0, err
        }
        b.SetHeaders(req)
        resp, err := b.HTTP.Do(req)
        if err != nil {
                return 0, err
        }
        // 422 = branch already exists (idempotent — fine for retries).
        if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusUnprocessableEntity {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                resp.Body.Close()
                return 0, fmt.Errorf("create revert branch: %d %s", resp.StatusCode, string(body))
        }
        resp.Body.Close()

        // 3. Open a PR from the revert branch to baseBranch.
        prBody := fmt.Sprintf("Sentinel auto-revert of merge commit %s.\n\n**Reason:** %s\n\nThis PR was opened automatically by Sentinel's post-merge regression watcher. Do not re-merge until the root cause is identified.",
                mergeCommitSHA, reason)
        prPayload, _ := json.Marshal(map[string]string{
                "title": fmt.Sprintf("revert: %s (Sentinel auto-revert)", shortSHA),
                "body":  prBody,
                "head":  branchName,
                "base":  baseBranch,
        })
        req, err = http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/pulls", b.APIURL, repo),
                bytes.NewReader(prPayload))
        if err != nil {
                return 0, err
        }
        b.SetHeaders(req)
        resp, err = b.HTTP.Do(req)
        if err != nil {
                return 0, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return 0, fmt.Errorf("create revert PR: %d %s", resp.StatusCode, string(body))
        }
        var pr struct {
                Number int `json:"number"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
                return 0, fmt.Errorf("decode PR response: %w", err)
        }
        return pr.Number, nil
}

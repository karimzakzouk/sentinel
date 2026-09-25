package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// TokenProvider returns a fresh GitHub installation token. Implementations
// are typically *App.
type TokenProvider interface {
	GetInstallToken(ctx context.Context) (string, error)
}

// BaseClient handles auth + HTTP + retry for GitHub API calls. Domain-specific
// clients embed it and add their methods.
type BaseClient struct {
	Token         string
	APIURL        string
	HTTP          *http.Client
	TokenProvider TokenProvider
}

// NewBaseClient creates a BaseClient with a 30-second HTTP timeout.
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

// AuthToken returns the current auth token: installation token if a
// TokenProvider is set, otherwise the PAT.
func (b *BaseClient) AuthToken(ctx context.Context) string {
	if b.TokenProvider != nil {
		if token, err := b.TokenProvider.GetInstallToken(ctx); err == nil && token != "" {
			return token
		}
	}
	return b.Token
}

// SetHeaders sets the standard GitHub API headers on req.
func (b *BaseClient) SetHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+b.AuthToken(req.Context()))
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
}

// Do executes an authenticated GitHub API request with retry + rate-limit
// handling. out is populated from the response body (pass nil to ignore
// the body). Retries up to 3 times on rate-limit (403 with X-RateLimit-Remaining: 0
// or "rate limit" in body, or 429) with exponential backoff.
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
				return fmt.Errorf("marshal body: %w", err)
			}
			bodyReader = bytes.NewReader(bBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
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

		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if isRateLimited(resp, respBody) {
				logging.Info("github rate limited", "attempt", attempt+1, "max", maxRetries+1,
					"body", string(respBody[:min(len(respBody), 200)]))
				lastErr = fmt.Errorf("rate limited: %s", string(respBody[:min(len(respBody), 200)]))
				continue
			}
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
		}

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("github API %s %s: %d %s", method, url, resp.StatusCode, string(respBody))
		}

		if out != nil && resp.StatusCode != http.StatusNoContent {
			err := json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			return err
		}
		resp.Body.Close()
		return nil
	}

	return fmt.Errorf("github API %s %s: max retries exceeded: %w", method, url, lastErr)
}

// isRateLimited reports whether a 403/429 response is due to rate limiting.
func isRateLimited(resp *http.Response, body []byte) bool {
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	s := string(body)
	return strings.Contains(s, "rate limit") || strings.Contains(s, "secondary rate limit")
}

// PostComment posts a comment on a PR (issues/{n}/comments endpoint).
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("post comment: %d %s", resp.StatusCode, string(body))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("set labels: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// RemoveLabel removes a label from a PR. 404 is treated as success (label
// already gone).
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("remove label: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetLabels returns the label names on a PR.
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

// Merge squash-merges a PR.
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("merge: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// RevertMergeCommit opens a revert PR for mergeCommitSHA. It creates a new
// commit on a revert branch that has the current HEAD as parent but the merge
// commit's parent tree — effectively undoing only the merge commit's changes.
func (b *BaseClient) RevertMergeCommit(ctx context.Context, repo, mergeCommitSHA, baseBranch, reason string) (int, error) {
	shortSHA := mergeCommitSHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	branchName := fmt.Sprintf("sentinel/revert/%s", shortSHA)

	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA  string `json:"sha"`
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"parents"`
	}
	if err := b.Do(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/commits/%s", b.APIURL, repo, mergeCommitSHA), nil, &commit); err != nil {
		return 0, fmt.Errorf("get merge commit: %w", err)
	}
	if len(commit.Parents) == 0 {
		return 0, fmt.Errorf("commit %s has no parent — cannot revert", mergeCommitSHA)
	}
	parentTreeSHA := commit.Parents[0].Tree.SHA

	var baseRef struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := b.Do(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/git/refs/heads/%s", b.APIURL, repo, baseBranch), nil, &baseRef); err != nil {
		return 0, fmt.Errorf("get base branch ref: %w", err)
	}
	headSHA := baseRef.Object.SHA

	newCommitPayload, _ := json.Marshal(map[string]string{
		"message": fmt.Sprintf("revert: %s\n\nSentinel auto-revert of merge commit %s.\nReason: %s", shortSHA, mergeCommitSHA, reason),
		"tree":    parentTreeSHA,
		"parents": fmt.Sprintf("[%q]", headSHA),
	})
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := b.Do(ctx, "POST",
		fmt.Sprintf("%s/repos/%s/git/commits", b.APIURL, repo),
		json.RawMessage(newCommitPayload), &newCommit); err != nil {
		return 0, fmt.Errorf("create revert commit: %w", err)
	}

	refPayload, _ := json.Marshal(map[string]string{
		"ref": fmt.Sprintf("refs/heads/%s", branchName),
		"sha": newCommit.SHA,
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
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return 0, fmt.Errorf("create revert branch: %d %s", resp.StatusCode, string(body))
	}
	resp.Body.Close()

	prBody := fmt.Sprintf("Sentinel auto-revert of merge commit %s.\n\n**Reason:** %s\n\n"+
		"This PR reverts the changes introduced by merge commit %s. "+
		"Review the diff before merging.", mergeCommitSHA, reason, mergeCommitSHA)
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

// Base64Decode decodes a base64-encoded string returned by the GitHub
// Contents API. GitHub base64-encodes with newlines every 76 chars —
// strip them before decoding.
func Base64Decode(s string) (string, error) {
	s = strings.ReplaceAll(s, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

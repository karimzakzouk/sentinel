// Package executors implements the execution layer.
// v3: GitHub executor (open PRs), K8s executor (apply patches), sandbox runner
// (3-level verification), Kaniko builder (build canary images).
package executors

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "bytes"
        "context"
        "encoding/base64"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "strings"
        "time"

	githubapp "github.com/karimzakzouk/sentinel/internal/github"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// GitHubExecutor opens Pull Requests via the GitHub REST API.
// cfg.GitHost + cfg.GitHubAPIURL.
// https://api.github.com in 8 sites within this file.
// TokenProvider returns a fresh GitHub API token for GitHub App auth.
// Implemented by githubapp.App — when wired, PRs + comments appear as
// the "Sentinel" bot instead of the PAT owner. v4.0.0.

type TokenProvider = githubapp.TokenProvider

type GitHubExecutor struct {
	*githubapp.BaseClient
	gitHost      string
	defaultBranch string
}

// NewGitHub creates a GitHubExecutor.

func NewGitHub(token string) *GitHubExecutor {
	return &GitHubExecutor{
		BaseClient: githubapp.NewBaseClient(token, ""),
	}
}

// WithTokenProvider wires a GitHub App token provider. v4.0.0.
// When set, all GitHub API calls use the App's installation token, making
// PRs + comments appear as the "Sentinel" bot identity. Falls back to the
// PAT on error.

func (e *GitHubExecutor) WithTokenProvider(tp TokenProvider) *GitHubExecutor {
	e.BaseClient.WithTokenProvider(tp)
	return e
}

// authToken returns the token to use for an API call. v4.0.0.
// Prefers the GitHub App installation token (verified bot identity) when
// configured, falls back to the PAT.


// WithAPIURL sets the GitHub API base URL. v3.11.0: Replaces the hardcoded
// "https://api.github.com" in 8 sites within this file.
// For GitHub Enterprise: "https://gh.mycorp.internal/api/v3".
// For GitLab (GitHub-compat API): "https://gitlab.com/api/v4".

func (e *GitHubExecutor) WithAPIURL(url string) *GitHubExecutor {
        if url != "" {
                e.APIURL = strings.TrimRight(url, "/")
        }
        return e
}

// WithGitHost sets the git host for clone URLs. v3.11.0: Replaces the
// hardcoded "github.com". For GitHub Enterprise:
// "gh.mycorp.internal". For GitLab: "gitlab.com".

func (e *GitHubExecutor) WithGitHost(host string) *GitHubExecutor {
        if host != "" {
                e.gitHost = host
        }
        return e
}

// WithDefaultBranch sets the default branch fallback. v3.11.0: Replaces the
// hardcoded "main" in PrepareBranch. For repos whose
// default branch is master/develop/trunk, set this accordingly. Per-repo
// override via sentinel.io/branch annotation takes precedence.

func (e *GitHubExecutor) WithDefaultBranch(branch string) *GitHubExecutor {
        if branch != "" {
                e.defaultBranch = branch
        }
        return e
}

// BranchInfo is the result of PrepareBranch.

type BranchInfo struct {
        Success     bool   `json:"success"`
        BranchName  string `json:"branch_name"`
        Repo        string `json:"repo"`
        BaseBranch  string `json:"base_branch"`
        FilesChanged int   `json:"files_changed"`
        Error       string `json:"error,omitempty"`
}

// PrepareBranch creates a new Git branch from the base branch and pushes the
// fix's file changes to that branch.

func (e *GitHubExecutor) PrepareBranch(ctx context.Context, inc *schemas.Incident) BranchInfo {
        fp := inc.State.FixProposal
        if fp == nil || e.Token == "" {
                return BranchInfo{Success: false, Error: "no_fix_proposal_or_no_github_token"}
        }
        if fp.Repo == nil || *fp.Repo == "" {
                return BranchInfo{Success: false, Error: "no_repo_discovered_for_service"}
        }
        repo := *fp.Repo

        // 1. Get the base branch's latest commit SHA

        baseBranch := e.defaultBranch
        if fp.BaseBranch != nil && *fp.BaseBranch != "" {
                baseBranch = *fp.BaseBranch
        }
        baseSHA, err := e.getBranchSHA(ctx, repo, baseBranch)
        if err != nil {
                return BranchInfo{Success: false, Error: fmt.Sprintf("get_base_branch: %v", err)}
        }

        // 2. Create a new branch from base

        branchName := fmt.Sprintf("sentinel/%s-%s", fp.TargetService, fp.FixID.String()[:8])
        if fp.BranchName != nil && *fp.BranchName != "" {
                branchName = *fp.BranchName
        }
        if err := e.createBranch(ctx, repo, branchName, baseSHA); err != nil {
                // 422 = branch already exists, that's fine — reuse it

                if !strings.Contains(err.Error(), "422") {
                        return BranchInfo{Success: false, Error: fmt.Sprintf("create_branch: %v", err)}
                }
        }

        // 3. Write each file to the new branch

        filesChanged := 0
        for _, fc := range fp.FilesToChange {
                if fc.Path == "" {
                        continue
                }

                // current file content, apply the blocks, then write the result.
                // This produces minimal, surgical diffs instead of rewriting the
                // entire file.

                if len(fc.Changes) > 0 {
                        newContent, err := e.applySearchReplace(ctx, repo, branchName, fc.Path, fc.Changes)
                        if err != nil {
                                return BranchInfo{Success: false, Error: fmt.Sprintf("search_replace %s: %v", fc.Path, err)}
                        }
                        if err := e.upsertFile(ctx, repo, branchName, fc.Path, newContent); err != nil {
                                return BranchInfo{Success: false, Error: fmt.Sprintf("upsert_file %s: %v", fc.Path, err)}
                        }
                        filesChanged++
                        continue
                }
                // Legacy: full file replacement via NewContent

                if fc.NewContent != "" {
                        if err := e.upsertFile(ctx, repo, branchName, fc.Path, fc.NewContent); err != nil {
                                return BranchInfo{Success: false, Error: fmt.Sprintf("upsert_file %s: %v", fc.Path, err)}
                        }
                        filesChanged++
                }
        }

        return BranchInfo{
                Success:      true,
                BranchName:   branchName,
                Repo:         repo,
                BaseBranch:   baseBranch,
                FilesChanged: filesChanged,
        }
}

// PRResult is the result of OpenPR.

type PRResult struct {
        Success          bool
        PRURL            string
        PRNumber         int
        PRBranch         string
        SandboxVerified  bool
        SandboxSummary   string
        Error            string
}

// OpenPR opens a Pull Request from the fix branch.
// Called AFTER the sandbox has verified (or failed to verify) the canary.

func (e *GitHubExecutor) OpenPR(ctx context.Context, inc *schemas.Incident, branch BranchInfo, sandboxVerified bool, sandboxSummary string) *PRResult {
        fp := inc.State.FixProposal
        if !branch.Success {
                return &PRResult{Success: false, Error: fmt.Sprintf("branch_preparation_failed: %s", branch.Error)}
        }
        if e.Token == "" {
                return &PRResult{Success: false, Error: "no_github_token"}
        }

        // PR body is pre-built by the execution phase (includes sandbox verification)

        prBody := ""
        if fp.PRBody != nil {
                prBody = *fp.PRBody
        }

        prTitle := fmt.Sprintf("[Sentinel] %s for %s", fp.FixType, fp.TargetService)
        if fp.PRTitle != nil && *fp.PRTitle != "" {
                prTitle = *fp.PRTitle
        }

        // Open the PR

        prNumber, prURL, err := e.createPR(ctx, branch.Repo, prTitle, prBody, branch.BranchName, branch.BaseBranch)
        if err != nil {
                return &PRResult{Success: false, Error: fmt.Sprintf("create_pr: %v", err)}
        }

        // Add labels

        labels := []string{"sentinel", "automated"}
        if sandboxVerified {
                labels = append(labels, "sandbox-verified")
        } else {
                labels = append(labels, "needs-review")
        }
        _ = e.addLabels(ctx, branch.Repo, prNumber, labels)

        return &PRResult{
                Success:         true,
                PRURL:           prURL,
                PRNumber:        prNumber,
                PRBranch:        branch.BranchName,
                SandboxVerified: sandboxVerified,
                SandboxSummary:  sandboxSummary,
        }
}

// --- GitHub API helpers ---


func (e *GitHubExecutor) getBranchSHA(ctx context.Context, repo, branch string) (string, error) {
        url := fmt.Sprintf("%s/repos/%s/branches/%s", e.APIURL, repo, branch)
        var resp struct {
                Commit struct {
                        SHA string `json:"sha"`
                } `json:"commit"`
        }
        if err := e.do(ctx, "GET", url, nil, &resp); err != nil {
                return "", err
        }
        return resp.Commit.SHA, nil
}

func (e *GitHubExecutor) createBranch(ctx context.Context, repo, branch, sha string) error {
        url := fmt.Sprintf("%s/repos/%s/git/refs", e.APIURL, repo)
        body := map[string]any{
                "ref": fmt.Sprintf("refs/heads/%s", branch),
                "sha": sha,
        }
        return e.do(ctx, "POST", url, body, nil)
}

func (e *GitHubExecutor) upsertFile(ctx context.Context, repo, branch, path, content string) error {
        // Try to get existing file's SHA (for update). Ignore error — file may not exist yet.

        existingSHA, _ := e.getFileSHA(ctx, repo, path, branch)

        url := fmt.Sprintf("%s/repos/%s/contents/%s", e.APIURL, repo, path)
        body := map[string]any{
                "message": fmt.Sprintf("[Sentinel] Update %s", path),
                "content": base64.StdEncoding.EncodeToString([]byte(content)),
                "branch":  branch,
        }
        if existingSHA != "" {
                body["sha"] = existingSHA
        }
        return e.do(ctx, "PUT", url, body, nil)
}

func (e *GitHubExecutor) getFileSHA(ctx context.Context, repo, path, ref string) (string, error) {
        url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.APIURL, repo, path, ref)
        var resp struct {
                SHA  string `json:"sha"`
                Type string `json:"type"`
        }
        if err := e.do(ctx, "GET", url, nil, &resp); err != nil {
                return "", err
        }
        if resp.Type == "dir" {
                return "", fmt.Errorf("path is a directory")
        }
        return resp.SHA, nil
}

// getFileContent fetches the decoded content of a file from a GitHub repo.
// search/replace blocks.
// GetFileContent fetches the decoded content of a file from a GitHub repo.
// file state from the branch so the LLM generates search/replace blocks against
// the current code, not the original.

func (e *GitHubExecutor) GetFileContent(ctx context.Context, repo, path, ref string) (string, error) {
        url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.APIURL, repo, path, ref)
        var resp struct {
                Content  string `json:"content"`
                Encoding string `json:"encoding"`
                Type     string `json:"type"`
        }
        if err := e.do(ctx, "GET", url, nil, &resp); err != nil {
                return "", err
        }
        if resp.Type == "dir" {
                return "", fmt.Errorf("path is a directory")
        }
        if resp.Encoding == "base64" {
                decoded, err := base64Decode(resp.Content)
                if err != nil {
                        return "", fmt.Errorf("decode base64: %w", err)
                }
                return decoded, nil
        }
        return resp.Content, nil
}

// applySearchReplace fetches the current file content from the repo, applies
// each search/replace block using multiple matching strategies, validates
// the result is syntactically correct, and returns the modified content.
//  1. Exact match — search text must match character-for-character
//  2. Whitespace-insensitive match — strip leading/trailing whitespace per line
//  3. Fuzzy match — Levenshtein distance >80% similarity
//  4. If ALL strategies fail — return error with context for retry loop
// Also: reject useless partial fixes. If only an import block matched but the
// actual fix block didn't, don't ship a PR with just an unused import.
// implementation, which silently returned nil in distroless (no binaries)
// AND swallowed os.CreateTemp errors. See syntax_validate.go for details.


func (e *GitHubExecutor) createPR(ctx context.Context, repo, title, body, head, base string) (int, string, error) {
        url := fmt.Sprintf("%s/repos/%s/pulls", e.APIURL, repo)
        body_ := map[string]any{
                "title": title,
                "body":  body,
                "head":  head,
                "base":  base,
        }
        var resp struct {
                Number int    `json:"number"`
                HTMLURL string `json:"html_url"`
        }
        if err := e.do(ctx, "POST", url, body_, &resp); err != nil {
                return 0, "", err
        }
        return resp.Number, resp.HTMLURL, nil
}

func (e *GitHubExecutor) addLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
        url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", e.APIURL, repo, prNumber)
        body := map[string]any{"labels": labels}
        return e.do(ctx, "POST", url, body, nil)
}

func (e *GitHubExecutor) do(ctx context.Context, method, url string, body any, out any) error {

        // investigations × ~10 GitHub calls each, the secondary rate limit triggered and
        // all subsequent GitHub operations failed permanently.

        const maxRetries = 3
        var lastErr error
        for attempt := 0; attempt <= maxRetries; attempt++ {
                if attempt > 0 {
                        // Parse Retry-After header if present, else exponential backoff with jitter.

                        backoff := time.Duration(1<<uint(attempt-1)) * time.Second // 1s, 2s, 4s
                        // Add up to 500ms jitter to avoid thundering herd

                        backoff += time.Duration(time.Now().UnixNano()%500) * time.Millisecond
                        select {
                        case <-ctx.Done():
                                return ctx.Err()
                        case <-time.After(backoff):
                        }
                }

                var bodyReader io.Reader
                if body != nil {
                        b, err := json.Marshal(body)
                        if err != nil {
                                return err
                        }
                        bodyReader = bytes.NewReader(b)
                }

                req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
                if err != nil {
                        return err
                }
                req.Header.Set("Authorization", "Bearer "+e.AuthToken(ctx))
                req.Header.Set("Accept", "application/vnd.github+json")
                req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
                if body != nil {
                        req.Header.Set("Content-Type", "application/json")
                }

                resp, err := e.HTTP.Do(req)
                if err != nil {
                        lastErr = err
                        continue // network error — retry
                }

                // Retry on rate limit

                if resp.StatusCode == 429 || (resp.StatusCode == 403 && isGitHubRateLimit(resp)) {
                        resp.Body.Close()
                        lastErr = fmt.Errorf("GitHub API %d: rate limited (attempt %d/%d)", resp.StatusCode, attempt+1, maxRetries+1)
                        logging.Info("rate limited (attempt /) — backing off", "value", attempt+1, maxRetries+1)
                        continue
                }

                if resp.StatusCode >= 400 {
                        respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
                        resp.Body.Close()
                        return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(respBody))
                }

                if out != nil {
                        respBody, err := io.ReadAll(resp.Body)
                        resp.Body.Close()
                        if err != nil {
                                return err
                        }
                        if len(respBody) > 0 {
                                if err := json.Unmarshal(respBody, out); err != nil {
                                        return fmt.Errorf("unmarshal: %w", err)
                                }
                        }
                } else {
                        resp.Body.Close()
                }
                return nil
        }
        return lastErr
}

// isGitHubRateLimit checks if a 403 response is due to rate limiting.
// GitHub sends a X-RateLimit-Remaining: 0 header on primary rate limit,
// and includes "secondary rate limit" or "abuse detection" in the body on secondary.

func isGitHubRateLimit(resp *http.Response) bool {
        if resp.Header.Get("X-RateLimit-Remaining") == "0" {
                return true
        }
        // Peek at the body to check for secondary rate limit message

        body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
        // Restore the body for the caller

        resp.Body = io.NopCloser(bytes.NewReader(body))
        bodyStr := string(body)
        return strings.Contains(bodyStr, "secondary rate limit") ||
                strings.Contains(bodyStr, "abuse detection") ||
                strings.Contains(bodyStr, "rate limit")
}

// base64Decode decodes a base64-encoded string (used for GitHub Contents API
// file content). v3.9.0.

func base64Decode(s string) (string, error) {
        // GitHub returns base64 with newlines every 76 chars — strip them

        s = strings.ReplaceAll(s, "\n", "")
        decoded, err := base64.StdEncoding.DecodeString(s)
        if err != nil {
                return "", err
        }
        return string(decoded), nil
}

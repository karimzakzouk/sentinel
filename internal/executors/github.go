// Package executors implements the execution layer.
// v3: GitHub executor (open PRs), K8s executor (apply patches), sandbox runner
// (3-level verification), Kaniko builder (build canary images).
package executors

import (
        "bytes"
        "context"
        "encoding/base64"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strings"
        "time"

        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// GitHubExecutor opens Pull Requests via the GitHub REST API.
// Replaces the Python PyGithub-based executor with direct net/http calls.
type GitHubExecutor struct {
        token string
        http  *http.Client
}

// NewGitHub creates a GitHubExecutor.
func NewGitHub(token string) *GitHubExecutor {
        return &GitHubExecutor{
                token: token,
                http: &http.Client{
                        Timeout: 30 * time.Second,
                },
        }
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
        if fp == nil || e.token == "" {
                return BranchInfo{Success: false, Error: "no_fix_proposal_or_no_github_token"}
        }
        if fp.Repo == nil || *fp.Repo == "" {
                return BranchInfo{Success: false, Error: "no_repo_discovered_for_service"}
        }
        repo := *fp.Repo

        // 1. Get the base branch's latest commit SHA
        baseBranch := "main"
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
                // v3.9.0: If the FileChange has search/replace blocks, fetch the
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
        if e.token == "" {
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
        url := fmt.Sprintf("https://api.github.com/repos/%s/branches/%s", repo, branch)
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
        url := fmt.Sprintf("https://api.github.com/repos/%s/git/refs", repo)
        body := map[string]any{
                "ref": fmt.Sprintf("refs/heads/%s", branch),
                "sha": sha,
        }
        return e.do(ctx, "POST", url, body, nil)
}

func (e *GitHubExecutor) upsertFile(ctx context.Context, repo, branch, path, content string) error {
        // Try to get existing file's SHA (for update). Ignore error — file may not exist yet.
        existingSHA, _ := e.getFileSHA(ctx, repo, path, branch)

        url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, path)
        body := map[string]any{
                "message": fmt.Sprintf("[Sentinel] Update %s", path),
                "content": base64Encode(content),
                "branch":  branch,
        }
        if existingSHA != "" {
                body["sha"] = existingSHA
        }
        return e.do(ctx, "PUT", url, body, nil)
}

func (e *GitHubExecutor) getFileSHA(ctx context.Context, repo, path, ref string) (string, error) {
        url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
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
// v3.9.0: Used by applySearchReplace to get the current file before applying
// search/replace blocks.
func (e *GitHubExecutor) getFileContent(ctx context.Context, repo, path, ref string) (string, error) {
        url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, ref)
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
// each search/replace block, and returns the modified content. If a search
// block doesn't match, that block is skipped (the file stays intact for that
// block — no corruption). If ZERO blocks match, returns an error so the
// caller can fall back to whole-file replacement or fail gracefully.
//
// v3.9.0: This is the core of the search/replace approach. It produces
// minimal, surgical diffs instead of rewriting the entire file.
func (e *GitHubExecutor) applySearchReplace(ctx context.Context, repo, branch, path string, changes []schemas.SearchReplace) (string, error) {
        // Fetch the current file content from the branch
        content, err := e.getFileContent(ctx, repo, path, branch)
        if err != nil {
                return "", fmt.Errorf("fetch file %s: %w", path, err)
        }

        applied := 0
        for i, ch := range changes {
                // Count occurrences — if the search text appears more than once, we
                // can't safely replace (ambiguous). Skip that block.
                count := strings.Count(content, ch.Search)
                if count == 0 {
                        log.Printf("[github] search/replace block %d: search text not found in %s — skipping", i, path)
                        continue
                }
                if count > 1 {
                        log.Printf("[github] search/replace block %d: search text found %d times in %s — skipping (ambiguous)", i, count, path)
                        continue
                }
                // Exactly one match — apply the replacement
                content = strings.Replace(content, ch.Search, ch.Replace, 1)
                applied++
                log.Printf("[github] search/replace block %d: applied (%d chars → %d chars)", i, len(ch.Search), len(ch.Replace))
        }

        if applied == 0 {
                return "", fmt.Errorf("no search/replace blocks matched (0/%d applied)", len(changes))
        }

        log.Printf("[github] applied %d/%d search/replace blocks to %s", applied, len(changes), path)
        return content, nil
}

func (e *GitHubExecutor) createPR(ctx context.Context, repo, title, body, head, base string) (int, string, error) {
        url := fmt.Sprintf("https://api.github.com/repos/%s/pulls", repo)
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
        url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, prNumber)
        body := map[string]any{"labels": labels}
        return e.do(ctx, "POST", url, body, nil)
}

func (e *GitHubExecutor) do(ctx context.Context, method, url string, body any, out any) error {
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
        req.Header.Set("Authorization", "Bearer "+e.token)
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }

        resp, err := e.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()

        if resp.StatusCode >= 400 {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
                return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(respBody))
        }

        if out != nil {
                respBody, err := io.ReadAll(resp.Body)
                if err != nil {
                        return err
                }
                if len(respBody) > 0 {
                        if err := json.Unmarshal(respBody, out); err != nil {
                                return fmt.Errorf("unmarshal: %w", err)
                        }
                }
        }
        return nil
}

func base64Encode(s string) string {
        const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
        bytes := []byte(s)
        var out strings.Builder
        for i := 0; i < len(bytes); i += 3 {
                b1 := bytes[i]
                var b2, b3 byte
                hasB2, hasB3 := false, false
                if i+1 < len(bytes) {
                        b2 = bytes[i+1]
                        hasB2 = true
                }
                if i+2 < len(bytes) {
                        b3 = bytes[i+2]
                        hasB3 = true
                }
                out.WriteByte(chars[b1>>2])
                out.WriteByte(chars[((b1&0x03)<<4)|(b2>>4)])
                if hasB2 {
                        out.WriteByte(chars[((b2&0x0f)<<2)|(b3>>6)])
                } else {
                        out.WriteByte('=')
                }
                if hasB3 {
                        out.WriteByte(chars[b3&0x3f])
                } else {
                        out.WriteByte('=')
                }
        }
        return out.String()
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

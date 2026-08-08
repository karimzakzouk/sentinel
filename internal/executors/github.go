package executors

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/github"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TokenProvider is the GitHub App token provider. Implemented by *github.App;
// when wired, PRs + comments appear as the "Sentinel" bot instead of the PAT
// owner.
type TokenProvider = github.TokenProvider

// GitHubExecutor opens Pull Requests via the GitHub REST API. It embeds
// github.BaseClient for auth + HTTP + retry.
type GitHubExecutor struct {
	*github.BaseClient
	gitHost       string
	defaultBranch string
}

// NewGitHub creates a GitHubExecutor with the given PAT.
func NewGitHub(token string) *GitHubExecutor {
	return &GitHubExecutor{
		BaseClient: github.NewBaseClient(token, ""),
	}
}

// WithTokenProvider wires a GitHub App token provider. When set, all GitHub
// API calls use the App's installation token, making PRs + comments appear
// as the "Sentinel" bot identity.
func (e *GitHubExecutor) WithTokenProvider(tp TokenProvider) *GitHubExecutor {
	e.WithTokenProvider(tp)
	return e
}

// WithAPIURL sets the GitHub API base URL. For GitHub Enterprise:
// "https://gh.mycorp.internal/api/v3". For GitLab (GitHub-compat API):
// "https://gitlab.com/api/v4".
func (e *GitHubExecutor) WithAPIURL(url string) *GitHubExecutor {
	if url != "" {
		e.APIURL = strings.TrimRight(url, "/")
	}
	return e
}

// WithGitHost sets the git host for clone URLs. For GitHub Enterprise:
// "gh.mycorp.internal". For GitLab: "gitlab.com".
func (e *GitHubExecutor) WithGitHost(host string) *GitHubExecutor {
	if host != "" {
		e.gitHost = host
	}
	return e
}

// WithDefaultBranch sets the default branch fallback. Per-repo override
// via the sentinel.io/branch annotation takes precedence.
func (e *GitHubExecutor) WithDefaultBranch(branch string) *GitHubExecutor {
	if branch != "" {
		e.defaultBranch = branch
	}
	return e
}

// BranchInfo is the result of PrepareBranch.
type BranchInfo struct {
	Success      bool   `json:"success"`
	BranchName   string `json:"branch_name"`
	Repo         string `json:"repo"`
	BaseBranch   string `json:"base_branch"`
	FilesChanged int    `json:"files_changed"`
	Error        string `json:"error,omitempty"`
}

// PrepareBranch creates a new Git branch from the base branch and pushes
// the fix's file changes to that branch via the GitHub Contents API.
func (e *GitHubExecutor) PrepareBranch(ctx context.Context, inc *schemas.Incident) BranchInfo {
	fp := inc.State.FixProposal
	if fp == nil || e.Token == "" {
		return BranchInfo{Success: false, Error: "no_fix_proposal_or_no_github_token"}
	}
	if fp.Repo == nil || *fp.Repo == "" {
		return BranchInfo{Success: false, Error: "no_repo_discovered_for_service"}
	}
	repo := *fp.Repo

	baseBranch := e.defaultBranch
	if fp.BaseBranch != nil && *fp.BaseBranch != "" {
		baseBranch = *fp.BaseBranch
	}
	baseSHA, err := e.getBranchSHA(ctx, repo, baseBranch)
	if err != nil {
		return BranchInfo{Success: false, Error: fmt.Sprintf("get_base_branch: %v", err)}
	}

	branchName := fmt.Sprintf("sentinel/%s-%s", fp.TargetService, fp.FixID.String()[:8])
	if fp.BranchName != nil && *fp.BranchName != "" {
		branchName = *fp.BranchName
	}
	if err := e.createBranch(ctx, repo, branchName, baseSHA); err != nil {
		if !strings.Contains(err.Error(), "422") {
			return BranchInfo{Success: false, Error: fmt.Sprintf("create_branch: %v", err)}
		}
	}

	filesChanged := 0
	for _, fc := range fp.FilesToChange {
		if fc.Path == "" {
			continue
		}

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
	Success         bool
	PRURL           string
	PRNumber        int
	PRBranch        string
	SandboxVerified bool
	SandboxSummary  string
	Error           string
}

// OpenPR opens a Pull Request from the fix branch. Called AFTER the sandbox
// has verified (or failed to verify) the canary.
func (e *GitHubExecutor) OpenPR(ctx context.Context, inc *schemas.Incident, branch BranchInfo, sandboxVerified bool, sandboxSummary string) *PRResult {
	fp := inc.State.FixProposal
	if !branch.Success {
		return &PRResult{Success: false, Error: fmt.Sprintf("branch_preparation_failed: %s", branch.Error)}
	}
	if e.Token == "" {
		return &PRResult{Success: false, Error: "no_github_token"}
	}

	prBody := ""
	if fp.PRBody != nil {
		prBody = *fp.PRBody
	}

	prTitle := fmt.Sprintf("[Sentinel] %s for %s", fp.FixType, fp.TargetService)
	if fp.PRTitle != nil && *fp.PRTitle != "" {
		prTitle = *fp.PRTitle
	}

	prNumber, prURL, err := e.createPR(ctx, branch.Repo, prTitle, prBody, branch.BranchName, branch.BaseBranch)
	if err != nil {
		return &PRResult{Success: false, Error: fmt.Sprintf("create_pr: %v", err)}
	}

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

// getBranchSHA fetches the SHA of the latest commit on branch.
func (e *GitHubExecutor) getBranchSHA(ctx context.Context, repo, branch string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/branches/%s", e.APIURL, repo, branch)
	var resp struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := e.Do(ctx, "GET", url, nil, &resp); err != nil {
		return "", fmt.Errorf("get branch %s/%s: %w", repo, branch, err)
	}
	return resp.Commit.SHA, nil
}

// createBranch creates a new Git ref pointing at sha.
func (e *GitHubExecutor) createBranch(ctx context.Context, repo, branch, sha string) error {
	url := fmt.Sprintf("%s/repos/%s/git/refs", e.APIURL, repo)
	body := map[string]any{
		"ref": fmt.Sprintf("refs/heads/%s", branch),
		"sha": sha,
	}
	return e.Do(ctx, "POST", url, body, nil)
}

// upsertFile creates or updates a file on branch via the Contents API.
func (e *GitHubExecutor) upsertFile(ctx context.Context, repo, branch, path, content string) error {
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
	return e.Do(ctx, "PUT", url, body, nil)
}

// getFileSHA fetches the SHA of a file (required for update). Returns an
// error if the path is a directory.
func (e *GitHubExecutor) getFileSHA(ctx context.Context, repo, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.APIURL, repo, path, ref)
	var resp struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := e.Do(ctx, "GET", url, nil, &resp); err != nil {
		return "", err
	}
	if resp.Type == "dir" {
		return "", fmt.Errorf("path is a directory")
	}
	return resp.SHA, nil
}

// GetFileContent fetches the decoded content of a file from a GitHub repo.
func (e *GitHubExecutor) GetFileContent(ctx context.Context, repo, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.APIURL, repo, path, ref)
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if err := e.Do(ctx, "GET", url, nil, &resp); err != nil {
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

// createPR opens a Pull Request. Returns (number, html_url, error).
func (e *GitHubExecutor) createPR(ctx context.Context, repo, title, body, head, base string) (int, string, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls", e.APIURL, repo)
	body_ := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	var resp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := e.Do(ctx, "POST", url, body_, &resp); err != nil {
		return 0, "", err
	}
	return resp.Number, resp.HTMLURL, nil
}

// addLabels adds labels to a PR (issues/{n}/labels endpoint).
func (e *GitHubExecutor) addLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", e.APIURL, repo, prNumber)
	body := map[string]any{"labels": labels}
	return e.Do(ctx, "POST", url, body, nil)
}

// base64Decode decodes a base64-encoded string returned by the GitHub
// Contents API. GitHub base64-encodes with newlines every 76 chars —
// strip them before decoding.
func base64Decode(s string) (string, error) {
	s = strings.ReplaceAll(s, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// logApply is a tiny helper used by github_searchreplace.go to keep imports
// tidy. Inlined to avoid an extra logging import in the searchreplace file.
func logApply(applied, total int, path string) {
	logging.Info("applied search/replace blocks (syntax validated)",
		"applied", applied, "total", total, "path", path)
}

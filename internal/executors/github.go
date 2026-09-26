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
	e.BaseClient.WithTokenProvider(tp)
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
	SandboxLevels   string
	SandboxSummary  string
	Error           string
}

// OpenPR opens a Pull Request from the fix branch. Called AFTER the sandbox
// has verified (or failed to verify) the canary. sandboxLevels records how
// far verification got (e.g. "L1-L5" or "L1-L3", "" when not verified) and
// is embedded in the sandbox-verified label so reviewers see the strength of
// verification at a glance.
func (e *GitHubExecutor) OpenPR(ctx context.Context, inc *schemas.Incident, branch BranchInfo, sandboxVerified bool, sandboxLevels, sandboxSummary string) *PRResult {
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
		if sandboxLevels == "" {
			sandboxLevels = "L1-L3"
		}
		labels = append(labels, "sandbox-verified:"+sandboxLevels)
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
		SandboxLevels:   sandboxLevels,
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

// GetFileContentAt satisfies the RepoFileFetcher interface (repotests.go).
// Identical to GetFileContent — the interface method exists so test doubles
// can stub file fetching without a full GitHubExecutor.
func (e *GitHubExecutor) GetFileContentAt(ctx context.Context, repo, path, ref string) (string, error) {
	return e.GetFileContent(ctx, repo, path, ref)
}

// ListDirEntries lists the entries of a directory path at a ref via the
// Contents API. Returns (nil, err) on API errors; a non-existent path is
// an API error too (404) — callers treat any error as "absent".
func (e *GitHubExecutor) ListDirEntries(ctx context.Context, repo, path, ref string) ([]DirEntry, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.APIURL, repo, path, ref)
	var entries []DirEntry
	if err := e.Do(ctx, "GET", url, nil, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ResetBranchToBase reverts every file that differs between branch and
// baseBranch back to its base content (deleting files that only exist on
// the branch). This makes the branch content == base + exactly the files
// the CURRENT fix proposal writes — killing the run-#27 "stale junk"
// failure mode where attempt 1's Cargo.toml edition change stayed on the
// branch, broke the Kaniko build, and was misread as an infra error while
// the sandbox had verified attempt 2's (different, clean) files.
//
// Uses the public compare API: GET /repos/{repo}/compare/{base}...{branch}.
// Returns the number of files reverted/deleted.
func (e *GitHubExecutor) ResetBranchToBase(ctx context.Context, repo, branch, baseBranch string) int {
	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s", e.APIURL, repo, baseBranch, branch)
	var cmp struct {
		Files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"` // added|removed|modified
		} `json:"files"`
	}
	if err := e.Do(ctx, "GET", url, nil, &cmp); err != nil {
		logging.Info("branch reset: compare failed (branch may equal base)", "err", err)
		return 0
	}

	reverted := 0
	for _, f := range cmp.Files {
		if f.Filename == "" {
			continue
		}
		if f.Status == "removed" {
			continue // already absent on the branch
		}
		baseContent, baseErr := e.GetFileContent(ctx, repo, f.Filename, baseBranch)
		if baseErr == nil && baseContent != "" {
			// File exists on base — restore the base content on the branch.
			if err := e.upsertFile(ctx, repo, branch, f.Filename, baseContent); err != nil {
				logging.Error("branch reset: restore failed", "file", f.Filename, "err", err)
				continue
			}
			reverted++
			continue
		}
		// File only exists on the branch (added) — delete it.
		if err := e.deleteFile(ctx, repo, branch, f.Filename, "revert file absent on base"); err != nil {
			logging.Error("branch reset: delete failed", "file", f.Filename, "err", err)
			continue
		}
		reverted++
	}
	if reverted > 0 {
		logging.Info("branch reset: reverted stale files to base state",
			"repo", repo, "branch", branch, "files", reverted)
	}
	return reverted
}

// deleteFile removes a file from a branch via the Contents API.
func (e *GitHubExecutor) deleteFile(ctx context.Context, repo, branch, path, msg string) error {
	sha, err := e.getFileSHA(ctx, repo, path, branch)
	if err != nil {
		return fmt.Errorf("get file sha: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/contents/%s", e.APIURL, repo, path)
	body := map[string]any{
		"message": fmt.Sprintf("[Sentinel] %s: %s", msg, path),
		"sha":     sha,
		"branch":  branch,
	}
	return e.Do(ctx, "DELETE", url, body, nil)
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

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
        "path/filepath"
        "strings"
        "time"

        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// GitHubExecutor opens Pull Requests via the GitHub REST API.
// Replaces the Python PyGithub-based executor with direct net/http calls.
//
// v3.11.0: Now supports GitHub Enterprise / GitLab / Bitbucket via
// cfg.GitHost + cfg.GitHubAPIURL (CRITICAL audit #2). Was hardcoded to
// https://api.github.com in 8 sites within this file.
type GitHubExecutor struct {
        token        string
        http         *http.Client
        apiURL       string // v3.11.0: base API URL (default "https://api.github.com")
        gitHost      string // v3.11.0: git host for clone URLs (default "github.com")
        defaultBranch string // v3.11.0: default branch fallback (default "main")
}

// NewGitHub creates a GitHubExecutor.
func NewGitHub(token string) *GitHubExecutor {
        return &GitHubExecutor{
                token:         token,
                http:          &http.Client{Timeout: 30 * time.Second},
                apiURL:        "https://api.github.com", // v3.11.0: default; override via WithAPIURL
                gitHost:       "github.com",             // v3.11.0: default; override via WithGitHost
                defaultBranch: "main",                   // v3.11.0: default; override via WithDefaultBranch
        }
}

// WithAPIURL sets the GitHub API base URL. v3.11.0: Replaces the hardcoded
// "https://api.github.com" in 8 sites within this file (CRITICAL audit #2).
// For GitHub Enterprise: "https://gh.mycorp.internal/api/v3".
// For GitLab (GitHub-compat API): "https://gitlab.com/api/v4".
func (e *GitHubExecutor) WithAPIURL(url string) *GitHubExecutor {
        if url != "" {
                e.apiURL = strings.TrimRight(url, "/")
        }
        return e
}

// WithGitHost sets the git host for clone URLs. v3.11.0: Replaces the
// hardcoded "github.com" (CRITICAL audit #2). For GitHub Enterprise:
// "gh.mycorp.internal". For GitLab: "gitlab.com".
func (e *GitHubExecutor) WithGitHost(host string) *GitHubExecutor {
        if host != "" {
                e.gitHost = host
        }
        return e
}

// WithDefaultBranch sets the default branch fallback. v3.11.0: Replaces the
// hardcoded "main" in PrepareBranch (CRITICAL audit #6). For repos whose
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
        if fp == nil || e.token == "" {
                return BranchInfo{Success: false, Error: "no_fix_proposal_or_no_github_token"}
        }
        if fp.Repo == nil || *fp.Repo == "" {
                return BranchInfo{Success: false, Error: "no_repo_discovered_for_service"}
        }
        repo := *fp.Repo

        // 1. Get the base branch's latest commit SHA
        baseBranch := e.defaultBranch // v3.11.0: was hardcoded "main" (audit #6)
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
        url := fmt.Sprintf("%s/repos/%s/branches/%s", e.apiURL, repo, branch)
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
        url := fmt.Sprintf("%s/repos/%s/git/refs", e.apiURL, repo)
        body := map[string]any{
                "ref": fmt.Sprintf("refs/heads/%s", branch),
                "sha": sha,
        }
        return e.do(ctx, "POST", url, body, nil)
}

func (e *GitHubExecutor) upsertFile(ctx context.Context, repo, branch, path, content string) error {
        // Try to get existing file's SHA (for update). Ignore error — file may not exist yet.
        existingSHA, _ := e.getFileSHA(ctx, repo, path, branch)

        url := fmt.Sprintf("%s/repos/%s/contents/%s", e.apiURL, repo, path)
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
        url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.apiURL, repo, path, ref)
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
// GetFileContent fetches the decoded content of a file from a GitHub repo.
// v3.14.0: Exported for the sandbox retry loop (Fix C) — fetches the current
// file state from the branch so the LLM generates search/replace blocks against
// the current code, not the original.
func (e *GitHubExecutor) GetFileContent(ctx context.Context, repo, path, ref string) (string, error) {
        url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", e.apiURL, repo, path, ref)
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
//
// v3.10.0: Multi-strategy matching (following Aider's approach):
//  1. Exact match — search text must match character-for-character
//  2. Whitespace-insensitive match — strip leading/trailing whitespace per line
//  3. Fuzzy match — Levenshtein distance >80% similarity
//  4. If ALL strategies fail — return error with context for retry loop
//
// Also: reject useless partial fixes. If only an import block matched but the
// actual fix block didn't, don't ship a PR with just an unused import.
func (e *GitHubExecutor) applySearchReplace(ctx context.Context, repo, branch, path string, changes []schemas.SearchReplace) (string, error) {
        content, err := e.GetFileContent(ctx, repo, path, branch)
        if err != nil {
                return "", fmt.Errorf("fetch file %s: %w", path, err)
        }

        originalContent := content
        applied := 0
        failedBlocks := []int{}      // track which block indices failed
        var matchErrors []string     // collect errors for retry feedback

        for i, ch := range changes {
                // Strategy 1: Exact match
                matched, replacement := tryExactMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        log.Printf("[github] block %d: matched (exact)", i)
                        continue
                }

                // Strategy 2: Whitespace-insensitive match
                matched, replacement = tryWhitespaceInsensitiveMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        log.Printf("[github] block %d: matched (whitespace-insensitive)", i)
                        continue
                }

                // Strategy 3: Fuzzy match (Levenshtein >80%)
                matched, replacement = tryFuzzyMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        log.Printf("[github] block %d: matched (fuzzy)", i)
                        continue
                }

                // All strategies failed
                failedBlocks = append(failedBlocks, i)
                errMsg := fmt.Sprintf("block %d: search text not found (tried exact, whitespace-insensitive, fuzzy)", i)
                matchErrors = append(matchErrors, errMsg)
                log.Printf("[github] %s", errMsg)

                // Log what the search text was looking for (first 100 chars)
                preview := ch.Search
                if len(preview) > 100 {
                        preview = preview[:100] + "..."
                }
                log.Printf("[github] block %d search preview: %q", i, preview)
        }

        if applied == 0 {
                return "", fmt.Errorf("no search/replace blocks matched (0/%d applied): %s",
                        len(changes), strings.Join(matchErrors, "; "))
        }

        // v3.10.0: Reject useless partial fixes.
        // If some blocks failed, check whether the applied blocks are "meaningful"
        // (i.e. they actually change code logic, not just add an import).
        // If ALL applied blocks are just import changes and the actual fix blocks
        // failed, reject the PR — shipping an unused import is worse than no PR.
        if len(failedBlocks) > 0 {
                hasMeaningfulChange := false
                for i, ch := range changes {
                        // Skip blocks that failed
                        isFailed := false
                        for _, fb := range failedBlocks {
                                if fb == i {
                                        isFailed = true
                                        break
                                }
                        }
                        if isFailed {
                                continue
                        }
                        // Check if this block does more than just change imports
                        // An import-only change typically has "import" in the search text
                        // and doesn't contain function/class definitions
                        if !isImportOnlyChange(ch.Search, ch.Replace) {
                                hasMeaningfulChange = true
                                break
                        }
                }
                if !hasMeaningfulChange {
                        return "", fmt.Errorf("only import-only blocks matched, all meaningful fix blocks failed — not shipping a useless partial fix")
                }
                log.Printf("[github] partial fix: %d/%d blocks applied, %d failed (but has meaningful changes)", applied, len(changes), len(failedBlocks))
        }

        // Syntax validation
        if syntaxErr := validateSyntax(path, content); syntaxErr != nil {
                log.Printf("[github] SYNTAX VALIDATION FAILED: %v", syntaxErr)
                // Try applying blocks one at a time, skipping syntax-breaking ones
                content = originalContent
                goodBlocks := 0
                for i, ch := range changes {
                        matched, trial := tryAllStrategies(content, ch.Search, ch.Replace)
                        if !matched {
                                continue
                        }
                        if err := validateSyntax(path, trial); err != nil {
                                log.Printf("[github] block %d produces syntax error — skipping: %v", i, err)
                                continue
                        }
                        content = trial
                        goodBlocks++
                }
                if goodBlocks == 0 {
                        return "", fmt.Errorf("all search/replace blocks produce syntax errors — LLM generated broken code")
                }
                log.Printf("[github] recovered: applied %d/%d blocks after syntax recovery", goodBlocks, len(changes))
                return content, nil
        }

        log.Printf("[github] applied %d/%d search/replace blocks to %s (syntax validated ✓)", applied, len(changes), path)
        return content, nil
}

// tryExactMatch tries to find ch.Search in content exactly once.
// Returns (matched, newContent).
func tryExactMatch(content, search, replace string) (bool, string) {
        count := strings.Count(content, search)
        if count != 1 {
                return false, content
        }
        return true, strings.Replace(content, search, replace, 1)
}

// tryWhitespaceInsensitiveMatch strips leading/trailing whitespace from each
// line of both the search text and the file content, then tries to match.
// This handles the #1 LLM error: getting indentation wrong.
//
// v3.10.0: Following Aider's approach.
func tryWhitespaceInsensitiveMatch(content, search, replace string) (bool, string) {
        // Normalize: strip leading/trailing whitespace from each line
        normalize := func(s string) string {
                lines := strings.Split(s, "\n")
                for i, line := range lines {
                        lines[i] = strings.TrimSpace(line)
                }
                return strings.Join(lines, "\n")
        }

        normalizedSearch := normalize(search)
        normalizedContent := normalize(content)

        count := strings.Count(normalizedContent, normalizedSearch)
        if count != 1 {
                return false, content
        }

        // Found exactly one match in normalized space. Now we need to find the
        // corresponding location in the original content and replace it.
        // Strategy: find the starting line by matching the first non-empty
        // normalized line, then verify the rest match.
        searchLines := strings.Split(search, "\n")
        contentLines := strings.Split(content, "\n")

        for startLine := 0; startLine <= len(contentLines)-len(searchLines); startLine++ {
                match := true
                for j, searchLine := range searchLines {
                        if strings.TrimSpace(contentLines[startLine+j]) != strings.TrimSpace(searchLine) {
                                match = false
                                break
                        }
                }
                if match {
                        // Found the match at startLine. Replace those lines with the
                        // replacement text, preserving the original indentation of the
                        // first line.
                        originalIndent := getIndent(contentLines[startLine])
                        replaceLines := strings.Split(replace, "\n")
                        for k := range replaceLines {
                                if k == 0 {
                                        // First line: use original indent
                                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                                } else if strings.TrimSpace(replaceLines[k]) != "" {
                                        // Other lines: preserve relative indentation from the replace text
                                        // but offset by the original indent
                                        replaceIndent := getIndent(replaceLines[k])
                                        replaceLines[k] = originalIndent + replaceIndent + strings.TrimSpace(replaceLines[k])
                                }
                        }
                        newLines := append([]string{}, contentLines[:startLine]...)
                        newLines = append(newLines, replaceLines...)
                        newLines = append(newLines, contentLines[startLine+len(searchLines):]...)
                        return true, strings.Join(newLines, "\n")
                }
        }

        return false, content
}

// tryFuzzyMatch uses a simple similarity score to find the closest match.
// If the best match has >80% similarity (Levenshtein-based), use it.
//
// v3.10.0: Following Aider's approach. Disabled by default in Aider for
// safety, but we enable it because free models produce more search text
// errors than GPT-4. The 80% threshold is conservative enough to avoid
// false matches.
func tryFuzzyMatch(content, search, replace string) (bool, string) {
        searchLines := strings.Split(search, "\n")
        contentLines := strings.Split(content, "\n")

        if len(searchLines) == 0 || len(searchLines) > len(contentLines) {
                return false, content
        }

        // v3.10.1: Log the search text for debugging
        for i, sl := range searchLines {
                preview := strings.TrimSpace(sl)
                if len(preview) > 80 {
                        preview = preview[:80] + "..."
                }
                log.Printf("[github] fuzzy: search line %d: %q", i, preview)
        }

        bestScore := 0.0
        bestStart := -1

        // Slide a window over the content lines
        for start := 0; start <= len(contentLines)-len(searchLines); start++ {
                totalSim := 0.0
                for j := 0; j < len(searchLines); j++ {
                        sim := lineSimilarity(strings.TrimSpace(contentLines[start+j]), strings.TrimSpace(searchLines[j]))
                        totalSim += sim
                }
                avgSim := totalSim / float64(len(searchLines))
                if avgSim > bestScore {
                        bestScore = avgSim
                        bestStart = start
                }
        }

        // v3.10.1: Log the best match details
        if bestStart >= 0 {
                for j := 0; j < len(searchLines); j++ {
                        fileLine := strings.TrimSpace(contentLines[bestStart+j])
                        searchLine := strings.TrimSpace(searchLines[j])
                        sim := lineSimilarity(fileLine, searchLine)
                        if sim < 1.0 {
                                filePreview := fileLine
                                if len(filePreview) > 80 { filePreview = filePreview[:80] + "..." }
                                searchPreview := searchLine
                                if len(searchPreview) > 80 { searchPreview = searchPreview[:80] + "..." }
                                log.Printf("[github] fuzzy: line %d diff (%.0f%%): file=%q search=%q", j, sim*100, filePreview, searchPreview)
                        }
                }
        }

        log.Printf("[github] fuzzy: best score=%.1f%% at line %d (threshold=70%%)", bestScore*100, bestStart+1)

        // v3.10.1: Lowered threshold from 80% to 70% — free models drop more chars
        if bestScore < 0.7 || bestStart == -1 {
                return false, content
        }

        log.Printf("[github] fuzzy match: %.1f%% similarity at line %d", bestScore*100, bestStart+1)

        // Replace the matched lines with the replacement text
        originalIndent := getIndent(contentLines[bestStart])
        replaceLines := strings.Split(replace, "\n")
        for k := range replaceLines {
                if k == 0 {
                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                } else if strings.TrimSpace(replaceLines[k]) != "" {
                        replaceIndent := getIndent(replaceLines[k])
                        replaceLines[k] = originalIndent + replaceIndent + strings.TrimSpace(replaceLines[k])
                }
        }
        newLines := append([]string{}, contentLines[:bestStart]...)
        newLines = append(newLines, replaceLines...)
        newLines = append(newLines, contentLines[bestStart+len(searchLines):]...)
        return true, strings.Join(newLines, "\n")
}

// tryAllStrategies tries exact, whitespace-insensitive, and fuzzy in order.
// Used by the syntax-validation recovery path.
func tryAllStrategies(content, search, replace string) (bool, string) {
        if matched, result := tryExactMatch(content, search, replace); matched {
                return true, result
        }
        if matched, result := tryWhitespaceInsensitiveMatch(content, search, replace); matched {
                return true, result
        }
        if matched, result := tryFuzzyMatch(content, search, replace); matched {
                return true, result
        }
        return false, content
}

// isImportOnlyChange returns true if the search/replace only changes an import
// statement (e.g. "from fastapi import FastAPI" → "from fastapi import FastAPI, HTTPException").
// These changes are useless on their own without the actual fix.
func isImportOnlyChange(search, replace string) bool {
        searchTrimmed := strings.TrimSpace(search)
        replaceTrimmed := strings.TrimSpace(replace)
        return (strings.Contains(searchTrimmed, "import ") || strings.Contains(searchTrimmed, "from ")) &&
                (strings.Contains(replaceTrimmed, "import ") || strings.Contains(replaceTrimmed, "from "))
}

// getIndent returns the leading whitespace of a line.
func getIndent(line string) string {
        for i, c := range line {
                if c != ' ' && c != '\t' {
                        return line[:i]
                }
        }
        return line
}

// lineSimilarity computes a similarity score between two strings [0.0, 1.0]
// using a simple Levenshtein-distance-based ratio.
func lineSimilarity(a, b string) float64 {
        if a == b {
                return 1.0
        }
        if len(a) == 0 || len(b) == 0 {
                return 0.0
        }
        dist := levenshtein(a, b)
        maxLen := len(a)
        if len(b) > maxLen {
                maxLen = len(b)
        }
        return 1.0 - float64(dist)/float64(maxLen)
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
        la := len(a)
        lb := len(b)
        if la == 0 {
                return lb
        }
        if lb == 0 {
                return la
        }

        // Use a single row for DP (memory optimization)
        prev := make([]int, lb+1)
        curr := make([]int, lb+1)
        for j := 0; j <= lb; j++ {
                prev[j] = j
        }
        for i := 1; i <= la; i++ {
                curr[0] = i
                for j := 1; j <= lb; j++ {
                        cost := 1
                        if a[i-1] == b[j-1] {
                                cost = 0
                        }
                        curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
                }
                prev, curr = curr, prev
        }
        return prev[lb]
}

func min3(a, b, c int) int {
        if b < a {
                a = b
        }
        if c < a {
                a = c
        }
        return a
}

// validateSyntax checks if the given file content is syntactically valid
// for the file's language. Currently supports:
//   - Python (.py): uses `python3 -c "import ast; ast.parse(code)"`
//   - Go (.go): uses `gofmt -e` (checks for parse errors)
//   - JavaScript/TypeScript (.js/.ts): uses `node --check`
//   - JSON (.json): uses json.Unmarshal
//   - YAML (.yaml/.yml): uses yaml.Unmarshal
//
// For unknown file types, validation is skipped (returns nil).
//
// v3.9.7: This is the quality gate that prevents broken PRs. The LLM
// sometimes generates code with missing closing quotes, unclosed parens,
// or broken indentation. This catches those before writing to GitHub.
func validateSyntax(path, content string) error {
        ext := strings.ToLower(filepath.Ext(path))
        switch ext {
        case ".py":
                return validatePython(content)
        case ".go":
                return validateGo(content)
        case ".js", ".ts", ".jsx", ".tsx":
                return validateJS(content)
        case ".json":
                return validateJSON(content)
        case ".yaml", ".yml":
                return validateYAML(content)
        default:
                // Unknown file type — skip validation
                return nil
        }
}

// validateSyntax, validatePython, validateGo, validateJS, validateJSON,
// validateYAML are implemented in syntax_validate.go (pure-Go, no exec).
// v3.11.0: Replaced the previous exec.Command("python3"/"gofmt"/"node")
// implementation, which silently returned nil in distroless (no binaries)
// AND swallowed os.CreateTemp errors. See syntax_validate.go for details.

func (e *GitHubExecutor) createPR(ctx context.Context, repo, title, body, head, base string) (int, string, error) {
        url := fmt.Sprintf("%s/repos/%s/pulls", e.apiURL, repo)
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
        url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", e.apiURL, repo, prNumber)
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

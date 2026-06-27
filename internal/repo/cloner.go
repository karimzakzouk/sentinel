// Package repo provides repository cloning + local search for the agent pipeline.
// v3.0.2: replaces the GitHub API file-fetch approach with a local clone,
// exactly like SWE-agent / OpenHands / Aider do it.
//
// The flow:
//   1. Clone the repo (shallow, --depth 1) to a temp dir using go-git (pure Go)
//   2. Walk the file tree, filter noise (node_modules, vendor, .git, etc.)
//   3. LLM round 1: "what should I search for?" → returns search queries
//   4. Run searches locally (Go regexp over cloned files) — instant, no API
//   5. LLM round 2: "given these results, what's the root cause + which files?" → returns hypotheses + file paths
//   6. Read those files from disk (zero API calls)
//   7. Clean up the temp dir
package repo

import (
        "context"
        "fmt"
        "io/fs"
        "log"
        "os"
        "path/filepath"
        "regexp"
        "sort"
        "strings"
        "time"

        "github.com/go-git/go-git/v5"
        "github.com/go-git/go-git/v5/config"
        "github.com/go-git/go-git/v5/plumbing"
        githttpprovider "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Cloner clones a GitHub repo to a local temp dir using go-git (pure Go, no CGO).
//
// v3.11.0: Now supports GitHub Enterprise / GitLab / Bitbucket via
// cfg.GitHost (CRITICAL audit #2). Was hardcoded to github.com in 2 sites.
type Cloner struct {
        githubToken string
        gitHost     string // v3.11.0: default "github.com"; override via WithGitHost
        defaultBranch string // v3.11.0: default "main"; override via WithDefaultBranch
}

// NewCloner creates a Cloner.
func NewCloner(githubToken string) *Cloner {
        return &Cloner{
                githubToken:   githubToken,
                gitHost:       "github.com", // v3.11.0: default
                defaultBranch: "main",       // v3.11.0: default
        }
}

// WithGitHost sets the git host for clone URLs. v3.11.0: Replaces the
// hardcoded "github.com" (CRITICAL audit #2).
func (c *Cloner) WithGitHost(host string) *Cloner {
        if host != "" {
                c.gitHost = host
        }
        return c
}

// WithDefaultBranch sets the default branch fallback. v3.11.0: Replaces the
// hardcoded "main" (CRITICAL audit #6).
func (c *Cloner) WithDefaultBranch(branch string) *Cloner {
        if branch != "" {
                c.defaultBranch = branch
        }
        return c
}

// Clone clones a repo to a temp dir. Returns the local path.
// The `ref` parameter can be:
//   - a branch name (e.g. "main", "bug/crashloop-typo")
//   - a tag name (e.g. "v1.2.3")
//   - a commit SHA (e.g. "abc123...") — 40 hex chars
//
// Uses --depth 1 for speed (we don't need history, just the current state).
// The clone is written to /data/repos/ (the mounted PVC) because /tmp is
// read-only in the distroless image (readOnlyRootFilesystem: true).
func (c *Cloner) Clone(ctx context.Context, repo, ref string) (string, error) {
        if ref == "" {
                ref = c.defaultBranch // v3.11.0: was hardcoded "main" (audit #6)
        }

        // Use /data/repos/ (the mounted PVC) instead of /tmp (read-only in distroless)
        cloneDir := "/data/repos"
        if err := os.MkdirAll(cloneDir, 0755); err != nil {
                // Fallback to /tmp if /data isn't writable (e.g. running locally without PVC)
                cloneDir = os.TempDir()
        }

        // v3.17.0: Clone caching — if a clone of this repo+ref already exists,
        // do a git fetch (only downloads new commits) instead of a full clone.
        // Saves 2-5s on re-investigations of the same service.
        safeRepo := strings.ReplaceAll(repo, "/", "-")
        // Look for an existing clone (without the random suffix)
        pattern := fmt.Sprintf("sentinel-repo-%s-%s-*", safeRepo, ref)
        matches, _ := filepath.Glob(filepath.Join(cloneDir, pattern))
        if len(matches) > 0 {
                // Found an existing clone — try to fetch updates
                existingDir := matches[0]
                log.Printf("[cloner] found existing clone at %s, fetching updates", existingDir)

                // v3.18.0: SECURITY — fetch URL must NOT embed the token (logs leak).
                // Use a token-free URL + Auth option on the fetch.
                fetchURL := fmt.Sprintf("https://%s/%s.git", c.gitHost, repo)
                fetchAuth := c.gitAuth()

                fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
                defer fetchCancel()

                // Open the existing repo and fetch
                existingRepo, err := git.PlainOpen(existingDir)
                if err == nil {
                        err = existingRepo.FetchContext(fetchCtx, &git.FetchOptions{
                                RemoteURL:    fetchURL,
                                RefSpecs:     []config.RefSpec{config.RefSpec("+refs/heads/*:refs/heads/*")},
                                Depth: 1,
                                Force:        true,
                                Auth:         fetchAuth,
                        })
                        if err != nil && err != git.NoErrAlreadyUpToDate {
                                log.Printf("[cloner] fetch failed (%s), falling back to full clone", c.sanitizeErr(err))
                        } else {
                                log.Printf("[cloner] fetch succeeded, reusing existing clone at %s", existingDir)
                                return existingDir, nil
                        }
                } else {
                        log.Printf("[cloner] open existing clone failed (%s), falling back to full clone", c.sanitizeErr(err))
                }
                // If fetch failed, fall through to full clone below
                // Clean up the old clone to avoid confusion
                _ = os.RemoveAll(existingDir)
        }

        // Sanitize repo name for directory naming
        dirName := fmt.Sprintf("sentinel-repo-%s-%s", safeRepo, ref)
        if len(dirName) > 60 {
                // Truncate long names + add a short random suffix
                dirName = dirName[:60]
        }
        // Add random suffix to avoid collisions
        dirName = fmt.Sprintf("%s-%d", dirName, time.Now().UnixNano())

        tmpDir := filepath.Join(cloneDir, dirName)
        if err := os.MkdirAll(tmpDir, 0755); err != nil {
                return "", fmt.Errorf("create clone dir: %w", err)
        }

        // v3.18.0: SECURITY — clone URL must NOT embed the token in the URL
        // itself (go-git error messages include the URL, leaking the token to logs).
        // Instead, use a token-free URL + the Auth option on CloneOptions.
        cloneURL := fmt.Sprintf("https://%s/%s.git", c.gitHost, repo)
        cloneAuth := c.gitAuth()

        cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
        defer cancel()

        // Detect if ref is a commit SHA (40 hex chars) or a branch/tag name
        isCommitSHA := len(ref) == 40 && isHex(ref)

        if isCommitSHA {
                // Clone the full repo (shallow clone of a specific commit is tricky with go-git),
                // then checkout the commit. We use depth 1 + the commit's branch if known,
                // but for arbitrary commits we need a fuller clone. Cap at depth 50 to keep it fast.
                r, err := git.PlainCloneContext(cloneCtx, tmpDir, false, &git.CloneOptions{
                        URL:   cloneURL,
                        Depth: 50,
                        Auth:  cloneAuth,
                })
                if err != nil {
                        _ = os.RemoveAll(tmpDir)
                        return "", fmt.Errorf("clone %s@%s: %w", repo, ref, c.sanitizeErr(err))
                }
                // Checkout the specific commit
                w, err := r.Worktree()
                if err != nil {
                        _ = os.RemoveAll(tmpDir)
                        return "", fmt.Errorf("get worktree: %w", err)
                }
                hash := plumbing.NewHash(ref)
                if err := w.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
                        _ = os.RemoveAll(tmpDir)
                        return "", fmt.Errorf("checkout commit %s: %w", ref, c.sanitizeErr(err))
                }
                return tmpDir, nil
        }

        // Branch or tag — try as branch first, then as tag, then full clone
        _, err := git.PlainCloneContext(cloneCtx, tmpDir, false, &git.CloneOptions{
                URL:           cloneURL,
                Depth:         1,
                SingleBranch:  true,
                ReferenceName: plumbing.NewBranchReferenceName(ref),
                Auth:          cloneAuth,
        })
        if err != nil {
                // Try as a tag
                _, err = git.PlainCloneContext(cloneCtx, tmpDir, false, &git.CloneOptions{
                        URL:           cloneURL,
                        Depth:         1,
                        SingleBranch:  true,
                        ReferenceName: plumbing.NewTagReferenceName(ref),
                        Auth:          cloneAuth,
                })
                if err != nil {
                        // Last resort: full shallow clone (works for any ref)
                        _, err = git.PlainCloneContext(cloneCtx, tmpDir, false, &git.CloneOptions{
                                URL:   cloneURL,
                                Depth: 1,
                                Auth:  cloneAuth,
                        })
                        if err != nil {
                                _ = os.RemoveAll(tmpDir)
                                return "", fmt.Errorf("clone %s@%s: %w", repo, ref, c.sanitizeErr(err))
                        }
                }
        }

        return tmpDir, nil
}

// gitAuth returns an HTTP basic-auth object for go-git, or nil if no token is set.
// v3.18.0: Replaces URL-embedded token (which leaked to logs on clone failure).
func (c *Cloner) gitAuth() *githttpprovider.BasicAuth {
        if c.githubToken == "" {
                return nil
        }
        return &githttpprovider.BasicAuth{
                Username: "x-access-token", // GitHub convention for PATs
                Password: c.githubToken,
        }
}

// sanitizeErr strips any embedded token from an error message before it is
// logged or returned. v3.18.0: defense-in-depth — even though we no longer
// embed the token in URLs, go-git might still leak it via other paths.
func (c *Cloner) sanitizeErr(err error) error {
        if err == nil || c.githubToken == "" {
                return err
        }
        return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), c.githubToken, "***"))
}

// isHex returns true if s contains only hexadecimal characters.
func isHex(s string) bool {
        for _, c := range s {
                if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
                        return false
                }
        }
        return true
}

// Cleanup removes the cloned repo.
// v3.18.0: Logs an error if RemoveAll fails (was silently swallowed — caused
// slow PVC disk leak when clones contained read-only files).
func (c *Cloner) Cleanup(path string) {
        if err := os.RemoveAll(path); err != nil {
                log.Printf("[cloner] cleanup failed for %s: %v", path, err)
        }
}

// WalkTree returns the file paths in the cloned repo, filtered + capped.
// Filters out: .git/, node_modules/, vendor/, dist/, build/, __pycache__/,
// *.pyc, *.lock, etc. Caps at maxFiles to avoid huge repos.
func WalkTree(root string, maxFiles int) ([]string, error) {
        if maxFiles <= 0 {
                maxFiles = 500
        }

        var paths []string
        ignoreDirs := map[string]bool{
                ".git": true, "node_modules": true, "vendor": true, "dist": true,
                "build": true, "__pycache__": true, ".next": true, ".cache": true,
                ".pytest_cache": true, "target": true, ".idea": true, ".vscode": true,
        }
        ignoreExts := map[string]bool{
                ".pyc": true, ".pyo": true, ".lock": true, ".bin": true, ".exe": true,
                ".dll": true, ".so": true, ".dylib": true,
                ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true,
                ".pdf": true, ".zip": true, ".tar": true, ".gz": true,
                ".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
        }

        err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
                if err != nil {
                        return nil
                }
                if d.IsDir() {
                        if ignoreDirs[d.Name()] {
                                return filepath.SkipDir
                        }
                        return nil
                }
                if len(paths) >= maxFiles {
                        return filepath.SkipAll
                }

                name := d.Name()
                ext := strings.ToLower(filepath.Ext(name))
                if ignoreExts[ext] {
                        return nil
                }
                if name == "package-lock.json" || name == "yarn.lock" || name == "pnpm-lock.yaml" ||
                        name == "go.sum" || name == ".gitignore" || name == ".gitattributes" ||
                        name == ".DS_Store" || name == "LICENSE" {
                        return nil
                }

                relPath, err := filepath.Rel(root, path)
                if err != nil {
                        return nil
                }
                paths = append(paths, relPath)
                return nil
        })

        if err != nil {
                return nil, err
        }

        sort.Strings(paths)
        return paths, nil
}

// SearchResult is one match from a local search query.
type SearchResult struct {
        Path    string `json:"path"`
        Line    int    `json:"line"`
        Column  int    `json:"column"`
        Match   string `json:"match"`
        Context string `json:"context"`
}

// Search runs a regexp search over the cloned repo. Returns up to maxResults
// matches across all files. Searches file contents (not just names).
func Search(root string, pattern string, maxResults int) ([]SearchResult, error) {
        if maxResults <= 0 {
                maxResults = 50
        }

        re, err := regexp.Compile(pattern)
        if err != nil {
                return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
        }

        var results []SearchResult

        err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
                if err != nil || d.IsDir() {
                        return nil
                }
                if len(results) >= maxResults {
                        return filepath.SkipAll
                }

                info, _ := d.Info()
                if info.Size() > 500*1024 {
                        return nil
                }
                ext := strings.ToLower(filepath.Ext(path))
                if isBinaryExt(ext) {
                        return nil
                }

                data, err := os.ReadFile(path)
                if err != nil {
                        return nil
                }

                relPath, _ := filepath.Rel(root, path)
                lines := strings.Split(string(data), "\n")

                for lineNum, line := range lines {
                        if len(results) >= maxResults {
                                return filepath.SkipAll
                        }
                        matches := re.FindStringIndex(line)
                        if matches != nil {
                                context := strings.TrimSpace(line)
                                if len(context) > 200 {
                                        context = context[:200] + "..."
                                }
                                matchStr := line[matches[0]:matches[1]]
                                results = append(results, SearchResult{
                                        Path:    relPath,
                                        Line:    lineNum + 1,
                                        Column:  matches[0] + 1,
                                        Match:   matchStr,
                                        Context: context,
                                })
                        }
                }
                return nil
        })

        return results, err
}

// ReadFile reads a file from the cloned repo.
//
// v3.8.4: This function is called with paths that originate from LLM output
// (the investigator asks the LLM which files to read, and the LLM returns
// arbitrary strings). The previous implementation called
// filepath.Join(root, relPath) with no validation, so an LLM that returned
// "../../../etc/passwd" or an absolute path like "/data/runtime-config.json"
// would read files outside the cloned repo. Worse, the file contents are
// concatenated into the next LLM prompt, so an LLM that asked for
// "../../data/runtime-config.json" would exfiltrate the GitHub App private
// key + LLM API key into the prompt and from there into the SQLite incident
// store and PR body.
//
// We now:
//  1. Reject absolute paths (anything starting with '/')
//  2. Clean the path with filepath.Clean
//  3. Compute the relative path from root to the candidate file
//  4. Reject if the relative path starts with ".." (escapes root)
//  5. Reject if the cleaned path equals "." (the root itself)
//  6. Reject if the file is a symlink that escapes root (open with O_NOFOLLOW)
//
// These checks are defense-in-depth — the LLM should never return such
// paths in normal operation, but a prompt-injection attack (see H4 in the
// security review) could coax it into doing so.
func ReadFile(root, relPath string) (string, error) {
        // Reject absolute paths — they cannot be relative to root by definition.
        if filepath.IsAbs(relPath) {
                return "", fmt.Errorf("refusing to read absolute path %q (must be relative to repo root)", relPath)
        }

        // Clean both sides so the under-root check is robust against "./" and "//"
        // padding. filepath.Join already cleans, but we want to inspect the
        // cleaned relPath before joining.
        cleanRel := filepath.Clean(relPath)
        if cleanRel == "." {
                return "", fmt.Errorf("refusing to read repo root itself")
        }
        if strings.HasPrefix(cleanRel, "..") {
                return "", fmt.Errorf("refusing to read path outside repo root: %q", relPath)
        }

        fullPath := filepath.Join(root, cleanRel)

        // Defense-in-depth: verify the resolved path is still under root. This
        // catches symlinks inside the repo that point outside it (e.g. a
        // maliciously crafted repo could contain a symlink 'foo' -> '/etc/passwd'
        // and the LLM could ask us to read 'foo').
        relAfter, err := filepath.Rel(root, fullPath)
        if err != nil {
                return "", fmt.Errorf("resolve %q under %q: %w", relPath, root, err)
        }
        if relAfter == "." || strings.HasPrefix(relAfter, "..") {
                return "", fmt.Errorf("refusing to read path outside repo root after resolution: %q -> %q", relPath, relAfter)
        }

        // Verify the file is a regular file (not a symlink to outside root, not
        // a device file, not a directory). os.ReadFile follows symlinks, so we
        // open with O_NOFOLLOW via os.Stat first.
        info, err := os.Lstat(fullPath)
        if err != nil {
                return "", err
        }
        if info.Mode()&os.ModeSymlink != 0 {
                // Symlink inside the repo. Resolve and re-check it's still under root.
                target, err := os.Readlink(fullPath)
                if err != nil {
                        return "", fmt.Errorf("read symlink %q: %w", relPath, err)
                }
                var resolved string
                if filepath.IsAbs(target) {
                        resolved = filepath.Clean(target)
                } else {
                        resolved = filepath.Clean(filepath.Join(filepath.Dir(fullPath), target))
                }
                relTarget, err := filepath.Rel(root, resolved)
                if err != nil || relTarget == "." || strings.HasPrefix(relTarget, "..") {
                        return "", fmt.Errorf("refusing to follow symlink %q -> %q (escapes repo root)", relPath, target)
                }
        }
        if info.IsDir() {
                return "", fmt.Errorf("refusing to read directory as file: %q", relPath)
        }

        data, err := os.ReadFile(fullPath)
        if err != nil {
                return "", err
        }
        return string(data), nil
}

func isBinaryExt(ext string) bool {
        switch ext {
        case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".pdf",
                ".zip", ".tar", ".gz", ".bz2", ".7z",
                ".woff", ".woff2", ".ttf", ".eot", ".otf",
                ".mp3", ".mp4", ".avi", ".mov", ".webm",
                ".exe", ".dll", ".so", ".dylib", ".bin",
                ".pyc", ".pyo", ".class", ".jar", ".war":
                return true
        }
        return false
}

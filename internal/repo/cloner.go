// Package repo clones a GitHub repository to a local directory and provides
// filtered tree walking, regexp search, and path-safe file reads.
//
// The cloner uses go-git (pure Go, no CGO) and shallow clones (depth 1).
// On re-investigations of the same service it tries to fetch into an
// existing clone before falling back to a full clone.
//
// ReadFile enforces strict path confinement to root: absolute paths,
// parent escapes, and symlinks pointing outside root are rejected. This
// is the primary defense against prompt-injection coaxing the LLM into
// reading /etc/passwd or the Sentinel config.
package repo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Cloner clones GitHub repositories to local directories.
type Cloner struct {
	githubToken   string
	gitHost       string
	defaultBranch string
}

// NewCloner returns a Cloner with default host "github.com" and default
// branch "main". Use WithGitHost / WithDefaultBranch to override.
func NewCloner(githubToken string) *Cloner {
	return &Cloner{
		githubToken:   githubToken,
		gitHost:       "github.com",
		defaultBranch: "main",
	}
}

// WithGitHost sets the git host for clone URLs (e.g. "gh.mycorp.internal"
// for GitHub Enterprise).
func (c *Cloner) WithGitHost(host string) *Cloner {
	if host != "" {
		c.gitHost = host
	}
	return c
}

// WithDefaultBranch sets the fallback branch when ref is empty.
func (c *Cloner) WithDefaultBranch(branch string) *Cloner {
	if branch != "" {
		c.defaultBranch = branch
	}
	return c
}

// Clone clones repo at ref (branch, tag, or 40-char commit SHA) to a local
// directory under /data/repos (or os.TempDir() if /data is not writable).
// Returns the local path. The clone is shallow (depth 1) for branches and
// tags; commit SHAs require depth 50 because go-git cannot shallow-clone
// arbitrary commits.
//
// On re-investigations, an existing clone matching repo+ref is fetched
// (depth 1) instead of doing a full re-clone.
func (c *Cloner) Clone(ctx context.Context, repo, ref string) (string, error) {
	if ref == "" {
		ref = c.defaultBranch
	}

	cloneDir := "/data/repos"
	if err := os.MkdirAll(cloneDir, 0o755); err != nil {
		cloneDir = os.TempDir()
	}

	safeRepo := strings.ReplaceAll(repo, "/", "-")
	pattern := fmt.Sprintf("sentinel-repo-%s-%s-*", safeRepo, ref)
	if matches, _ := filepath.Glob(filepath.Join(cloneDir, pattern)); len(matches) > 0 {
		existingDir := matches[0]
		if c.tryFetch(ctx, existingDir, repo) {
			return existingDir, nil
		}
		_ = os.RemoveAll(existingDir)
	}

	dirName := fmt.Sprintf("sentinel-repo-%s-%s", safeRepo, ref)
	if len(dirName) > 60 {
		dirName = dirName[:60]
	}
	dirName = fmt.Sprintf("%s-%d", dirName, time.Now().UnixNano())
	tmpDir := filepath.Join(cloneDir, dirName)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create clone dir: %w", err)
	}

	cloneURL := fmt.Sprintf("https://%s/%s.git", c.gitHost, repo)
	auth := c.gitAuth()

	cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if len(ref) == 40 && isHex(ref) {
		return c.cloneCommit(cloneCtx, tmpDir, cloneURL, repo, ref, auth)
	}
	return c.cloneRef(cloneCtx, tmpDir, cloneURL, repo, ref, auth)
}

// tryFetch attempts a depth-1 fetch of repo into existingDir. Returns true
// on success (including already-up-to-date).
func (c *Cloner) tryFetch(ctx context.Context, existingDir, repo string) bool {
	logging.Info("found existing clone, fetching updates", "dir", existingDir)
	fetchURL := fmt.Sprintf("https://%s/%s.git", c.gitHost, repo)
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	r, err := git.PlainOpen(existingDir)
	if err != nil {
		logging.Error("open existing clone failed, falling back to full clone", "err", c.sanitizeErr(err))
		return false
	}
	err = r.FetchContext(fetchCtx, &git.FetchOptions{
		RemoteURL: fetchURL,
		RefSpecs:  []config.RefSpec{config.RefSpec("+refs/heads/*:refs/heads/*")},
		Depth:     1,
		Force:     true,
		Auth:      c.gitAuth(),
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		logging.Error("fetch failed, falling back to full clone", "err", c.sanitizeErr(err))
		return false
	}
	logging.Info("fetch succeeded, reusing clone", "dir", existingDir)
	return true
}

func (c *Cloner) cloneCommit(ctx context.Context, dir, url, repo, ref string, auth *githttp.BasicAuth) (string, error) {
	r, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:   url,
		Depth: 50,
		Auth:  auth,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("clone %s@%s: %w", repo, ref, c.sanitizeErr(err))
	}
	w, err := r.Worktree()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("get worktree: %w", err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(ref)}); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("checkout commit %s: %w", ref, c.sanitizeErr(err))
	}
	return dir, nil
}

func (c *Cloner) cloneRef(ctx context.Context, dir, url, repo, ref string, auth *githttp.BasicAuth) (string, error) {
	_, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           url,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(ref),
		Auth:          auth,
	})
	if err == nil {
		return dir, nil
	}
	_, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           url,
		Depth:         1,
		SingleBranch:  true,
		ReferenceName: plumbing.NewTagReferenceName(ref),
		Auth:          auth,
	})
	if err == nil {
		return dir, nil
	}
	_, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:   url,
		Depth: 1,
		Auth:  auth,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("clone %s@%s: %w", repo, ref, c.sanitizeErr(err))
	}
	return dir, nil
}

// gitAuth returns an HTTP basic-auth object for go-git, or nil if no token is set.
func (c *Cloner) gitAuth() *githttp.BasicAuth {
	if c.githubToken == "" {
		return nil
	}
	return &githttp.BasicAuth{
		Username: "x-access-token",
		Password: c.githubToken,
	}
}

// sanitizeErr strips the embedded token from err's message. Defense-in-depth:
// even though we never embed the token in URLs, go-git might leak it via
// other paths.
func (c *Cloner) sanitizeErr(err error) error {
	if err == nil || c.githubToken == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), c.githubToken, "***"))
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Cleanup removes a cloned repo directory.
func (c *Cloner) Cleanup(path string) {
	if err := os.RemoveAll(path); err != nil {
		logging.Error("repo cleanup failed", "path", path, "err", err)
	}
}

// WalkTree returns the file paths in root, filtered and capped at maxFiles.
// Filters out: .git/, node_modules/, vendor/, dist/, build/, __pycache__/,
// *.pyc, *.lock, lockfiles, .gitignore, LICENSE, etc.
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
	ignoreNames := map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
		"go.sum": true, ".gitignore": true, ".gitattributes": true,
		".DS_Store": true, "LICENSE": true,
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
		if ignoreNames[name] {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ignoreExts[ext] {
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

// Search runs a regexp search over the cloned repo at root. Returns up to
// maxResults matches across all files. Files larger than 500KB and files
// with binary extensions are skipped.
func Search(root, pattern string, maxResults int) ([]SearchResult, error) {
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
		if isBinaryExt(strings.ToLower(filepath.Ext(path))) {
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
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			ctx := strings.TrimSpace(line)
			if len(ctx) > 200 {
				ctx = ctx[:200] + "..."
			}
			results = append(results, SearchResult{
				Path:    relPath,
				Line:    lineNum + 1,
				Column:  loc[0] + 1,
				Match:   line[loc[0]:loc[1]],
				Context: ctx,
			})
		}
		return nil
	})
	return results, err
}

// ReadFile reads relPath from the cloned repo at root. relPath must be a
// relative path that, after cleaning and resolution, stays under root.
// Absolute paths, parent escapes (".."), and symlinks pointing outside
// root are rejected. This is the primary defense against prompt-injection
// coaxing the LLM into reading files outside the cloned repo.
func ReadFile(root, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("refusing to read absolute path %q (must be relative to repo root)", relPath)
	}
	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." {
		return "", fmt.Errorf("refusing to read repo root itself")
	}
	if strings.HasPrefix(cleanRel, "..") {
		return "", fmt.Errorf("refusing to read path outside repo root: %q", relPath)
	}

	fullPath := filepath.Join(root, cleanRel)

	relAfter, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve %q under %q: %w", relPath, root, err)
	}
	if relAfter == "." || strings.HasPrefix(relAfter, "..") {
		return "", fmt.Errorf("refusing to read path outside repo root after resolution: %q -> %q", relPath, relAfter)
	}

	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
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

// Package repoci discovers a repository's own CI configuration — the test
// and format commands its GitHub Actions workflows run — so Sentinel can
// run the repo's real checks BEFORE opening a PR instead of discovering
// failures only after CI rejects the PR.
//
// Run #27 post-mortem: three ground-truth-correct fixes died in CI on
// things the sandbox never checked — cargo fmt formatting, the repo's own
// surefire/rspec suites. The sandbox runs LLM-GENERATED tests (circular
// validation); CI runs the repo's OWN tests (the real oracle). This
// package extracts the repo's oracle commands deterministically: YAML
// parsing, no LLM anywhere.
//
// Anti-injection stance: commands come from the repo's own CI files (the
// same thing CI runs on every push), never from Sentinel's knowledge of
// the eval. We only SELECT which of the repo's own commands to run.
package repoci

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/karimzakzouk/sentinel/internal/github"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Client lists and fetches repo files via the GitHub API. It caches git
// trees and workflow parses per (repo, ref) — one Discover call per
// incident is enough; repeats within a run hit the cache.
type Client struct {
	bc *github.BaseClient

	mu    sync.Mutex
	trees map[string][]string    // "repo@ref" -> file paths
	disco map[string]*Discovered // "repo@ref" -> discovery result
}

// NewClient creates a repoci Client on top of a GitHub BaseClient.
func NewClient(bc *github.BaseClient) *Client {
	return &Client{
		bc:    bc,
		trees: map[string][]string{},
		disco: map[string]*Discovered{},
	}
}

// Discovered is the result of CI discovery for one repo@ref.
type Discovered struct {
	// Language is the repo's primary language marker ("rust", "java",
	// "go", "node", "ruby", "python", "" when unknown).
	Language string
	// ToolchainImage is the container image used to run the commands —
	// a stock language toolchain that matches what GitHub CI runners
	// provide.
	ToolchainImage string
	// TestCmds are the repo's own test commands (from its CI files, or
	// language defaults when the repo has no CI).
	TestCmds []string
	// FmtCmds are format commands transformed to APPLY mode (they modify
	// files in place). Empty when the repo has no format check.
	FmtCmds []string
	// Source is where the commands came from: "workflows", "defaults",
	// or "none" (unknown language, gates skipped).
	Source string
	// TreePaths is the full file list at ref (used to locate test
	// sources for feedback).
	TreePaths []string
}

// workflowFile is the subset of a GitHub Actions workflow we parse.
type workflowFile struct {
	Jobs map[string]struct {
		Steps []struct {
			Run             string `yaml:"run"`
			ContinueOnError bool   `yaml:"continue-on-error"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// Discover extracts the repo's test + format commands. Never returns an
// error — discovery is best-effort; on failure it returns a Discovered
// with Source "none" and the caller skips the gates.
func (c *Client) Discover(ctx context.Context, repo, ref string) *Discovered {
	if repo == "" || ref == "" {
		return &Discovered{Source: "none"}
	}
	key := repo + "@" + ref
	c.mu.Lock()
	if d, ok := c.disco[key]; ok {
		c.mu.Unlock()
		return d
	}
	c.mu.Unlock()

	d := &Discovered{Source: "none"}

	// 1. Full file tree (single API call) — language detection + later
	// test-source lookup.
	paths, err := c.listTree(ctx, repo, ref)
	if err != nil {
		logging.Info("repoci: tree listing failed — repo-check gates skipped",
			"repo", repo, "ref", ref, "err", err)
		c.cacheDisco(key, d)
		return d
	}
	d.TreePaths = paths
	d.Language = detectLanguage(paths)
	if d.Language == "" {
		logging.Info("repoci: unknown language — repo-check gates skipped", "repo", repo)
		c.cacheDisco(key, d)
		return d
	}
	d.ToolchainImage = ToolchainImage(d.Language)

	// 2. Parse CI workflows for commands.
	wfPaths := listWorkflows(paths)
	var tests, fmts []string
	for _, wf := range wfPaths {
		content, err := c.getFile(ctx, repo, wf, ref)
		if err != nil {
			continue
		}
		t, f := extractCommands(content)
		tests = append(tests, t...)
		fmts = append(fmts, f...)
	}
	tests = dedupe(tests)
	fmts = dedupe(fmts)

	if len(tests) > 0 || len(fmts) > 0 {
		d.Source = "workflows"
	} else {
		d.Source = "defaults"
		tests = defaultTestCmds(d.Language)
		fmts = nil // never impose a formatter the repo doesn't use
	}
	d.TestCmds = tests
	d.FmtCmds = fmts

	logging.Info("repoci: discovered repo CI commands",
		"repo", repo, "ref", ref, "language", d.Language, "source", d.Source,
		"test_cmds", len(d.TestCmds), "fmt_cmds", len(d.FmtCmds))

	c.cacheDisco(key, d)
	return d
}

func (c *Client) cacheDisco(key string, d *Discovered) {
	c.mu.Lock()
	c.disco[key] = d
	c.mu.Unlock()
}

// listTree fetches the recursive git tree (file paths only).
func (c *Client) listTree(ctx context.Context, repo, ref string) ([]string, error) {
	key := repo + "@" + ref
	c.mu.Lock()
	if p, ok := c.trees[key]; ok {
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()

	var resp struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	url := fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", c.bc.APIURL, repo, ref)
	if err := c.bc.Do(ctx, "GET", url, nil, &resp); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(resp.Tree))
	for _, t := range resp.Tree {
		if t.Type == "blob" {
			paths = append(paths, t.Path)
		}
	}
	sort.Strings(paths)
	c.mu.Lock()
	c.trees[key] = paths
	c.mu.Unlock()
	return paths, nil
}

// listWorkflows returns .github/workflows/*.{yml,yaml} paths from the tree.
func listWorkflows(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !strings.HasPrefix(p, ".github/workflows/") {
			continue
		}
		if strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml") {
			out = append(out, p)
		}
	}
	return out
}

// GetFile fetches a file's decoded content from the Contents API.
func (c *Client) GetFile(ctx context.Context, repo, path, ref string) (string, error) {
	return c.getFile(ctx, repo, path, ref)
}

// getFile fetches a file's decoded content from the Contents API.
func (c *Client) getFile(ctx context.Context, repo, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", c.bc.APIURL, repo, path, ref)
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.bc.Do(ctx, "GET", url, nil, &resp); err != nil {
		return "", err
	}
	if resp.Encoding == "base64" {
		return github.Base64Decode(resp.Content)
	}
	return resp.Content, nil
}

// ─── Command classification ─────────────────────────────────────────────

// testPatterns: a run-step line containing any of these runs the repo's
// test suite (or a lint gate that blocks CI).
var testPatterns = []string{
	"cargo test", "cargo clippy", "cargo nextest",
	"mvn test", "mvn -", "maven test", "gradle test", "gradlew test", "gradle -",
	"go test", "go vet",
	"npm test", "npm run test", "yarn test", "pnpm test", "npm run lint",
	"jest", "vitest", "mocha",
	"pytest", "python -m pytest", "python3 -m pytest",
	"rspec", "bundle exec rspec", "minitest",
	"rubocop", "checkstyle", "spotless:check", "eslint", "ruff check", "flake8",
	"dotnet test",
}

// fmtPatterns: a run-step line containing any of these is a FORMAT check.
// These get transformed to apply-mode and run on the fix files only.
var fmtPatterns = []string{
	"cargo fmt", "gofmt", "go fmt", "spotless:apply", "prettier",
	"black ", "black --", "clang-format", "ktlint",
}

// dangerousCmdFragments reject extracted CI lines that do more than run a
// test/format tool (defense against a hostile repo's workflow files).
var dangerousCmdFragments = []string{
	"curl", "wget", "nc ", "/dev/tcp", "bash -c", "sh -c", "eval ",
	"$(", "`", "sudo", "docker ", "kubectl ", "helm ", "git push", "gh ",
	"aws ", "gcloud ", "ssh ", "scp ", "base64 -d", "chmod ", "rm -rf",
}

func lineIsDangerous(line string) bool {
	lower := strings.ToLower(line)
	for _, d := range dangerousCmdFragments {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}

func lineMatches(line string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// extractCommands parses one workflow file's YAML and returns
// (testCommands, formatCommands). Format commands are transformed to
// apply-mode.
func extractCommands(content string) (tests, fmts []string) {
	var wf workflowFile
	if err := yaml.Unmarshal([]byte(content), &wf); err != nil {
		return nil, nil
	}
	// Deterministic job order.
	jobNames := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)

	for _, name := range jobNames {
		job := wf.Jobs[name]
		for _, step := range job.Steps {
			if step.Run == "" || step.ContinueOnError {
				continue
			}
			for _, raw := range strings.Split(step.Run, "\n") {
				line := strings.TrimSpace(raw)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				// Strip common shell prefixes that would defeat
				// pattern matching.
				line = strings.TrimPrefix(line, "- ")
				if strings.Contains(line, "&&") {
					// Take the segment around the matched pattern.
					line = bestSegment(line)
				}
				if lineIsDangerous(line) {
					continue
				}
				if lineMatches(line, fmtPatterns) {
					if apply := FmtToApply(line); apply != "" {
						fmts = append(fmts, apply)
					}
					continue
				}
				if lineMatches(line, testPatterns) {
					tests = append(tests, line)
				}
			}
		}
	}
	return dedupe(tests), dedupe(fmts)
}

// bestSegment picks the &&-segment that matches a known pattern, so
// "npm ci && npm test" yields "npm test".
func bestSegment(line string) string {
	for _, seg := range strings.Split(line, "&&") {
		seg = strings.TrimSpace(seg)
		if lineMatches(seg, testPatterns) || lineMatches(seg, fmtPatterns) {
			return seg
		}
	}
	return line
}

// FmtToApply transforms a format-check command into its apply form.
// Returns "" for commands we don't know how to apply safely.
func FmtToApply(cmd string) string {
	c := strings.Join(strings.Fields(strings.TrimSpace(cmd)), " ")
	// normalize collapses the whitespace left behind by flag removal
	// ("black  ." -> "black .").
	normalize := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	switch {
	case strings.Contains(c, "cargo fmt"):
		// `cargo fmt --all -- --check` -> `cargo fmt --all`
		c = strings.ReplaceAll(c, "--check", "")
		c = strings.ReplaceAll(c, " -- ", " ")
		return normalize(c)
	case strings.Contains(c, "gofmt"):
		// `gofmt -l .` -> `gofmt -w .`; `gofmt -d` similar
		c = strings.ReplaceAll(c, " -l ", " -w ")
		c = strings.ReplaceAll(c, " -d ", " -w ")
		c = strings.ReplaceAll(c, " -l", " -w")
		return normalize(c)
	case strings.Contains(c, "go fmt"):
		return c
	case strings.Contains(c, "spotless:check"):
		return strings.ReplaceAll(c, "spotless:check", "spotless:apply")
	case strings.Contains(c, "spotless:apply"):
		return c
	case strings.Contains(c, "prettier"):
		c = strings.ReplaceAll(c, "--check", "--write")
		c = strings.ReplaceAll(c, "-c ", "--write ")
		c = strings.ReplaceAll(c, "--list-different", "--write")
		return normalize(c)
	case strings.Contains(c, "black"):
		c = strings.ReplaceAll(c, "--check", "")
		c = strings.ReplaceAll(c, "--diff", "")
		return normalize(c)
	case strings.Contains(c, "clang-format"):
		return "" // -i semantics vary; skip
	case strings.Contains(c, "ktlint"):
		return strings.ReplaceAll(c, " --check ", " -F ")
	default:
		return ""
	}
}

// defaultTestCmds returns the conventional test command when the repo has
// no CI workflows to parse.
func defaultTestCmds(language string) []string {
	switch language {
	case "rust":
		return []string{"cargo test"}
	case "java":
		return []string{"mvn -q test"}
	case "go":
		return []string{"go test ./..."}
	case "node":
		return []string{"npm test"}
	case "ruby":
		return []string{"bundle exec rspec"}
	case "python":
		return []string{"python -m pytest"}
	default:
		return nil
	}
}

// ToolchainImage maps a language to the stock toolchain image used to run
// the repo's commands. These match what GitHub CI runners provide closely
// enough to be the same oracle.
func ToolchainImage(language string) string {
	switch language {
	case "rust":
		return "rust:1-bookworm"
	case "java":
		return "maven:3.9-eclipse-temurin-21"
	case "go":
		return "golang:1.24-bookworm"
	case "node":
		return "node:22-bookworm"
	case "ruby":
		return "ruby:3.3-bookworm"
	case "python":
		return "python:3.12-bookworm"
	default:
		return ""
	}
}

// detectLanguage infers the repo language from marker files in the tree.
func detectLanguage(paths []string) string {
	has := func(name string) bool {
		for _, p := range paths {
			if p == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("Cargo.toml"):
		return "rust"
	case has("pom.xml") || has("build.gradle") || has("build.gradle.kts"):
		return "java"
	case has("go.mod"):
		return "go"
	case has("Gemfile"):
		return "ruby"
	case has("package.json"):
		return "node"
	case has("requirements.txt") || has("pyproject.toml") || has("setup.py"):
		return "python"
	default:
		return ""
	}
}

// dedupe removes duplicates, preserving order.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ─── Failing-test extraction ────────────────────────────────────────────

var (
	surefireFailedRe = regexp.MustCompile(`(?m)^\s*\[ERROR\]\s+([A-Za-z0-9_.$]+Test\.[A-Za-z0-9_$]+)`)
	rspecFailedRe    = regexp.MustCompile(`(?m)^\s*rspec (\./)?(\S+):\d+ # (.+)$`)
	cargoFailedRe    = regexp.MustCompile(`(?m)^\s*test (\S+) \.\.\. FAILED`)
	pytestFailedRe   = regexp.MustCompile(`(?m)^FAILED (\S+?)(?: - |$)`)
	goFailedRe       = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
	jestFailedRe     = regexp.MustCompile(`(?m)^\s*[✕×●]\s+(.+?)(?:\s+\(\d+.*\))?$`)
	minitestFailedRe = regexp.MustCompile(`(?m)^\s*\d+\)\s+(\S+#\S+.*)$`)
)

// ExtractFailedTests pulls failing test identifiers out of raw test output.
// Framework-specific regexes; best effort — an empty result falls back to
// command-level comparison in the caller.
func ExtractFailedTests(output string) []string {
	var out []string
	for _, m := range surefireFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	for _, m := range rspecFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, fmt.Sprintf("%s # %s", m[2], m[3]))
	}
	for _, m := range cargoFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	for _, m := range pytestFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	for _, m := range goFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	for _, m := range jestFailedRe.FindAllStringSubmatch(output, -1) {
		name := strings.TrimSpace(m[1])
		if name != "" && !strings.HasPrefix(name, "Test suite") {
			out = append(out, name)
		}
	}
	for _, m := range minitestFailedRe.FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	out = dedupe(out)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// TestSourcePaths maps failing test identifiers to likely source file
// paths in the repo tree. Used to include the failing tests' source in
// LLM feedback — seeing the assertion beats guessing at it (the run #27
// shipping-api lesson: the assertion diff arrived, but the test source
// never did, and the model never converged).
func TestSourcePaths(failedTests, treePaths []string) []string {
	pathSet := map[string]bool{}
	for _, p := range treePaths {
		pathSet[p] = true
	}
	var out []string
	for _, t := range failedTests {
		var candidates []string
		first := strings.Fields(t)[0]
		switch {
		case strings.HasSuffix(first, ".rb"): // rspec "spec/x_spec.rb # desc"
			candidates = []string{strings.TrimPrefix(first, "./")}
		case strings.HasSuffix(first, ".py"): // pytest "tests/x.py::test_y"
			candidates = []string{first}
		case strings.Contains(t, "::"): // pytest "path::test"
			base := strings.SplitN(t, "::", 2)[0]
			if strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".rb") {
				candidates = []string{base}
			}
		case strings.Contains(t, "#"): // minitest "Class#method"
			class := strings.SplitN(t, "#", 2)[0]
			candidates = filesNamed(treePaths, snakeCase(class)+".rb", "_test.rb")
		case strings.Contains(t, "Test."): // surefire "Class.method"
			class := strings.SplitN(t, ".", 2)[0]
			candidates = filesNamed(treePaths, class+".java", "Test.java")
		case strings.HasSuffix(t, ".rs"), strings.HasSuffix(t, ".go"):
			candidates = []string{t}
		}
		for _, cand := range candidates {
			if pathSet[cand] {
				out = append(out, cand)
				break
			}
		}
	}
	out = dedupe(out)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// filesNamed returns tree paths whose base name equals name (or ends with
// suffix as a fallback).
func filesNamed(treePaths []string, name, suffix string) []string {
	var exact, suffixMatches []string
	for _, p := range treePaths {
		base := p
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base == name {
			exact = append(exact, p)
		} else if strings.HasSuffix(base, suffix) {
			suffixMatches = append(suffixMatches, p)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return suffixMatches
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

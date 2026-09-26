// Package main — repo CI checks orchestration (v1.7.13).
//
// repofeedback.go runs the repository's OWN CI commands (formatter + test
// suite, parsed from .github/workflows) against the fix branch before the
// PR is opened, and turns failures into fix-proposer feedback. This moves
// CI's oracle into the pre-PR retry loop where the LLM can still act on
// it, instead of discovering regressions only after CI rejects the PR.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repoci"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// repoChecksBundle carries the results of one pre-PR repo-checks run.
type repoChecksBundle struct {
	ci       *repoci.Discovered
	fixRun   *executors.RunResult
	baseRun  *executors.RunResult
	newFails []string // tests that pass on base but fail on the fix branch
	preexist []string // tests failing on both branches
}

// repoCheckGate is the verdict of evaluating a bundle.
type repoCheckGate int

const (
	repoCheckPassed      repoCheckGate = iota // no new failures
	repoCheckRegression                       // fix broke something that worked on base
	repoCheckPreexisting                      // failures, but they exist on base too
	repoCheckUnavailable                      // checks couldn't run (skip gate)
)

// runRepoChecks executes the repo's own fmt + test commands on the fix
// branch (and once on the base branch, cached per repo@ref), then pushes
// any formatter-rewritten fix files to the branch. fixPaths are the files
// the fix touched — the formatter's changes to OTHER files are discarded
// to keep the diff minimal.
func (w *wiring) runRepoChecks(ctx context.Context, inc *schemas.Incident, repo, branch, baseBranch string, fixPaths []string) *repoChecksBundle {
	cfg := w.Config()
	if !cfg.RepoChecksEnabled || w.repoChecks == nil || w.repoci == nil {
		return nil
	}
	if repo == "" || branch == "" {
		return nil
	}

	ci := w.repoci.Discover(ctx, repo, baseBranch)
	// Run #30 post-mortem: BuildCmds must count. user-api's CI is a bare
	// `npx tsc --noEmit` typecheck (its `npm test` step is
	// continue-on-error "no tests yet") — the old condition skipped the
	// gate whenever a repo had no test commands, so the typecheck gate has
	// NEVER actually run despite being shipped in v1.7.14.
	if ci == nil || ci.Source == "none" ||
		(len(ci.TestCmds) == 0 && len(ci.FmtCmds) == 0 && len(ci.BuildCmds) == 0) {
		logging.Info("repo-checks: no CI commands discovered — gate skipped", "repo", repo)
		return nil
	}

	// Fix-branch run: fmt + tests + readback of fix files.
	fixRun, err := w.repoChecks.WithTimeout(time.Duration(cfg.RepoChecksTimeoutSec)*time.Second).
		Run(ctx, repo, branch, ci, executors.RunOpts{
			FmtEnabled:    true,
			ReadbackPaths: fixPaths,
		})
	if err != nil {
		logging.Error("repo-checks: fix-branch run failed — gate skipped (infra)",
			"repo", repo, "branch", branch, "err", err)
		return nil
	}

	// Push formatter output for the fix files to the branch so kaniko, the
	// PR, and CI all see the same formatted code.
	for _, path := range fixPaths {
		content, ok := fixRun.Readback[path]
		if !ok || content == "" {
			continue
		}
		if err := w.ghExec.UpsertFileToBranch(ctx, repo, branch, path, content); err != nil {
			logging.Error("repo-checks: pushing formatted file failed (non-blocking)",
				"repo", repo, "branch", branch, "path", path, "err", err)
			continue
		}
		logging.Info("repo-checks: pushed formatted file to branch",
			"repo", repo, "branch", branch, "path", path)
	}

	// Base-branch run (tests only), cached per repo@ref.
	baseRun := w.baseRepoChecks(ctx, repo, baseBranch, ci)

	newFails, preexist := executors.CompareRepoTests(fixRun, baseRun)
	b := &repoChecksBundle{ci: ci, fixRun: fixRun, baseRun: baseRun, newFails: newFails, preexist: preexist}
	logging.Info("repo-checks: verdict",
		"repo", repo, "branch", branch,
		"tests", fixRun.Summary(),
		"new_failures", len(newFails),
		"preexisting_failures", len(preexist))
	return b
}

// baseRepoChecks runs the repo's test commands on the base branch, once
// per (repo, ref), so "new failure" attribution never re-runs the suite.
func (w *wiring) baseRepoChecks(ctx context.Context, repo, baseBranch string, ci *repoci.Discovered) *executors.RunResult {
	key := repo + "@" + baseBranch
	w.baseChecksMu.Lock()
	if r, ok := w.baseChecksCache[key]; ok {
		w.baseChecksMu.Unlock()
		return r
	}
	w.baseChecksMu.Unlock()

	cfg := w.Config()
	run, err := w.repoChecks.WithTimeout(time.Duration(cfg.RepoChecksTimeoutSec)*time.Second).
		Run(ctx, repo, baseBranch, ci, executors.RunOpts{FmtEnabled: false})
	if err != nil {
		logging.Error("repo-checks: base-branch run failed — attribution degrades to fix-only",
			"repo", repo, "branch", baseBranch, "err", err)
		run = nil
	}
	w.baseChecksMu.Lock()
	w.baseChecksCache[key] = run
	w.baseChecksMu.Unlock()
	return run
}

// evaluateRepoChecks maps a bundle to a gate verdict.
func evaluateRepoChecks(b *repoChecksBundle) (repoCheckGate, *repoChecksBundle) {
	if b == nil || b.fixRun == nil || (!b.fixRun.TestsRan && !b.fixRun.BuildsRan) {
		return repoCheckUnavailable, b
	}
	if len(b.newFails) > 0 {
		return repoCheckRegression, b
	}
	if len(b.preexist) > 0 {
		return repoCheckPreexisting, b
	}
	return repoCheckPassed, b
}

// buildRepoTestsFeedback renders the feedback the fix proposer sees when
// the repo's own tests fail pre-PR. Includes the failing tests' SOURCE
// when it can be located — run #27 showed assertion diffs alone don't
// converge; the model needs to read the test.
func (w *wiring) buildRepoTestsFeedback(ctx context.Context, repo, baseBranch string, b *repoChecksBundle, regression bool) string {
	var sb strings.Builder
	if regression {
		sb.WriteString("## REPO TEST REGRESSION (pre-PR gate)\n\n")
		sb.WriteString("The repository's own test suite (the same one CI runs on every push) was executed against your fix branch.\n")
		sb.WriteString("These tests PASSED on the base branch but FAIL with your fix — your change broke them:\n\n")
	} else {
		sb.WriteString("## REPO TESTS STILL FAILING (pre-existing on base branch)\n\n")
		sb.WriteString("The repository's own test suite was executed against your fix branch. These tests ALREADY failed on the base branch and still fail:\n\n")
	}
	for _, f := range b.failedList() {
		fmt.Fprintf(&sb, "- %s\n", f)
	}
	sb.WriteString("\nDo NOT delete, skip, or weaken tests. Fix the code so the tests pass.\n")
	if !regression {
		sb.WriteString("Fix them ONLY if they are related to the incident you are fixing; otherwise keep your change minimal and leave them.\n")
	}

	// Failing-command output excerpts.
	if b.fixRun != nil {
		for _, c := range b.fixRun.Commands {
			if c.Phase == "test" && c.Exit != 0 {
				fmt.Fprintf(&sb, "\nCommand `%s` failed with:\n\n```\n%s\n```\n", c.Cmd, tailN(c.Output, 3000))
			}
		}
	}

	// Failing test sources — the strongest convergence signal.
	if srcs := w.fetchTestSources(ctx, repo, baseBranch, b); len(srcs) > 0 {
		sb.WriteString("\n## Failing test sources (read them before revising)\n\n")
		for _, s := range srcs {
			fmt.Fprintf(&sb, "### %s\n```%s\n%s\n```\n\n", s.path, langComment(s.path), tailN(s.content, 12000))
		}
	}
	return sb.String()
}

// failedList merges new + preexisting failures for display.
func (b *repoChecksBundle) failedList() []string {
	out := append([]string{}, b.newFails...)
	out = append(out, b.preexist...)
	if len(out) == 0 {
		return []string{"(test command failed — see output below)"}
	}
	return out
}

type testSource struct {
	path    string
	content string
}

// fetchTestSources locates and fetches the source files of failing tests.
func (w *wiring) fetchTestSources(ctx context.Context, repo, ref string, b *repoChecksBundle) []testSource {
	if b == nil || w.repoci == nil {
		return nil
	}
	paths := repoci.TestSourcePaths(b.failedList(), b.ci.TreePaths)
	var out []testSource
	for _, p := range paths {
		content, err := w.repoci.GetFile(ctx, repo, p, ref)
		if err != nil || content == "" {
			continue
		}
		out = append(out, testSource{path: p, content: content})
		if len(out) >= 2 {
			break
		}
	}
	return out
}

func langComment(path string) string {
	switch {
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".rb"):
		return "ruby"
	case strings.HasSuffix(path, ".java"):
		return "java"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".ts"):
		return "typescript"
	case strings.HasSuffix(path, ".js"):
		return "javascript"
	default:
		return ""
	}
}

// tailN returns the last n bytes of s.
func tailN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// fixPathsFromProposal extracts the file paths a fix proposal touches.
func fixPathsFromProposal(fp *schemas.FixProposal) []string {
	if fp == nil {
		return nil
	}
	var out []string
	for _, fc := range fp.FilesToChange {
		if fc.Path != "" {
			out = append(out, fc.Path)
		}
	}
	return out
}

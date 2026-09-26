package executors

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// BranchFormatter — pre-push formatting with the repo's OWN rules (v1.7.13).
//
// Run #27 lost a correct one-line Rust fix because the repo's CI ran
// `cargo fmt --check` and the fix's indentation differed. A human
// contributor formats before pushing; so does Sentinel now.
//
// Rules (deliberately conservative — "no rule, no formatting"):
//   - .go files        -> gofmt -w (universal Go standard)
//   - .rs files        -> cargo fmt (rustfmt IS the Rust default; it also
//                         picks up rustfmt.toml when the repo has one. When
//                         the base branch is fmt-clean — the normal case —
//                         whole-crate formatting only ever touches OUR
//                         changed files anyway.)
//   - .py files        -> black only when the repo configures black
//                         (pyproject [tool.black]); ruff format only when a
//                         ruff config exists.
//   - .js/.ts/...      -> prettier only when the repo configures prettier
//                         (.prettierrc* / prettier key in package.json).
//
// Only the FIX'S changed files are formatted (except cargo fmt, see above).
// The formatted result is committed to the same branch BEFORE the canary
// build and the repo-test run, so everything downstream verifies the exact
// PR content.
// ─────────────────────────────────────────────────────────────────────────────

// BranchFormatter formats changed files on a branch using the repo's own
// formatter configuration and pushes the result.
type BranchFormatter struct {
	clientset kubernetes.Interface
	namespace string
	gitHost   string
	initImage string
	timeout   time.Duration
}

// NewBranchFormatter creates a BranchFormatter.
func NewBranchFormatter(clientset kubernetes.Interface, namespace string) *BranchFormatter {
	return &BranchFormatter{
		clientset: clientset,
		namespace: namespace,
		gitHost:   "github.com",
		initImage: kanikoInitImageDefault,
		timeout:   5 * time.Minute,
	}
}

// WithGitHost sets the git host used in clone URLs.
func (f *BranchFormatter) WithGitHost(host string) *BranchFormatter {
	if host != "" {
		f.gitHost = host
	}
	return f
}

// WithInitImage sets the clone init-container image.
func (f *BranchFormatter) WithInitImage(image string) *BranchFormatter {
	if image != "" {
		f.initImage = image
	}
	return f
}

// WithTimeout sets the format pod timeout.
func (f *BranchFormatter) WithTimeout(d time.Duration) *BranchFormatter {
	if d > 0 {
		f.timeout = d
	}
	return f
}

// FormatResult reports what the formatter did.
type FormatResult struct {
	// Skipped: no applicable formatter for the changed files (e.g. only
	// .yaml files changed, or the repo configures no Python/JS formatter).
	Skipped bool
	// Reason explains the skip or the tool choice.
	Reason string
	// Committed: true when a formatting commit was pushed.
	Committed bool
}

// FormatPlan is the formatter decision for a set of changed files.
type FormatPlan struct {
	Tool    string   // "gofmt" | "cargo-fmt" | "black" | "ruff" | "prettier"
	Image   string   // builder image that has the tool
	Command string   // full shell command to run inside the clone
	Files   []string // the changed files the command targets
}

// PlanFormatting decides which formatter (if any) applies to the changed
// files, given the repo's config markers fetched from the base branch.
// Returns nil when nothing applies.
func PlanFormatting(changedFiles []string, configMarkers map[string]bool) *FormatPlan {
	if len(changedFiles) == 0 {
		return nil
	}

	var goFiles, rsFiles, pyFiles, jsFiles []string
	for _, p := range changedFiles {
		switch {
		case strings.HasSuffix(p, ".go"):
			goFiles = append(goFiles, p)
		case strings.HasSuffix(p, ".rs"):
			rsFiles = append(rsFiles, p)
		case strings.HasSuffix(p, ".py"):
			pyFiles = append(pyFiles, p)
		case hasAnySuffix(p, ".js", ".ts", ".tsx", ".jsx", ".mjs", ".cjs", ".json", ".css", ".scss", ".md"):
			jsFiles = append(jsFiles, p)
		}
	}

	// Go: gofmt is the universal standard — always applies.
	if len(goFiles) > 0 {
		return &FormatPlan{
			Tool:    "gofmt",
			Image:   "golang:1.23-bookworm",
			Command: "gofmt -w " + shellQuoteAll(goFiles),
			Files:   goFiles,
		}
	}
	// Rust: rustfmt is the Rust default — always applies (picks up
	// rustfmt.toml automatically when present).
	if len(rsFiles) > 0 {
		return &FormatPlan{
			Tool:    "cargo-fmt",
			Image:   "rust:1",
			Command: "cargo fmt",
			Files:   rsFiles,
		}
	}
	// Python: only with an explicit repo config.
	if len(pyFiles) > 0 {
		if configMarkers["ruff"] {
			return &FormatPlan{
				Tool:    "ruff",
				Image:   "python:3.11",
				Command: "pip install -q ruff && ruff format " + shellQuoteAll(pyFiles),
				Files:   pyFiles,
			}
		}
		if configMarkers["black"] {
			return &FormatPlan{
				Tool:    "black",
				Image:   "python:3.11",
				Command: "pip install -q black && black " + shellQuoteAll(pyFiles),
				Files:   pyFiles,
			}
		}
	}
	// JS/TS: only with an explicit prettier config.
	if len(jsFiles) > 0 && configMarkers["prettier"] {
		return &FormatPlan{
			Tool:    "prettier",
			Image:   "node:20",
			Command: "npx --yes prettier@3 --write " + shellQuoteAll(jsFiles),
			Files:   jsFiles,
		}
	}
	return nil
}

// FormatterConfigMarkers probes the repo's base branch for formatter
// configuration files. Returns a set of markers ("black", "ruff",
// "prettier") — empty set means "don't impose any formatter".
func FormatterConfigMarkers(ctx context.Context, fetcher RepoFileFetcher, repo, baseBranch string) map[string]bool {
	markers := map[string]bool{}
	if fetcher == nil || repo == "" {
		return markers
	}
	exists := func(path string) bool {
		_, err := fetcher.GetFileContentAt(ctx, repo, path, baseBranch)
		return err == nil
	}

	// Prettier: any .prettierrc* / prettier.config.* / package.json "prettier".
	for _, p := range []string{".prettierrc", ".prettierrc.json", ".prettierrc.js", ".prettierrc.cjs", ".prettierrc.yml", "prettier.config.js", "prettier.config.cjs"} {
		if exists(p) {
			markers["prettier"] = true
			break
		}
	}
	if !markers["prettier"] {
		if body, err := fetcher.GetFileContentAt(ctx, repo, "package.json", baseBranch); err == nil && strings.Contains(body, "\"prettier\"") {
			markers["prettier"] = true
		}
	}

	// Python: black / ruff via pyproject.toml sections or config files.
	if body, err := fetcher.GetFileContentAt(ctx, repo, "pyproject.toml", baseBranch); err == nil {
		if strings.Contains(body, "[tool.black]") {
			markers["black"] = true
		}
		if strings.Contains(body, "[tool.ruff") || strings.Contains(body, "\"ruff\"") {
			markers["ruff"] = true
		}
	}
	if exists("ruff.toml") || exists(".ruff.toml") {
		markers["ruff"] = true
	}

	return markers
}

// FormatChangedFiles formats the changed files on the branch with the
// repo's own formatter and pushes a formatting commit when anything
// changed. Best-effort: a formatter failure NEVER blocks the fix (the CI
// will surface it) — it logs and returns the failure reason.
func (f *BranchFormatter) FormatChangedFiles(ctx context.Context, fetcher RepoFileFetcher, repo, branch, baseBranch string, changedFiles []string) FormatResult {
	if len(changedFiles) == 0 {
		return FormatResult{Skipped: true, Reason: "no changed files"}
	}
	// Validate every path BEFORE it reaches a shell (same allowlist as
	// test/fix file paths).
	var safe []string
	for _, p := range changedFiles {
		if v := validateTestFilePath(p); v != "" && !strings.Contains(v, " ") {
			safe = append(safe, v)
		}
	}
	if len(safe) == 0 {
		return FormatResult{Skipped: true, Reason: "changed file paths rejected by safety check"}
	}

	markers := FormatterConfigMarkers(ctx, fetcher, repo, baseBranch)
	plan := PlanFormatting(safe, markers)
	if plan == nil {
		return FormatResult{Skipped: true, Reason: "no applicable formatter for the changed files (repo rule: no config, no formatting)"}
	}
	if err := validateRepoBranch(repo, branch); err != nil {
		return FormatResult{Skipped: true, Reason: fmt.Sprintf("repo/branch rejected: %v", err)}
	}

	logging.Info("formatter: formatting changed files before push",
		"repo", repo, "branch", branch, "tool", plan.Tool, "files", len(plan.Files))

	podName := fmt.Sprintf("formatter-%d", time.Now().Unix())
	// The git home (credentials + config) lives on a shared emptyDir so the
	// FORMATTING container — a different image — can push with the same
	// credentials the clone stored. The token never appears in logs.
	initScript := `set -e
export HOME=/git-home
if [ -n "$SENTINEL_GITHUB_TOKEN" ]; then
  git config --global credential.helper store
  printf '%s\n' "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@` + f.gitHost + `" > /git-home/.git-credentials
  chmod 600 /git-home/.git-credentials
fi
git clone --depth 1 --branch "$SENTINEL_BRANCH" "https://` + f.gitHost + `/$SENTINEL_REPO.git" /workspace/repo
echo "cloned $SENTINEL_REPO@$SENTINEL_BRANCH"`

	formatScript := `set -e
export HOME=/git-home
export GIT_AUTHOR_NAME="sentinel-bot"
export GIT_AUTHOR_EMAIL="sentinel@local"
export GIT_COMMITTER_NAME="sentinel-bot"
export GIT_COMMITTER_EMAIL="sentinel@local"
cd /workspace/repo
` + plan.Command + `
if git diff --quiet; then
  echo "FORMAT_CLEAN: no formatting changes"
  exit 0
fi
git add -A
git commit -m "style: apply ` + plan.Tool + ` (repo formatting rules) [skip ci]"
git push origin "HEAD:refs/heads/$SENTINEL_BRANCH"
echo "FORMAT_PUSHED: formatting commit pushed"`

	allowPE := false
	seccomp := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: f.namespace,
			Labels: map[string]string{
				"app":                 "formatter",
				"sentinel.io/managed": "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: seccomp,
			},
			InitContainers: []corev1.Container{
				{
					Name:       "clone",
					Image:      f.initImage,
					WorkingDir: "/workspace",
					Command:    []string{"/bin/sh", "-c"},
					Args:       []string{initScript},
					Env:        repoBranchEnv(repo, branch),
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPE,
						ReadOnlyRootFilesystem:   newBool(true),
						SeccompProfile:           seccomp,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "git-home", MountPath: "/git-home"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:       "format",
					Image:      plan.Image,
					Command:    []string{"/bin/sh", "-c"},
					Args:       []string{formatScript},
					WorkingDir: "/workspace/repo",
					Env:        repoBranchEnv(repo, branch),
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPE,
						SeccompProfile:           seccomp,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "git-home", MountPath: "/git-home"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "git-home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}

	if _, err := f.clientset.CoreV1().Pods(f.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return FormatResult{Skipped: true, Reason: fmt.Sprintf("format pod create failed: %v", err)}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.clientset.CoreV1().Pods(f.namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	phase, output := waitForPodPhase(f.clientset, ctx, f.namespace, podName, "format", f.timeout)
	switch {
	case phase != corev1.PodSucceeded:
		// Non-blocking by design: a formatter failure must not kill a fix.
		logging.Error("formatter: pod did not succeed (non-blocking)",
			"phase", string(phase), "output", lastN(output, 800))
		return FormatResult{Skipped: true, Reason: "formatter pod failed (non-blocking): " + firstOutputLine(output)}
	case strings.Contains(output, "FORMAT_PUSHED"):
		logging.Info("formatter: formatting commit pushed", "tool", plan.Tool, "files", len(plan.Files))
		return FormatResult{Committed: true, Reason: "formatted with " + plan.Tool + " and pushed"}
	default:
		logging.Info("formatter: already clean", "tool", plan.Tool)
		return FormatResult{Reason: "already formatted (" + plan.Tool + ")"}
	}
}

// hasAnySuffix reports whether p ends with any of the suffixes.
func hasAnySuffix(p string, suffixes ...string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

// shellQuoteAll quotes each path for safe shell interpolation. Paths were
// already validated (no metacharacters) by validateTestFilePath — the
// quoting is defense in depth.
func shellQuoteAll(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

// firstOutputLine returns the last meaningful line of output (errors sit at
// the end of formatter output).
func firstOutputLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no output)"
	}
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

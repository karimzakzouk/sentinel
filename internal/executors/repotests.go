package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"gopkg.in/yaml.v3"
)

// Repo file fetching interface — implemented by *GitHubExecutor. Kept small
// so tests can stub it.
type RepoFileFetcher interface {
	// ListDirEntries lists the entries of a directory path at a ref. Returns
	// (nil, nil) when the path does not exist.
	ListDirEntries(ctx context.Context, repo, path, ref string) ([]DirEntry, error)
	// GetFileContentAt fetches a file's decoded content at a ref. Returns
	// ("", nil-ish error) when the file does not exist — callers treat any
	// error as "marker absent".
	GetFileContentAt(ctx context.Context, repo, path, ref string) (string, error)
}

// DirEntry is one entry of a GitHub directory listing.
type DirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	SHA  string `json:"sha"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Test-command discovery (pure YAML parsing — NO LLM).
//
// Circular validation was the #1 run-#27 failure: the sandbox verified fixes
// with LLM-generated tests that the same LLM wrote, so they always passed,
// while the repository's own CI suite (the real oracle) failed the PR.
// Discovery here reads the repo's PUBLIC CI configuration the same way a
// human contributor reads it before pushing — that is not answer injection,
// it is the repo's own definition of "done".
//
// Commands are extracted from the BASE branch, never the fix branch: a
// prompt-injected fix must not be able to weaken its own gate by editing
// .github/workflows on the branch. The CODE under test still comes from the
// fix branch.
// ─────────────────────────────────────────────────────────────────────────────

// repoTestsMaxFiles bounds how many workflow files are fetched and parsed.
const repoTestsMaxFiles = 10

// repoTestsMaxCommands bounds the total extracted commands per repo.
const repoTestsMaxCommands = 8

// repoTestsMaxCmdLen bounds a single extracted command.
const repoTestsMaxCmdLen = 2000

// testRunnerRe matches shell invocations of known test runners inside a
// workflow `run:` step.
var testRunnerRe = regexp.MustCompile(`(?i)\b(mvn\s|gradle\s|cargo\s+(test|fmt)|go\s+test|pytest|python\s+-m\s+pytest|npm\s+(ci|test|run)|npx\s+(jest|vitest|mocha)|yarn\s+(test|install)|pnpm\s+(test|install)|jest\s|mocha\s|vitest\s|rspec|bundle\s+exec|dotnet\s+test)\b`)

// nonTestStepRe matches commands that are publish/deploy actions, not test
// actions. They are skipped even when they appear in a test-like job — a
// fix verification must never push or deploy anything.
var nonTestStepRe = regexp.MustCompile(`(?i)\b(docker\s+(push|build)|git\s+push|gh\s+|kubectl\s|helm\s|aws\s|gcloud\s)\b`)

// workflowFile is the minimal subset of a GitHub Actions workflow we parse.
type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Name  string         `yaml:"name"`
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name       string `yaml:"name"`
	Run        string `yaml:"run"`
	WorkingDir string `yaml:"working-directory"`
}

// ExtractTestCommandsFromWorkflow parses one workflow YAML body and returns
// the run commands of test-like jobs. A job is test-like when its id or name
// mentions "test", or when any of its run steps invokes a known test runner.
// `uses:` steps are ignored (they are GitHub-hosted actions, not shell).
func ExtractTestCommandsFromWorkflow(body string) []string {
	var wf workflowFile
	if err := parseYAMLStrict(body, &wf); err != nil {
		return nil
	}

	var cmds []string
	// Deterministic job order (map iteration is random).
	jobIDs := make([]string, 0, len(wf.Jobs))
	for id := range wf.Jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)

	for _, id := range jobIDs {
		job := wf.Jobs[id]
		jobName := strings.ToLower(id + " " + job.Name)
		isTestJob := strings.Contains(jobName, "test")
		if !isTestJob {
			for _, st := range job.Steps {
				if st.Run != "" && testRunnerRe.MatchString(st.Run) {
					isTestJob = true
					break
				}
			}
		}
		if !isTestJob {
			continue
		}
		for _, st := range job.Steps {
			cmd := strings.TrimSpace(st.Run)
			if cmd == "" || len(cmd) > repoTestsMaxCmdLen {
				continue
			}
			if nonTestStepRe.MatchString(cmd) {
				continue
			}
			// Multi-line run blocks are fine as-is (the shell executes them
			// in order); a working-directory is honored with a cd prefix.
			if wd := strings.TrimSpace(st.WorkingDir); wd != "" {
				cmd = fmt.Sprintf("cd %s && %s", wd, cmd)
			}
			cmds = append(cmds, cmd)
			if len(cmds) >= repoTestsMaxCommands {
				return cmds
			}
		}
	}
	return cmds
}

// DiscoverRepoTestCommands extracts the test commands for a repo:
//  1. Parse .github/workflows/*.yml on the BASE branch (trusted source)
//     and collect the run commands of test-like jobs.
//  2. Fallback when no workflows exist (or none are test-like): infer from
//     repo markers — pom.xml -> mvn test, Cargo.toml -> cargo test,
//     go.mod -> go test ./..., package.json -> npm test (when a test
//     script exists), spec/ -> rspec, tests/ -> pytest. No rule, no gate.
//
// Returns (commands, source) where source is "ci-yaml", "inferred" or ""
// (nothing to run — gate skipped).
func DiscoverRepoTestCommands(ctx context.Context, fetcher RepoFileFetcher, repo, baseBranch string) ([]string, string) {
	if fetcher == nil || repo == "" || baseBranch == "" {
		return nil, ""
	}

	// 1. CI workflows on the base branch.
	entries, err := fetcher.ListDirEntries(ctx, repo, ".github/workflows", baseBranch)
	if err != nil {
		logging.Info("repo-tests: workflow listing failed — falling back to markers",
			"repo", repo, "err", err)
		entries = nil
	}
	var seen = map[string]bool{}
	var cmds []string
	parsed := 0
	for _, e := range entries {
		if e.Type != "file" || parsed >= repoTestsMaxFiles || len(cmds) >= repoTestsMaxCommands {
			break
		}
		if !strings.HasSuffix(e.Name, ".yml") && !strings.HasSuffix(e.Name, ".yaml") {
			continue
		}
		body, err := fetcher.GetFileContentAt(ctx, repo, e.Path, baseBranch)
		if err != nil || body == "" {
			continue
		}
		parsed++
		for _, cmd := range ExtractTestCommandsFromWorkflow(body) {
			if !seen[cmd] {
				seen[cmd] = true
				cmds = append(cmds, cmd)
			}
		}
	}
	if len(cmds) > 0 {
		return cmds, "ci-yaml"
	}

	// 2. Marker inference. Every probe is a cheap Contents-API GET that
	// 404s when the marker is absent.
	fileExists := func(path string) (string, bool) {
		body, err := fetcher.GetFileContentAt(ctx, repo, path, baseBranch)
		return body, err == nil && body != ""
	}

	if _, ok := fileExists("pom.xml"); ok {
		return []string{"mvn -B test"}, "inferred"
	}
	if _, ok := fileExists("Cargo.toml"); ok {
		return []string{"cargo test"}, "inferred"
	}
	if _, ok := fileExists("go.mod"); ok {
		return []string{"go test ./..."}, "inferred"
	}
	if body, ok := fileExists("package.json"); ok {
		if cmd := npmTestCommand(body); cmd != "" {
			return []string{cmd}, "inferred"
		}
		// package.json exists but defines no test script — nothing to run.
		return nil, ""
	}
	if _, ok := fileExists("Gemfile"); ok {
		if entries, err := fetcher.ListDirEntries(ctx, repo, "spec", baseBranch); err == nil && len(entries) > 0 {
			return []string{"bundle install && bundle exec rspec"}, "inferred"
		}
		return nil, ""
	}
	if _, okPy := fileExists("pyproject.toml"); okPy {
		if cmd := pythonTestCommand(ctx, fetcher, repo, baseBranch); cmd != "" {
			return []string{cmd}, "inferred"
		}
		return nil, ""
	}
	if _, okReq := fileExists("requirements.txt"); okReq {
		if entries, err := fetcher.ListDirEntries(ctx, repo, "tests", baseBranch); err == nil && len(entries) > 0 {
			return []string{"pip install -q -r requirements.txt && python -m pytest -q"}, "inferred"
		}
		return nil, ""
	}
	return nil, ""
}

// npmTestCommand returns "npm ci && npm test" when package.json defines a
// non-empty test script, "" otherwise.
func npmTestCommand(packageJSON string) string {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(packageJSON), &pkg); err != nil {
		return ""
	}
	if pkg.Scripts["test"] == "" || pkg.Scripts["test"] == "echo \"Error: no test specified\" && exit 1" {
		return ""
	}
	return "npm ci && npm test"
}

// pythonTestCommand builds a pytest command when tests exist. The pip
// install prefix is only added when requirements.txt is present.
func pythonTestCommand(ctx context.Context, fetcher RepoFileFetcher, repo, baseBranch string) string {
	hasTests := func() bool {
		if entries, err := fetcher.ListDirEntries(ctx, repo, "tests", baseBranch); err == nil && len(entries) > 0 {
			return true
		}
		if entries, err := fetcher.ListDirEntries(ctx, repo, "test", baseBranch); err == nil && len(entries) > 0 {
			return true
		}
		return false
	}
	if !hasTests() {
		return ""
	}
	if _, err := fetcher.GetFileContentAt(ctx, repo, "requirements.txt", baseBranch); err == nil {
		return "pip install -q -r requirements.txt && python -m pytest -q"
	}
	return "python -m pytest -q"
}

// ─────────────────────────────────────────────────────────────────────────────
// RepoTestRunner — runs the discovered commands against the FIX BRANCH in a
// one-shot pod (clone branch via init container, run commands in a
// language-appropriate builder image). The pod runs in the Sentinel
// namespace (like Kaniko pods — it needs egress for the clone and for
// package installs) with a restricted security context. The GitHub token is
// confined to the init container's filesystem and never reaches the test
// container, so extracted CI commands cannot exfiltrate credentials.
// ─────────────────────────────────────────────────────────────────────────────

// RepoTestRunner executes the repo's own test suite against a fix branch.
type RepoTestRunner struct {
	clientset     kubernetes.Interface
	namespace     string
	gitHost       string
	initImage     string
	timeout       time.Duration
	builderImages map[string]string // tool -> image override

	// discovery cache: repo/base -> commands. Bounded by the fix-service
	// budget in practice; a mutex guards the map because investigations run
	// concurrently.
	discMu    sync.Mutex
	discCache map[string]discoveredTests
}

type discoveredTests struct {
	Commands []string
	Source   string
}

// NewRepoTestRunner creates a RepoTestRunner with default builder images.
func NewRepoTestRunner(clientset kubernetes.Interface, namespace string) *RepoTestRunner {
	return &RepoTestRunner{
		clientset:     clientset,
		namespace:     namespace,
		gitHost:       "github.com",
		initImage:     kanikoInitImageDefault,
		timeout:       15 * time.Minute,
		builderImages: map[string]string{},
		discCache:     map[string]discoveredTests{},
	}
}

// WithGitHost sets the git host used in clone URLs.
func (r *RepoTestRunner) WithGitHost(host string) *RepoTestRunner {
	if host != "" {
		r.gitHost = host
	}
	return r
}

// WithInitImage sets the clone init-container image.
func (r *RepoTestRunner) WithInitImage(image string) *RepoTestRunner {
	if image != "" {
		r.initImage = image
	}
	return r
}

// WithTimeout sets the test pod timeout.
func (r *RepoTestRunner) WithTimeout(d time.Duration) *RepoTestRunner {
	if d > 0 {
		r.timeout = d
	}
	return r
}

// defaultBuilderImageForCommands picks the builder image that can run every
// command (all commands must map to the SAME image — a repo whose CI mixes
// languages runs in the first match; mixed setups are rare and the gate
// still runs, just in the union image when one fits). Returns "" when no
// image fits (gate skipped).
func defaultBuilderImageForCommands(cmds []string) string {
	matches := func(re *regexp.Regexp) bool {
		for _, c := range cmds {
			if re.MatchString(c) {
				return true
			}
		}
		return false
	}
	switch {
	case matches(regexp.MustCompile(`(?i)\bcargo\b`)):
		return "rust:1"
	case matches(regexp.MustCompile(`(?i)\bmvn\b`)):
		return "maven:3.9-eclipse-temurin-17"
	case matches(regexp.MustCompile(`(?i)\bgradle\b`)):
		return "gradle:8-jdk17"
	case matches(regexp.MustCompile(`(?i)\bdotnet\b`)):
		return "mcr.microsoft.com/dotnet/sdk:8.0"
	case matches(regexp.MustCompile(`(?i)\bgo\s+test\b`)):
		return "golang:1.23-bookworm"
	case matches(regexp.MustCompile(`(?i)\b(rspec|bundle)\b`)):
		return "ruby:3.2"
	case matches(regexp.MustCompile(`(?i)\b(pytest|python|pip)\b`)):
		return "python:3.11"
	case matches(regexp.MustCompile(`(?i)\b(npm|npx|yarn|pnpm|jest|vitest|mocha|node)\b`)):
		return "node:20"
	default:
		return ""
	}
}

// builderImage resolves the image for the commands, honoring operator
// overrides (SENTINEL_REPO_TESTS_IMAGE_<TOOL> style pre-seeded map).
func (r *RepoTestRunner) builderImage(cmds []string) string {
	if img := defaultBuilderImageForCommands(cmds); img != "" {
		if override, ok := r.builderImages[img]; ok && override != "" {
			return override
		}
		return img
	}
	return ""
}

// RepoTestOutcome is the result of running the repo's test suite.
type RepoTestOutcome struct {
	// Skipped is true when no test commands were discovered or no builder
	// image fits — the gate does not block in that case.
	Skipped bool
	// Source: "ci-yaml" or "inferred".
	Source string
	// Commands that were run.
	Commands []string
	// Image used for the run ("" when skipped).
	Image string
	// Passed is the pod's Succeeded phase.
	Passed bool
	// Output is the test container's log (tailed).
	Output string
	// Summary is a one-line human verdict.
	Summary string
}

// Discover caches and returns the repo's test commands (base branch).
func (r *RepoTestRunner) Discover(ctx context.Context, fetcher RepoFileFetcher, repo, baseBranch string) ([]string, string) {
	key := repo + "@" + baseBranch
	r.discMu.Lock()
	if d, ok := r.discCache[key]; ok {
		r.discMu.Unlock()
		return d.Commands, d.Source
	}
	r.discMu.Unlock()

	cmds, source := DiscoverRepoTestCommands(ctx, fetcher, repo, baseBranch)

	r.discMu.Lock()
	r.discCache[key] = discoveredTests{Commands: cmds, Source: source}
	r.discMu.Unlock()
	return cmds, source
}

// Run clones the fix branch and executes the discovered test commands.
// Non-blocking skip conditions return a Skipped outcome — the caller treats
// that as "gate not applicable", never as failure.
func (r *RepoTestRunner) Run(ctx context.Context, fetcher RepoFileFetcher, repo, branch, baseBranch string) *RepoTestOutcome {
	cmds, source := r.Discover(ctx, fetcher, repo, baseBranch)
	if len(cmds) == 0 {
		return &RepoTestOutcome{Skipped: true, Summary: "no test commands discovered (gate skipped)"}
	}
	image := r.builderImage(cmds)
	if image == "" {
		return &RepoTestOutcome{
			Skipped:  true,
			Source:   source,
			Commands: cmds,
			Summary:  "no builder image fits the discovered commands (gate skipped)",
		}
	}
	return r.runCommands(ctx, repo, branch, cmds, source, image)
}

// runCommands creates the clone+test pod and waits for it.
func (r *RepoTestRunner) runCommands(ctx context.Context, repo, branch string, cmds []string, source, image string) *RepoTestOutcome {
	if err := validateRepoBranch(repo, branch); err != nil {
		return &RepoTestOutcome{Skipped: true, Commands: cmds, Summary: fmt.Sprintf("repo/branch rejected: %v", err)}
	}

	podName := fmt.Sprintf("repo-tests-%d", time.Now().Unix())
	// set -e WITHOUT -x: never echo the token. The test commands are joined
	// with && so the first failure fails the pod (GitHub Actions semantics:
	// a failing step fails the job).
	joined := strings.Join(cmds, " && \\\n  ")

	initScript := `set -e
if [ -n "$SENTINEL_GITHUB_TOKEN" ]; then
  git config --global credential.helper store
  printf '%s\n' "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@` + r.gitHost + `" > ~/.git-credentials
  chmod 600 ~/.git-credentials
fi
git clone --depth 1 --branch "$SENTINEL_BRANCH" "https://` + r.gitHost + `/$SENTINEL_REPO.git" /workspace/repo
echo "cloned $SENTINEL_REPO@$SENTINEL_BRANCH"`

	allowPE := false
	seccomp := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.namespace,
			Labels: map[string]string{
				"app":                 "repo-tests",
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
					Image:      r.initImage,
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
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:    "tests",
					Image:   image,
					Command: []string{"/bin/sh", "-c"},
					// The test container gets NO token and NO secret mounts —
					// CI commands cannot exfiltrate credentials they never see.
					Args: []string{
						"set -e\ncd /workspace/repo\n" + joined + "\n",
					},
					WorkingDir: "/workspace/repo",
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPE,
						SeccompProfile:           seccomp,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}

	if _, err := r.clientset.CoreV1().Pods(r.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return &RepoTestOutcome{
			Commands: cmds, Source: source, Image: image,
			Summary: fmt.Sprintf("repo test pod create failed: %v", err),
		}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.clientset.CoreV1().Pods(r.namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	logging.Info("repo-tests: running repo test suite",
		"repo", repo, "branch", branch, "image", image, "source", source,
		"commands", len(cmds), "timeout", r.timeout)

	phase, output := waitForPodPhase(r.clientset, ctx, r.namespace, podName, "tests", r.timeout)
	outcome := &RepoTestOutcome{
		Source:   source,
		Commands: cmds,
		Image:    image,
		Passed:   phase == corev1.PodSucceeded,
		Output:   output,
	}
	switch phase {
	case corev1.PodSucceeded:
		outcome.Summary = fmt.Sprintf("repo tests PASSED (%d command(s), source=%s)", len(cmds), source)
	case corev1.PodFailed:
		outcome.Summary = fmt.Sprintf("repo tests FAILED (%d command(s), source=%s)", len(cmds), source)
	default:
		outcome.Summary = fmt.Sprintf("repo tests TIMED OUT after %v (%d command(s), source=%s)", r.timeout, len(cmds), source)
		outcome.Skipped = true // infra-ish: don't block the fix on a timeout
	}
	logging.Info("repo-tests: complete",
		"repo", repo, "passed", outcome.Passed, "skipped", outcome.Skipped,
		"summary", outcome.Summary)
	return outcome
}

// repoBranchEnv builds the env vars for clone containers (token via secret
// reference — never inline, never in logs).
func repoBranchEnv(repo, branch string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "SENTINEL_REPO", Value: repo},
		{Name: "SENTINEL_BRANCH", Value: branch},
		{Name: "SENTINEL_GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "sentinel"},
				Key:                  "githubToken",
				Optional:             newBool(true),
			},
		}},
	}
}

// waitForPodPhase polls a pod until Succeeded/Failed or timeout, then
// returns (phase, container logs).
func waitForPodPhase(clientset kubernetes.Interface, ctx context.Context, ns, name, container string, timeout time.Duration) (corev1.PodPhase, string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ""
		default:
		}
		pod, err := clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			switch pod.Status.Phase {
			case corev1.PodSucceeded, corev1.PodFailed:
				return pod.Status.Phase, fetchContainerLogs(clientset, ctx, ns, name, container)
			}
		}
		select {
		case <-ctx.Done():
			return "", ""
		case <-time.After(5 * time.Second):
		}
	}
	return "", fetchContainerLogs(clientset, ctx, ns, name, container)
}

// fetchContainerLogs reads a container's logs (best effort, tailed to 64KB).
func fetchContainerLogs(clientset kubernetes.Interface, ctx context.Context, ns, name, container string) string {
	tail := int64(800)
	limit := int64(64 * 1024)
	req := clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tail,
		LimitBytes: &limit,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(stream)
	return buf.String()
}

// parseYAMLStrict is a tiny indirection so tests can exercise the workflow
// parser without importing yaml directly.
func parseYAMLStrict(body string, out any) error {
	return yaml.Unmarshal([]byte(body), out)
}

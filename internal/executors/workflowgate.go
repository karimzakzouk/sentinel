package executors

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/errs"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// WorkflowTestGate is the deterministic pre-push CI gate (v1.7.13 Fix #1).
//
// Problem it solves (eval run #27 root cause #1): the sandbox verified fixes
// with LLM-GENERATED tests, but the repository's REAL CI test commands —
// living in .github/workflows — were never executed before the PR was
// opened. Fixes that break `go vet`, `tsc --noEmit`, `ruff` or the actual
// test suite therefore sailed through verification and produced red PRs.
//
// The gate is 100% deterministic (no LLM in the loop):
//  1. List .github/workflows/*.yml on the fix branch (Contents API).
//  2. Parse each workflow: keep only pull_request-triggered workflows
//     (the PR gate — push-only deploy workflows are excluded), ubuntu
//     jobs without matrix/services.
//  3. Extract the run: steps verbatim (continue-on-error and
//     working-directory respected; if:/${{ }} steps skipped).
//  4. Map setup actions to a toolchain image (node:20, python:3.12,
//     golang:1.22, maven:3.9-eclipse-temurin-21, rust:<pinned>,
//     ruby:3.3; cmake jobs get an apt bootstrap).
//  5. Clone the fix branch and run the assembled script in a pod in the
//     Sentinel namespace (same security posture as the Kaniko build pod).
//  6. A failing step sets the "workflow_tests_failed" summary marker,
//     which executeCodeFix maps to Level-6 feedback for the fix-proposer
//     retry loop — the PR is never opened with known-red CI.
//
// Infra failures (API errors, pod scheduling, image pulls, timeouts) are
// fail-open: the gate notes it and the normal sandbox flow proceeds.
// Machines for gates, LLM for judgment.
type WorkflowTestGate struct {
	clientset    kubernetes.Interface
	namespace    string
	gh           *GitHubExecutor
	gitHost      string
	initImage    string
	enabled      bool
	timeout      time.Duration
	maxWorkflows int
	maxSteps     int
}

// NewWorkflowTestGate creates the gate. Pods run in the Sentinel namespace
// (NOT the sandbox namespace) for the same reason the Kaniko builder does:
// test runs need egress (go mod download, npm ci, pip install, Maven
// Central) which the sandbox namespaces' default-deny NetworkPolicy would
// block.
func NewWorkflowTestGate(clientset kubernetes.Interface, namespace string) *WorkflowTestGate {
	return &WorkflowTestGate{
		clientset:    clientset,
		namespace:    namespace,
		initImage:    kanikoInitImageDefault,
		enabled:      true,
		timeout:      15 * time.Minute,
		maxWorkflows: 3,
		maxSteps:     30,
	}
}

// WithGitHub wires the GitHub executor used to list and fetch workflow
// files from the fix branch.
func (g *WorkflowTestGate) WithGitHub(gh *GitHubExecutor) *WorkflowTestGate {
	g.gh = gh
	return g
}

// WithGitHost sets the git host templated into the clone URL (trusted
// config, mirroring KanikoBuilder).
func (g *WorkflowTestGate) WithGitHost(host string) *WorkflowTestGate {
	if host != "" {
		g.gitHost = host
	}
	return g
}

// WithInitImage overrides the clone init-container image.
func (g *WorkflowTestGate) WithInitImage(image string) *WorkflowTestGate {
	if image != "" {
		g.initImage = image
	}
	return g
}

// WithTimeout sets the per-workflow run timeout.
func (g *WorkflowTestGate) WithTimeout(d time.Duration) *WorkflowTestGate {
	if d > 0 {
		g.timeout = d
	}
	return g
}

// WorkflowTestResult is the outcome of one gate run. Summary always
// carries a machine-detectable marker: workflow_tests_failed (code
// failure — retryable), workflow_tests_infra_error (fail-open),
// workflow_tests_skipped (nothing to run) or a pass description.
type WorkflowTestResult struct {
	Ran        bool // a test pod actually ran
	Passed     bool
	InfraError bool   // gate could not run — caller fails open
	Summary    string // marker + one-line detail
	Output     string // failing step output (for LLM feedback), tail-capped
	Workflows  []string
	StepsRun   int
}

// ---------------------------------------------------------------------------
// Workflow YAML parsing (deterministic)
// ---------------------------------------------------------------------------

// gateStep is one runnable step extracted from a workflow job.
type gateStep struct {
	Name            string
	Run             string
	ContinueOnError bool
	WorkingDir      string
}

// gatePlan is one job from one workflow, flattened into a runnable script
// description.
type gatePlan struct {
	Workflow           string // file name, e.g. "ci.yml"
	Job                string // job id, e.g. "test"
	Image              string // toolchain container image
	Bootstrap          string // toolchain bootstrap commands (deps for the test tools)
	Env                map[string]string
	Steps              []gateStep
	declaredComponents string   // rust components from with: (sanitized)
	Notes              []string // skipped steps / jobs and why (surfaced in the PR body)
}

// wfRoot / wfJob / wfStep mirror the subset of the GitHub Actions workflow
// schema the gate understands. yaml.Node is used for fields with flexible
// types (`on:`, `runs-on:`, `with:`, `continue-on-error:`) so quoted and
// unquoted scalars both decode (go-version: 1.22 is a YAML float; its
// .Value is still the literal "1.22").
type wfRoot struct {
	Name string            `yaml:"name"`
	On   yaml.Node         `yaml:"on"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]wfJob  `yaml:"jobs"`
}

type wfJob struct {
	Name      string            `yaml:"name"`
	RunsOn    yaml.Node         `yaml:"runs-on"`
	If        string            `yaml:"if"`
	Services  yaml.Node         `yaml:"services"`
	Strategy  yaml.Node         `yaml:"strategy"`
	Container yaml.Node         `yaml:"container"`
	Env       map[string]string `yaml:"env"`
	Steps     []wfStep          `yaml:"steps"`
}

type wfStep struct {
	Name            string               `yaml:"name"`
	Uses            string               `yaml:"uses"`
	With            map[string]yaml.Node `yaml:"with"`
	Run             string               `yaml:"run"`
	If              string               `yaml:"if"`
	WorkingDir      string               `yaml:"working-directory"`
	ContinueOnError yaml.Node            `yaml:"continue-on-error"`
}

// hasPullRequestTrigger reports whether the `on:` node fires on
// pull_request events — the only trigger that gates PRs. Push-only
// workflows (deploy.yml) are intentionally excluded: they run after
// merge, not on the PR, and typically push images rather than run tests.
func hasPullRequestTrigger(on *yaml.Node) bool {
	if on == nil || on.Kind == 0 {
		return false
	}
	switch on.Kind {
	case yaml.DocumentNode:
		if len(on.Content) == 1 {
			return hasPullRequestTrigger(on.Content[0])
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if on.Content[i].Value == "pull_request" {
				return true
			}
		}
	case yaml.SequenceNode:
		for _, item := range on.Content {
			if item.Value == "pull_request" {
				return true
			}
		}
	case yaml.ScalarNode:
		return on.Value == "pull_request"
	}
	return false
}

// isUbuntuRunner accepts ubuntu-* runners (string or self-hosted-style
// sequence). Other runners (windows/macos) are skipped: no image parity.
func isUbuntuRunner(runsOn *yaml.Node) bool {
	if runsOn == nil || runsOn.Kind == 0 {
		return false
	}
	switch runsOn.Kind {
	case yaml.DocumentNode:
		if len(runsOn.Content) == 1 {
			return isUbuntuRunner(runsOn.Content[0])
		}
	case yaml.ScalarNode:
		return strings.HasPrefix(runsOn.Value, "ubuntu-")
	case yaml.SequenceNode:
		for _, item := range runsOn.Content {
			if strings.HasPrefix(item.Value, "ubuntu-") {
				return true
			}
		}
	}
	return false
}

// nodeBool decodes a scalar node that may be a bool, a "true"/"false"
// string, or an unevaluable expression (treated as false).
func nodeBool(n yaml.Node) bool {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return false
	}
	return n.Value == "true"
}

// nodeText returns the literal text of a scalar node ("1.22" stays "1.22"
// whether or not the workflow author quoted it).
func nodeText(n yaml.Node) string {
	if n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// nodePresent reports whether an optional yaml.Node field was set to a
// non-null value (Kind 0 = absent; Tag !!null = explicit null).
func nodePresent(n *yaml.Node) bool {
	return n != nil && n.Kind != 0 && n.Tag != "!!null"
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)*`)

// sanitizeVersion extracts the leading dotted-number version from a setup
// action input ("20.x" -> "20", ">=3.12" -> "3.12", "stable" -> fallback).
// The result is digits and dots only — safe for an image tag.
func sanitizeVersion(v, fallback string) string {
	if m := versionRe.FindString(v); m != "" {
		return m
	}
	return fallback
}

// toolchainFromSetup maps a setup action to (toolchain, container image).
// Versions are taken from the action's `with:` inputs so the gate runs the
// same major toolchain the repo's CI uses.
func toolchainFromSetup(action string, with map[string]yaml.Node) (tool, image string, ok bool) {
	withVal := func(key string) string {
		if n, exists := with[key]; exists {
			return nodeText(n)
		}
		return ""
	}
	switch action {
	case "actions/setup-node":
		return "node", "node:" + sanitizeVersion(withVal("node-version"), "20"), true
	case "actions/setup-python":
		return "python", "python:" + sanitizeVersion(withVal("python-version"), "3.12"), true
	case "actions/setup-go":
		return "go", "golang:" + sanitizeVersion(withVal("go-version"), "1.22"), true
	case "actions/setup-java":
		v := sanitizeVersion(withVal("java-version"), "21")
		return "java", "maven:3.9-eclipse-temurin-" + v, true
	case "dtolnay/rust-toolchain":
		return "rust", "rust:" + sanitizeVersion(withVal("toolchain"), "1"), true
	case "ruby/setup-ruby":
		return "ruby", "ruby:" + sanitizeVersion(withVal("ruby-version"), "3.3"), true
	}
	return "", "", false
}

// inferToolchainFromCommands falls back to command sniffing when the job
// declares no setup action.
func inferToolchainFromCommands(cmds string) (tool, image string) {
	c := cmds
	switch {
	case strings.Contains(c, "npm ") || strings.Contains(c, "npx "):
		return "node", "node:20"
	case strings.Contains(c, "pytest") || strings.Contains(c, "pip "):
		return "python", "python:3.12"
	case strings.Contains(c, "mvn "):
		return "java", "maven:3.9-eclipse-temurin-21"
	case strings.Contains(c, "cargo ") || strings.Contains(c, "rustc"):
		return "rust", "rust:1"
	case strings.Contains(c, "bundle ") || strings.Contains(c, "rspec"):
		return "ruby", "ruby:3.3"
	case strings.Contains(c, "cmake") || strings.Contains(c, "ctest"):
		return "cpp", "ubuntu:24.04"
	case strings.Contains(c, "go "):
		return "go", "golang:1.22"
	}
	return "ubuntu", "ubuntu:24.04"
}

var componentRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// bootstrapFor returns toolchain-specific preparation commands run before
// the workflow steps. Best-effort where marked — a bootstrap that cannot
// run must not fail the gate by itself; the steps themselves are the
// oracle.
func bootstrapFor(tool string, plan *gatePlan) string {
	switch tool {
	case "rust":
		// Official rust images ship rustup but not always the clippy /
		// rustfmt components. Use the workflow's declared components when
		// present, else the standard pair. Best-effort.
		components := "rustfmt clippy"
		if plan.declaredComponents != "" {
			components = plan.declaredComponents
		}
		return "rustup component add " + components + " 2>/dev/null || true"
	case "ruby":
		// ruby/setup-ruby with bundler-cache: true runs bundle install
		// implicitly; replicate that when steps use bundler.
		for _, s := range plan.Steps {
			if strings.Contains(s.Run, "bundle ") {
				return "if [ -f Gemfile ]; then bundle install --jobs 4; else echo \"workflow-gate: no Gemfile, skipping bundle install\"; fi"
			}
		}
	case "cpp":
		// cmake/ninja/g++ are not in the base ubuntu image.
		return "apt-get update -qq && apt-get install -y -qq --no-install-recommends cmake ninja-build g++"
	}
	return ""
}

// parseWorkflow parses one workflow file into zero or more runnable plans
// plus human-readable notes about what was skipped and why. Malformed
// files are skipped with a note rather than failing the gate (GitHub
// itself would reject them). maxSteps caps extracted steps per job.
func parseWorkflow(fileName, content string, maxSteps int) (plans []gatePlan, notes []string) {
	var root wfRoot
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, []string{fmt.Sprintf("%s: unparseable YAML (%v) — skipped", fileName, err)}
	}
	if !hasPullRequestTrigger(&root.On) {
		return nil, []string{fmt.Sprintf("%s: no pull_request trigger — skipped (push/deploy-only workflows do not gate PRs)", fileName)}
	}

	jobIDs := make([]string, 0, len(root.Jobs))
	for id := range root.Jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)

	for _, jobID := range jobIDs {
		job := root.Jobs[jobID]
		jobLabel := jobID
		if job.Name != "" {
			jobLabel = job.Name
		}
		switch {
		case !isUbuntuRunner(&job.RunsOn):
			notes = append(notes, fmt.Sprintf("%s#%s: non-ubuntu runner — skipped", fileName, jobLabel))
			continue
		case nodePresent(&job.Services):
			notes = append(notes, fmt.Sprintf("%s#%s: job declares service containers — skipped (v1 limitation)", fileName, jobLabel))
			continue
		case nodePresent(&job.Strategy):
			notes = append(notes, fmt.Sprintf("%s#%s: matrix strategy — skipped (v1 limitation)", fileName, jobLabel))
			continue
		case nodePresent(&job.Container):
			notes = append(notes, fmt.Sprintf("%s#%s: job-level container — skipped (v1 limitation)", fileName, jobLabel))
			continue
		case job.If != "":
			notes = append(notes, fmt.Sprintf("%s#%s: conditional job (if:) — skipped", fileName, jobLabel))
			continue
		}

		plan := gatePlan{
			Workflow: fileName,
			Job:      jobID,
			Env:      map[string]string{},
		}
		// Workflow-level env first; job-level wins.
		for k, v := range root.Env {
			plan.Env[k] = v
		}
		for k, v := range job.Env {
			plan.Env[k] = v
		}

		var runBlob strings.Builder
		tool, image, haveTool := "", "", false
		for _, step := range job.Steps {
			if step.Uses != "" {
				action := step.Uses
				if i := strings.Index(action, "@"); i >= 0 {
					action = action[:i]
				}
				if t, img, ok := toolchainFromSetup(action, step.With); ok && !haveTool {
					tool, image, haveTool = t, img, true
					if action == "dtolnay/rust-toolchain" {
						if n, exists := step.With["components"]; exists {
							raw := nodeText(n)
							var keep []string
							for _, c := range strings.Split(raw, ",") {
								c = strings.TrimSpace(c)
								if componentRe.MatchString(c) {
									keep = append(keep, c)
								}
							}
							if len(keep) > 0 {
								plan.declaredComponents = strings.Join(keep, " ")
							}
						}
					}
				}
				continue
			}
			if step.Run == "" {
				continue
			}
			if step.If != "" {
				notes = append(notes, fmt.Sprintf("%s#%s step %q: conditional (if:) — skipped", fileName, jobLabel, step.Name))
				continue
			}
			if strings.Contains(step.Run, "${{") {
				notes = append(notes, fmt.Sprintf("%s#%s step %q: uses ${{ }} expressions — skipped", fileName, jobLabel, step.Name))
				continue
			}
			if len(plan.Steps) >= maxSteps {
				notes = append(notes, fmt.Sprintf("%s#%s: step cap (%d) reached — truncating", fileName, jobLabel, maxSteps))
				break
			}
			name := step.Name
			if name == "" {
				name = firstLine(step.Run)
				if len(name) > 60 {
					name = name[:60]
				}
			}
			plan.Steps = append(plan.Steps, gateStep{
				Name:            name,
				Run:             step.Run,
				ContinueOnError: nodeBool(step.ContinueOnError),
				WorkingDir:      step.WorkingDir,
			})
			runBlob.WriteString(step.Run)
			runBlob.WriteString("\n")
		}

		if len(plan.Steps) == 0 {
			notes = append(notes, fmt.Sprintf("%s#%s: no runnable steps", fileName, jobLabel))
			continue
		}
		if !haveTool {
			tool, image = inferToolchainFromCommands(runBlob.String())
			notes = append(notes, fmt.Sprintf("%s#%s: no setup action — inferred %s image from commands", fileName, jobLabel, image))
		}
		plan.Image = image
		plan.Bootstrap = bootstrapFor(tool, &plan)
		plan.Notes = notes
		plans = append(plans, plan)
	}
	return plans, notes
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "(empty)"
}

// ---------------------------------------------------------------------------
// Script assembly
// ---------------------------------------------------------------------------

var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	markerSanRe = regexp.MustCompile(`[$"\x60\\]`)
)

// shellSingleQuote wraps s in single quotes, escaping embedded ones.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sanitizeMarker makes a step name safe inside a double-quoted echo:
// strips $, ", backtick and backslash so nothing expands or breaks.
func sanitizeMarker(s string) string {
	return markerSanRe.ReplaceAllString(s, "")
}

// buildGateScript assembles the POSIX sh script the pod runs. Steps run
// sequentially under set -e; each is wrapped in a subshell so cd does not
// leak between steps. continue-on-error steps tolerate failure.
func buildGateScript(plan gatePlan) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -e\n")
	b.WriteString("cd /workspace/repo\n")
	if plan.Bootstrap != "" {
		b.WriteString("# toolchain bootstrap\n")
		b.WriteString(plan.Bootstrap + "\n")
	}
	// Literal env only — values with ${{ }} cannot be evaluated.
	envKeys := make([]string, 0, len(plan.Env))
	for k := range plan.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		v := plan.Env[k]
		if !envKeyRe.MatchString(k) || strings.Contains(v, "${{") {
			continue
		}
		b.WriteString("export " + k + "=" + shellSingleQuote(v) + "\n")
	}
	for i, step := range plan.Steps {
		b.WriteString(fmt.Sprintf("echo \"=== GATE STEP %d/%d: %s ===\"\n",
			i+1, len(plan.Steps), sanitizeMarker(step.Name)))
		body := step.Run
		if step.WorkingDir != "" {
			body = "cd " + shellSingleQuote(step.WorkingDir) + "\n" + body
		}
		if step.ContinueOnError {
			b.WriteString("( " + body + " ) || echo \"workflow-gate: step tolerated failure (continue-on-error): " +
				sanitizeMarker(step.Name) + "\"\n")
		} else {
			b.WriteString("( " + body + " )\n")
		}
	}
	b.WriteString("echo \"workflow-gate: ALL STEPS COMPLETED\"\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Pod execution
// ---------------------------------------------------------------------------

// gatePodResult is the raw outcome of one plan's pod.
type gatePodResult struct {
	passed     bool
	infraError bool
	summary    string
	output     string
}

// runPlan creates the script ConfigMap + pod, waits for completion, and
// extracts the failing step from the logs.
func (g *WorkflowTestGate) runPlan(ctx context.Context, repo, branch string, plan gatePlan) gatePodResult {
	script := buildGateScript(plan)
	runID := gateRunID(branch)
	cmName := fmt.Sprintf("workflow-gate-script-%s", runID)
	podName := fmt.Sprintf("workflow-gate-%s", runID)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: g.namespace,
			Labels: map[string]string{
				"app":                 "workflow-gate",
				"sentinel.io/managed": "true",
			},
		},
		Data: map[string]string{"run.sh": script},
	}
	if _, err := g.clientset.CoreV1().ConfigMaps(g.namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return gatePodResult{infraError: true,
			summary: fmt.Sprintf("workflow_tests_infra_error: create configmap: %v", err)}
	}

	pod := g.buildPodSpec(podName, repo, branch, plan.Image, cmName)
	if _, err := g.clientset.CoreV1().Pods(g.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		g.cleanup(context.Background(), cmName, podName)
		return gatePodResult{infraError: true,
			summary: fmt.Sprintf("workflow_tests_infra_error: create pod: %v", err)}
	}

	logging.Info("workflow gate: running repo CI commands",
		"repo", repo, "branch", branch, "workflow", plan.Workflow,
		"job", plan.Job, "image", plan.Image, "steps", len(plan.Steps),
		"timeout", g.timeout.String())

	outcome := g.waitForGatePod(ctx, podName, g.timeout)
	logs, _ := g.getPodLogs(ctx, podName)
	g.cleanup(context.Background(), cmName, podName)

	switch outcome {
	case gatePodSucceeded:
		logging.Info("workflow gate: PASSED", "workflow", plan.Workflow, "job", plan.Job)
		return gatePodResult{passed: true,
			summary: fmt.Sprintf("passed %s#%s (%d steps)", plan.Workflow, plan.Job, len(plan.Steps)),
			output:  lastN(logs, 4000)}
	case gatePodTimeout:
		return gatePodResult{infraError: true,
			summary: fmt.Sprintf("workflow_tests_infra_error: timeout after %s running %s#%s (test suite hung or environment too slow — treated as infra, fail-open)",
				g.timeout.String(), plan.Workflow, plan.Job),
			output: lastN(logs, 4000)}
	default: // gatePodFailed
		stepName, stepCmd, output := extractGateFailure(logs, plan)
		logging.Info("workflow gate: FAILED", "workflow", plan.Workflow, "job", plan.Job,
			"step", stepName, "cmd", stepCmd)
		return gatePodResult{
			passed: false,
			summary: fmt.Sprintf("workflow_tests_failed: %s#%s step %q (%s)",
				plan.Workflow, plan.Job, stepName, stepCmd),
			output: output,
		}
	}
}

// gateRunID derives a short DNS-safe id from the branch name plus random
// suffix (branch "sentinel/user-api-ab12cd34" -> "user-api-ab12cd34-a1b2c3").
func gateRunID(branch string) string {
	base := branch
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	id := b.String()
	if len(id) > 24 {
		id = id[len(id)-24:]
	}
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return id + "-x"
	}
	return id + "-" + hex.EncodeToString(buf)
}

// buildPodSpec builds the gate pod: an alpine/git init container clones the
// fix branch (same credential handling as the Kaniko build pod — token via
// env-from-secret, never interpolated into the script), then the toolchain
// container runs the assembled script from the ConfigMap.
func (g *WorkflowTestGate) buildPodSpec(podName, repo, branch, image, configmapName string) *corev1.Pod {
	// SECURITY: mirrors KanikoBuilder.buildPodSpec — set -e WITHOUT -x so
	// the GitHub PAT is never printed to pod logs. Repo/branch arrive as
	// env vars; the git host is trusted operator config.
	initScript := `set -e
if [ -n "$SENTINEL_GITHUB_TOKEN" ]; then
  git config --global credential.helper store
  printf '%s\n' "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@` + g.gitHost + `" > ~/.git-credentials
  chmod 600 ~/.git-credentials
fi
git clone --depth 1 --branch "$SENTINEL_BRANCH" "https://` + g.gitHost + `/$SENTINEL_REPO.git" /workspace/repo
ls -la /workspace/repo/`

	allowPrivilegeEscalation := false
	seccompProfile := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: g.namespace,
			Labels: map[string]string{
				"app":                 "workflow-gate",
				"sentinel.io/managed": "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: newInt64(int64(g.timeout.Seconds()) + 30),
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: seccompProfile,
			},
			InitContainers: []corev1.Container{
				{
					Name:       "clone",
					Image:      g.initImage,
					WorkingDir: "/workspace",
					Command:    []string{"/bin/sh", "-c"},
					Args:       []string{initScript},
					Env: []corev1.EnvVar{
						{Name: "SENTINEL_REPO", Value: repo},
						{Name: "SENTINEL_BRANCH", Value: branch},
						{Name: "SENTINEL_GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "sentinel"},
								Key:                  "githubToken",
								Optional:             newBool(true),
							},
						}},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						ReadOnlyRootFilesystem:   newBool(true),
						SeccompProfile:           seccompProfile,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:       "run-tests",
					Image:      image,
					Command:    []string{"/bin/sh", "-c"},
					Args:       []string{"/gate/run.sh"},
					WorkingDir: "/workspace",
					// Writable root filesystem on purpose: test suites,
					// compilers and package managers write to disk (unlike
					// the kaniko executor). Privilege escalation stays off.
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						SeccompProfile:           seccompProfile,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "gate-script", MountPath: "/gate", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "gate-script", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configmapName},
					Items:                []corev1.KeyToPath{{Key: "run.sh", Path: "run.sh"}},
				}}},
			},
		},
	}
}

type gatePodOutcome int

const (
	gatePodSucceeded gatePodOutcome = iota
	gatePodFailed
	gatePodTimeout
)

// waitForGatePod polls the pod until Succeeded/Failed or the timeout
// elapses (distinguishing pod-level DeadlineExceeded).
func (g *WorkflowTestGate) waitForGatePod(ctx context.Context, podName string, timeout time.Duration) gatePodOutcome {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return gatePodTimeout
		default:
		}
		pod, err := g.clientset.CoreV1().Pods(g.namespace).Get(ctx, podName, metav1.GetOptions{})
		if err == nil {
			switch pod.Status.Phase {
			case corev1.PodSucceeded:
				return gatePodSucceeded
			case corev1.PodFailed:
				if pod.Status.Reason == "DeadlineExceeded" {
					return gatePodTimeout
				}
				return gatePodFailed
			}
		}
		select {
		case <-ctx.Done():
			return gatePodTimeout
		case <-time.After(5 * time.Second):
		}
	}
	return gatePodTimeout
}

// getPodLogs fetches the run-tests container logs.
func (g *WorkflowTestGate) getPodLogs(ctx context.Context, podName string) (string, error) {
	req := g.clientset.CoreV1().Pods(g.namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "run-tests"})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(stream); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// cleanup deletes the gate ConfigMap and Pod with a fresh context so it
// survives cancellation of the parent ctx.
func (g *WorkflowTestGate) cleanup(ctx context.Context, configmapName, podName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = g.clientset.CoreV1().ConfigMaps(g.namespace).Delete(cleanupCtx, configmapName, metav1.DeleteOptions{})
	_ = g.clientset.CoreV1().Pods(g.namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	_ = ctx
}

// gateStepMarkerRe matches the script's own step banners. The step number
// identifies which plan step was running when the script died.
var gateStepMarkerRe = regexp.MustCompile(`=== GATE STEP (\d+)/(\d+): (.*) ===`)

// extractGateFailure locates the failing step from the pod logs (the last
// banner printed before the non-zero exit) and returns its name, first
// command line, and the output from that banner onward (tail-capped for
// the LLM feedback budget).
func extractGateFailure(logs string, plan gatePlan) (stepName, stepCmd, output string) {
	stepName, stepCmd = "unknown", ""
	output = lastN(logs, 8000)

	lastIdx := -1
	var lastName string
	for _, m := range gateStepMarkerRe.FindAllStringSubmatch(logs, -1) {
		lastIdx++
		lastName = m[3]
	}
	if lastIdx >= 0 && lastIdx < len(plan.Steps) {
		stepName = lastName
		stepCmd = firstLine(plan.Steps[lastIdx].Run)
	}

	// Cut the output to start at the last banner so the feedback opens
	// with the failing step, not pages of earlier output.
	if idx := strings.LastIndex(logs, "=== GATE STEP"); idx >= 0 {
		output = lastN(logs[idx:], 8000)
	}
	return stepName, stepCmd, output
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// Run executes the gate against a fix branch. It never returns an error —
// every outcome (including infra problems) is expressed in the result so
// callers can decide fail-open vs fail-closed from config.
func (g *WorkflowTestGate) Run(ctx context.Context, repo, branch string) *WorkflowTestResult {
	res := &WorkflowTestResult{}

	if err := validateRepoBranch(repo, branch); err != nil {
		res.Summary = fmt.Sprintf("workflow_tests_infra_error: %v", err)
		res.InfraError = true
		return res
	}

	files, err := g.gh.ListDirEntries(ctx, repo, ".github/workflows", branch)
	if err != nil {
		if errs.IsNotFound(err) {
			res.Summary = "workflow_tests_skipped: no .github/workflows directory on branch (repo has no CI — gate is a no-op)"
			return res
		}
		res.Summary = fmt.Sprintf("workflow_tests_infra_error: list workflows: %v", err)
		res.InfraError = true
		return res
	}

	var ymls []string
	for _, f := range files {
		if strings.HasSuffix(f, ".yml") || strings.HasSuffix(f, ".yaml") {
			ymls = append(ymls, f)
		}
	}
	sort.Strings(ymls)
	if len(ymls) == 0 {
		res.Summary = "workflow_tests_skipped: .github/workflows contains no .yml/.yaml files"
		return res
	}
	if len(ymls) > g.maxWorkflows {
		ymls = ymls[:g.maxWorkflows]
	}

	var plans []gatePlan
	var notes []string
	for _, f := range ymls {
		content, err := g.gh.GetFileContent(ctx, repo, ".github/workflows/"+f, branch)
		if err != nil {
			res.Summary = fmt.Sprintf("workflow_tests_infra_error: fetch %s: %v", f, err)
			res.InfraError = true
			return res
		}
		p, n := parseWorkflow(f, content, g.maxSteps)
		plans = append(plans, p...)
		notes = append(notes, n...)
	}

	if len(plans) == 0 {
		detail := strings.Join(notes, "; ")
		if len(detail) > 400 {
			detail = detail[:400]
		}
		res.Summary = "workflow_tests_skipped: no runnable pull_request test jobs (" + detail + ")"
		return res
	}

	res.Workflows = make([]string, 0, len(plans))
	steps := 0
	for i := range plans {
		plan := &plans[i]
		res.Workflows = append(res.Workflows, plan.Workflow+"#"+plan.Job)
		steps += len(plan.Steps)

		pr := g.runPlan(ctx, repo, branch, *plan)
		if pr.infraError {
			res.Summary = pr.summary
			res.Output = pr.output
			res.InfraError = true
			return res
		}
		if !pr.passed {
			res.Ran = true
			res.Summary = pr.summary + " — the same command will fail GitHub Actions on this PR"
			res.Output = pr.output
			res.StepsRun = steps
			return res
		}
	}

	res.Ran = true
	res.Passed = true
	res.StepsRun = steps
	res.Summary = fmt.Sprintf("passed %s (%d steps)", strings.Join(res.Workflows, ", "), steps)
	return res
}

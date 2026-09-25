package executors

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repoci"
)

// RepoTestRunner runs a repository's OWN CI commands (tests + formatters)
// against a branch, inside a pod with a stock language toolchain. The pod
// clones the branch (same init-container pattern as Kaniko), applies the
// repo's format commands, runs its test commands, and reads back the
// (formatted) contents of the fix files so the branch can be updated.
//
// This is the pre-PR oracle introduced after run #27: the sandbox verifies
// against LLM-GENERATED tests (circular), while CI verifies against the
// repo's own suite (real). Running the real suite before the PR moves CI
// failures into the fix-proposer retry loop where the LLM can still act
// on them.
type RepoTestRunner struct {
	clientset   kubernetes.Interface
	namespace   string
	initImage   string
	gitHost     string
	testTimeout time.Duration
}

// NewRepoTestRunner creates a RepoTestRunner. It runs pods in the
// Sentinel namespace (same as Kaniko pods).
func NewRepoTestRunner(clientset kubernetes.Interface, namespace string) *RepoTestRunner {
	return &RepoTestRunner{
		clientset:   clientset,
		namespace:   namespace,
		initImage:   kanikoInitImageDefault,
		gitHost:     "github.com",
		testTimeout: 10 * time.Minute,
	}
}

// WithInitImage overrides the git-clone init image.
func (r *RepoTestRunner) WithInitImage(image string) *RepoTestRunner {
	if image != "" {
		r.initImage = image
	}
	return r
}

// WithGitHost overrides the git host for clone URLs.
func (r *RepoTestRunner) WithGitHost(host string) *RepoTestRunner {
	if host != "" {
		r.gitHost = host
	}
	return r
}

// WithTimeout overrides the overall pod timeout.
func (r *RepoTestRunner) WithTimeout(d time.Duration) *RepoTestRunner {
	if d > 0 {
		r.testTimeout = d
	}
	return r
}

// CmdResult is the outcome of one command the pod ran.
type CmdResult struct {
	Phase  string // "fmt" or "test"
	Cmd    string
	Exit   int
	Output string
}

// RunResult is the outcome of a full repo-checks run.
type RunResult struct {
	Commands    []CmdResult
	Readback    map[string]string // path -> formatted content (fmt phase)
	FailedTests []string          // failing test ids extracted from outputs
	TestsRan    bool
}

// HasTestFailures reports whether any test command exited non-zero.
func (rr *RunResult) HasTestFailures() bool {
	for _, c := range rr.Commands {
		if c.Phase == "test" && c.Exit != 0 {
			return true
		}
	}
	return false
}

// Summary renders a compact one-line summary for logs and PR bodies.
func (rr *RunResult) Summary() string {
	if rr == nil {
		return "not run"
	}
	pass, fail := 0, 0
	for _, c := range rr.Commands {
		if c.Phase != "test" {
			continue
		}
		if c.Exit == 0 {
			pass++
		} else {
			fail++
		}
	}
	if !rr.TestsRan {
		return "no test commands"
	}
	return fmt.Sprintf("%d passed / %d failed", pass, fail)
}

// RunOpts controls one run.
type RunOpts struct {
	// FmtEnabled runs the format commands and reads back files.
	FmtEnabled bool
	// ReadbackPaths are the files whose (formatted) content is returned.
	ReadbackPaths []string
}

// Run clones the branch, runs fmt + test commands, reads back files.
// Never returns an error for command failures — those are data. Only
// infrastructure failures (pod creation, timeout, clone failure) error.
func (r *RepoTestRunner) Run(ctx context.Context, repo, branch string, ci *repoci.Discovered, opts RunOpts) (*RunResult, error) {
	if ci == nil || ci.ToolchainImage == "" {
		return nil, fmt.Errorf("no toolchain image for repo checks")
	}
	if err := validateRepoBranch(repo, branch); err != nil {
		return nil, err
	}

	// Validate readback paths (they come from LLM fix proposals).
	var readback []string
	for _, p := range opts.ReadbackPaths {
		if v := validateTestFilePath(p); v != "" {
			readback = append(readback, v)
		}
	}

	// Build the command ConfigMap — one file per command avoids all
	// shell-quoting issues.
	cmData := map[string]string{}
	cmdKeys := []string{}
	addCmd := func(phase string, idx int, cmd string) string {
		key := fmt.Sprintf("%s-%d", phase, idx)
		cmData[key] = cmd + "\n"
		cmdKeys = append(cmdKeys, key)
		return key
	}
	fmtKeys := []string{}
	if opts.FmtEnabled {
		for i, cmd := range ci.FmtCmds {
			fmtKeys = append(fmtKeys, addCmd("fmt", i, cmd))
		}
	}
	testKeys := []string{}
	for i, cmd := range ci.TestCmds {
		testKeys = append(testKeys, addCmd("test", i, cmd))
	}
	if len(cmData) == 0 {
		return &RunResult{Readback: map[string]string{}}, nil
	}

	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	cmName := fmt.Sprintf("repo-cmds-%s", runID)
	podName := fmt.Sprintf("repo-checks-%s", runID)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: r.namespace,
			Labels:    map[string]string{"sentinel.io/managed": "true"},
		},
		Data: cmData,
	}
	if _, err := r.clientset.CoreV1().ConfigMaps(r.namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create repo-cmds configmap: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().ConfigMaps(r.namespace).Delete(context.Background(), cmName, metav1.DeleteOptions{})
	}()

	pod := r.buildPodSpec(podName, repo, branch, cmName, ci, fmtKeys, testKeys, readback)
	if _, err := r.clientset.CoreV1().Pods(r.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create repo-checks pod: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().Pods(r.namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	}()

	logging.Info("repotests: running repo CI commands",
		"repo", repo, "branch", branch, "language", ci.Language,
		"fmt_cmds", len(fmtKeys), "test_cmds", len(testKeys),
		"timeout", r.testTimeout)

	output, err := r.waitForCompletion(ctx, podName, r.testTimeout)
	if err != nil {
		logging.Error("repotests: pod failed", "repo", repo, "branch", branch, "err", err,
			"output_tail", lastN(output, 500))
		return nil, fmt.Errorf("repo checks pod: %w", err)
	}

	result := parseRunOutput(output, ci)
	logging.Info("repotests: repo CI commands complete",
		"repo", repo, "branch", branch, "summary", result.Summary(),
		"failed_tests", strings.Join(result.FailedTests, ", "),
		"fmt_files_changed", len(result.Readback))
	return result, nil
}

// buildPodSpec builds the repo-checks pod: init container clones the
// branch, main container (toolchain image) installs deps, runs fmt
// commands, reads back files, runs test commands.
func (r *RepoTestRunner) buildPodSpec(podName, repo, branch, cmName string, ci *repoci.Discovered, fmtKeys, testKeys, readback []string) *corev1.Pod {
	initScript := `set -e
if [ -n "$SENTINEL_GITHUB_TOKEN" ]; then
  git config --global credential.helper store
  printf '%s\n' "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@` + r.gitHost + `" > ~/.git-credentials
  chmod 600 ~/.git-credentials
fi
git clone --depth 1 --branch "$SENTINEL_BRANCH" "https://` + r.gitHost + `/$SENTINEL_REPO.git" /workspace/repo
cd /workspace/repo
git config --global --add safe.directory /workspace/repo
echo cloned`

	// Main script: deps -> fmt -> readback -> tests. All output is
	// structured with markers parsed by parseRunOutput.
	var b strings.Builder
	b.WriteString("set +e\ncd /workspace/repo\n")

	// Language-specific dependency install (best-effort).
	switch ci.Language {
	case "node":
		b.WriteString(`if [ -f package.json ] && [ ! -d node_modules ]; then
  npm ci --no-audit --no-fund >/dev/null 2>&1 || npm install --no-audit --no-fund >/dev/null 2>&1
fi
`)
	case "ruby":
		b.WriteString(`if [ -f Gemfile ]; then
  bundle install --quiet >/dev/null 2>&1 || bundle install >/dev/null 2>&1
fi
`)
	case "python":
		b.WriteString(`if [ -f requirements.txt ]; then
  pip install -q -r requirements.txt >/dev/null 2>&1
fi
`)
	}

	// Fmt phase.
	for _, key := range fmtKeys {
		fmt.Fprintf(&b, "echo '===SENTINEL_CMD_START key=%s phase=fmt==='\n", key)
		fmt.Fprintf(&b, "timeout 180 sh /cmds/%s 2>&1\n", key)
		fmt.Fprintf(&b, "echo \"===SENTINEL_CMD_END key=%s exit=$?===\"\n", key)
	}

	// Readback of fix files (post-format content).
	if len(fmtKeys) > 0 && len(readback) > 0 {
		b.WriteString("echo '===SENTINEL_READBACK_START==='\n")
		for _, p := range readback {
			fmt.Fprintf(&b, "if [ -f '%s' ]; then echo \"===SENTINEL_FILE_START path=%s===\"; base64 -w0 '%s'; echo; echo '===SENTINEL_FILE_END==='; fi\n", p, p, p)
		}
		b.WriteString("echo '===SENTINEL_READBACK_END==='\n")
	}

	// Test phase.
	for _, key := range testKeys {
		fmt.Fprintf(&b, "echo '===SENTINEL_CMD_START key=%s phase=test==='\n", key)
		fmt.Fprintf(&b, "timeout 540 sh /cmds/%s 2>&1\n", key)
		fmt.Fprintf(&b, "echo \"===SENTINEL_CMD_END key=%s exit=$?===\"\n", key)
	}
	b.WriteString("echo '===SENTINEL_DONE==='\n")

	allowPrivilegeEscalation := false
	seccompProfile := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.namespace,
			Labels: map[string]string{
				"app":                 "repo-checks",
				"sentinel.io/managed": "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: seccompProfile,
			},
			InitContainers: []corev1.Container{
				{
					Name:       "clone",
					Image:      r.initImage,
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
						SeccompProfile:           seccompProfile,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "runner",
					Image:           ci.ToolchainImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/bin/sh", "-c"},
					Args:            []string{b.String()},
					WorkingDir:      "/workspace/repo",
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						SeccompProfile:           seccompProfile,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "cmds", MountPath: "/cmds", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "cmds", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				}}},
			},
		},
	}
}

// waitForCompletion polls the pod and returns the runner container's logs.
func (r *RepoTestRunner) waitForCompletion(ctx context.Context, podName string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		pod, err := r.clientset.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
		if err == nil {
			switch pod.Status.Phase {
			case corev1.PodSucceeded, corev1.PodFailed:
				req := r.clientset.CoreV1().Pods(r.namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "runner"})
				stream, err := req.Stream(ctx)
				if err != nil {
					return "", fmt.Errorf("fetch repo-checks logs: %w", err)
				}
				defer stream.Close()
				buf := new(bytes.Buffer)
				_, _ = buf.ReadFrom(stream)
				return buf.String(), nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return "", fmt.Errorf("repo-checks pod timed out after %v", timeout)
}

// parseRunOutput parses the marker-structured pod output into a RunResult.
// Markers (emitted by buildPodSpec's script):
//
//	echo '===SENTINEL_CMD_START key=fmt-0 phase=fmt==='
//	<command output>
//	echo "===SENTINEL_CMD_END key=fmt-0 exit=0==="
//	echo "===SENTINEL_FILE_START path=src/x.rs==="
//	<base64 -w0 content>
//	echo '===SENTINEL_FILE_END==='
func parseRunOutput(output string, ci *repoci.Discovered) *RunResult {
	res := &RunResult{Readback: map[string]string{}}
	cmdByKey := map[string]string{}
	for i, c := range ci.FmtCmds {
		cmdByKey[fmt.Sprintf("fmt-%d", i)] = c
	}
	for i, c := range ci.TestCmds {
		cmdByKey[fmt.Sprintf("test-%d", i)] = c
	}

	// markerField extracts the value of `name=` from a marker line:
	// "===SENTINEL_CMD_START key=fmt-0 phase=fmt===" -> ("key") "fmt-0".
	// The value ends at the next space or the trailing "===".
	markerField := func(line, name string) string {
		idx := strings.Index(line, name+"=")
		if idx < 0 {
			return ""
		}
		rest := line[idx+len(name)+1:]
		if j := strings.IndexByte(rest, ' '); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimSuffix(rest, "===")
		return strings.TrimSpace(rest)
	}

	lines := strings.Split(output, "\n")
	var curKey, curPhase string
	var curOutput []string
	flush := func(exit int) {
		if curKey == "" {
			return
		}
		out := strings.Join(curOutput, "\n")
		if len(out) > 8000 {
			out = out[:8000] + "\n... (truncated)"
		}
		res.Commands = append(res.Commands, CmdResult{
			Phase: curPhase, Cmd: cmdByKey[curKey], Exit: exit, Output: out,
		})
		if curPhase == "test" {
			res.TestsRan = true
			if exit != 0 {
				res.FailedTests = append(res.FailedTests, repoci.ExtractFailedTests(out)...)
			}
		}
		curKey, curPhase, curOutput = "", "", nil
	}

	inReadback := false
	var fileBytes []string
	var filePath string

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "===SENTINEL_CMD_START"):
			curKey = markerField(line, "key")
			if strings.Contains(line, "phase=fmt") {
				curPhase = "fmt"
			} else {
				curPhase = "test"
			}
			curOutput = nil
		case strings.HasPrefix(line, "===SENTINEL_CMD_END"):
			exit := 0
			if v := markerField(line, "exit"); v != "" {
				fmt.Sscanf(v, "%d", &exit)
			}
			flush(exit)
		case strings.HasPrefix(line, "===SENTINEL_READBACK_START"):
			inReadback = true
		case strings.HasPrefix(line, "===SENTINEL_READBACK_END"):
			inReadback = false
		case strings.HasPrefix(line, "===SENTINEL_FILE_START"):
			filePath = markerField(line, "path")
			fileBytes = nil
		case strings.HasPrefix(line, "===SENTINEL_FILE_END"):
			if filePath != "" {
				if decoded, err := base64.StdEncoding.DecodeString(strings.Join(fileBytes, "")); err == nil {
					res.Readback[filePath] = string(decoded)
				}
			}
			filePath, fileBytes = "", nil
		default:
			if inReadback && filePath != "" {
				fileBytes = append(fileBytes, strings.TrimSpace(line))
			} else if curKey != "" {
				curOutput = append(curOutput, line)
			}
		}
	}

	res.FailedTests = dedupeStrings(res.FailedTests)
	if len(res.FailedTests) > 20 {
		res.FailedTests = res.FailedTests[:20]
	}
	return res
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// CompareRepoTests compares a fix-branch run against the base-branch run
// and returns (newFailures, preexistingFailures). A failure is NEW when
// the failing test (or, when test names can't be extracted, the failing
// command) passed on base but fails on the fix branch — i.e. the fix
// broke something that worked. Failures present on BOTH branches are
// pre-existing and not attributable to the fix.
func CompareRepoTests(fix, base *RunResult) (newFailures, preexisting []string) {
	if fix == nil {
		return nil, nil
	}
	if base == nil {
		// No base reference — treat every failure as pre-existing
		// (conservative: don't block on unattributable failures).
		return nil, extractFailureIDs(fix)
	}

	baseFailed := map[string]bool{}
	for _, id := range extractFailureIDs(base) {
		baseFailed[id] = true
	}
	baseCmdPassed := map[string]bool{}
	for _, c := range base.Commands {
		if c.Phase == "test" {
			baseCmdPassed[c.Cmd] = c.Exit == 0
		}
	}

	var namedNew, namedPre []string
	for _, c := range fix.Commands {
		if c.Phase != "test" || c.Exit == 0 {
			continue
		}
		tests := repoci.ExtractFailedTests(c.Output)
		if len(tests) == 0 {
			// No test names — compare at command level.
			if passed, ok := baseCmdPassed[c.Cmd]; ok && passed {
				namedNew = append(namedNew, "command: "+c.Cmd)
			} else {
				namedPre = append(namedPre, "command: "+c.Cmd)
			}
			continue
		}
		for _, t := range tests {
			if baseFailed[t] {
				namedPre = append(namedPre, t)
			} else {
				namedNew = append(namedNew, t)
			}
		}
	}
	return dedupeStrings(namedNew), dedupeStrings(namedPre)
}

// extractFailureIDs returns all failing test ids in a run.
func extractFailureIDs(r *RunResult) []string {
	var out []string
	for _, c := range r.Commands {
		if c.Phase == "test" && c.Exit != 0 {
			tests := repoci.ExtractFailedTests(c.Output)
			if len(tests) == 0 {
				out = append(out, "command: "+c.Cmd)
			} else {
				out = append(out, tests...)
			}
		}
	}
	return dedupeStrings(out)
}

// SortReadbackPaths returns the readback paths in deterministic order.
func SortReadbackPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

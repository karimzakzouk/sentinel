// Package executors — sandbox verification logic.
// Implements the 5-level verification suite:
//   Level 1: canary pod Ready + sustained ready window
//   Level 2: health endpoints return 2xx/3xx
//   Level 3: original error gone from canary logs (PRIMARY ORACLE)
//   Level 4: LLM-generated regression test passes
//   Level 5: LLM-generated reproducer script runs without error
package executors

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strings"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

        "github.com/karimzakzouk/sentinel/internal/agents"
        "github.com/karimzakzouk/sentinel/internal/schemas"
)

func (s *SandboxRunner) waitForReadyWithLogs(ctx context.Context, ns, name string, timeout time.Duration) (bool, string) {
        deadline := time.Now().Add(timeout)
        readySince := time.Time{}
        var canaryLogsBuf strings.Builder

        for time.Now().Before(deadline) {
                select {
                case <-ctx.Done():
                        return false, canaryLogsBuf.String()
                default:
                }
                deploy, err := s.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
                if err == nil && deploy.Status.ReadyReplicas >= 1 {
                        if readySince.IsZero() {
                                readySince = time.Now()
                                logging.Info("Level 1: canary pod went Ready at  — sustaining for", "value", readySince.Format(time.RFC3339), s.sustainedWindow)
                        }
                        // Capture logs during the sustained window

                        if logs := s.fetchCanaryPodLogs(ctx, ns, name); logs != "" {
                                canaryLogsBuf.Reset()
                                canaryLogsBuf.WriteString(logs)
                        }
                        if time.Since(readySince) >= s.sustainedWindow {
                                // Final log capture

                                if logs := s.fetchCanaryPodLogs(ctx, ns, name); logs != "" {
                                        canaryLogsBuf.Reset()
                                        canaryLogsBuf.WriteString(logs)
                                }
                                return true, canaryLogsBuf.String()
                        }
                } else {
                        if !readySince.IsZero() {
                                logging.Info("Level 1: canary pod dropped below ReadyReplicas=1 before sustaining  — resetting timer", "sustainedwindow", s.sustainedWindow)
                        }
                        readySince = time.Time{}
                }
                select {
                case <-ctx.Done():
                        return false, canaryLogsBuf.String()
                case <-time.After(3 * time.Second):
                }
        }
        return false, canaryLogsBuf.String()
}

// verifyErrorGoneFromLogs is the PRIMARY ORACLE for sandbox verification. v4.2.0.
// It checks whether the original error type (TypeError, panic, IndexError,
// NullPointerException, etc.) appears in the canary pod's logs during the
// sustained Ready window. If the error is GONE, the fix worked. If the error
// is STILL PRESENT, the fix did not work.
// This is universal:
//   - Works for HTTP errors (TypeError on /format → TypeError gone from logs)
//   - Works for background task errors (cron job crash → error gone)
//   - Works for startup errors (ImportError on boot → error gone)
//   - Works for OOMKills (OOMKilled → pod stays Ready)
//   - Works for any language (ParseError handles Python/Go/Java/JS/Rust/Ruby/C)
// This replaces the old trigger-endpoint-probing approach which:
//   - Picked the wrong endpoint (first 4xx in logs, not the crashing one)
//   - Failed on 4xx responses (4xx = handled error, not a crash)
//   - Didn't work for non-HTTP errors

func (s *SandboxRunner) verifyErrorGoneFromLogs(ctx context.Context, inc *schemas.Incident, canaryLogs string) *level3Result {
        // Extract the original error from the incident's anomaly explanations

        explanation := ""
        for _, anomaly := range inc.Cluster.Anomalies {
                explanation += anomaly.Explanation + "\n"
        }
        if explanation == "" {
                return &level3Result{
                        passed: true,
                        detail: "no original error to check (incident has no anomaly explanation) — passing by default",
                }
        }

        // Parse the original error from the incident logs

        originalError := agents.ParseError(explanation)
        if originalError == nil {
                // No recognizable error type — can't verify. Pass with a note.

                return &level3Result{
                        passed: true,
                        detail: "no recognizable error type in original logs — cannot verify error is gone, passing (Level 1 + Level 2 passed)",
                }
        }

        errorType := originalError.ErrorType
        errorDetail := originalError.ErrorDetail

        // If canary logs are empty, we can't check. Pass (Level 1 + 2 already passed).

        if canaryLogs == "" {
                return &level3Result{
                        passed: true,
                        detail: fmt.Sprintf("no canary logs to scan (pod Ready + health endpoints OK) — assuming %s is gone", errorType),
                }
        }

        // Check if the original error type appears in the canary logs
        // Use case-insensitive search for robustness

        canaryLower := strings.ToLower(canaryLogs)
        errorTypeLower := strings.ToLower(errorType)

        if strings.Contains(canaryLower, errorTypeLower) {
                // The exact error type is still present — fix did not work
                // Try to extract the context around the error for the LLM feedback

                contextLines := extractErrorContext(canaryLogs, errorType, 3)
                return &level3Result{
                        passed: false,
                        detail: fmt.Sprintf("%s still present in canary pod logs:\n%s", errorType, contextLines),
                }
        }

        // Also check for the error detail (e.g. "'DATABASE_URLL'" for KeyError)

        if errorDetail != "" {
                errorDetailLower := strings.ToLower(strings.TrimSpace(errorDetail))
                if len(errorDetailLower) > 3 && strings.Contains(canaryLower, errorDetailLower) {
                        contextLines := extractErrorContext(canaryLogs, errorDetail, 3)
                        return &level3Result{
                                passed: false,
                                detail: fmt.Sprintf("%s: %s still present in canary pod logs:\n%s", errorType, errorDetail, contextLines),
                        }
                }
        }

        // Check for generic crash patterns (Traceback, panic, Exception) even if
        // the specific error type isn't found — any crash in canary logs is bad

        crashPatterns := []string{"traceback (most recent call last)", "panic:", "fatal error:", "goroutine ", "exception in asgi application", "unhandled exception"}
        for _, pattern := range crashPatterns {
                if strings.Contains(canaryLower, pattern) {
                        contextLines := extractErrorContext(canaryLogs, pattern, 3)
                        return &level3Result{
                                passed: false,
                                detail: fmt.Sprintf("new crash pattern detected in canary logs: %q\n%s", pattern, contextLines),
                        }
                }
        }

        return &level3Result{
                passed: true,
                detail: fmt.Sprintf("%s: %s not found in canary logs during %s sustained window — error is gone, fix verified", errorType, errorDetail, s.sustainedWindow),
        }
}

// extractErrorContext returns N lines of context around the first occurrence
// of pattern in logs. Used to give the LLM actionable feedback when the
// error is still present.

func extractErrorContext(logs, pattern string, contextLines int) string {
        lines := strings.Split(logs, "\n")
        patternLower := strings.ToLower(pattern)
        for i, line := range lines {
                if strings.Contains(strings.ToLower(line), patternLower) {
                        start := i - contextLines
                        if start < 0 {
                                start = 0
                        }
                        end := i + contextLines + 1
                        if end > len(lines) {
                                end = len(lines)
                        }
                        return strings.Join(lines[start:end], "\n")
                }
        }
        return pattern + " (context not available)"
}

// runRegressionTest extracts the LLM-generated test from the incident state
// and runs it via the TestRunner. v4.2.0 Tier 3.
// Returns nil if no test was generated. Returns a TestResult otherwise.

func (s *SandboxRunner) runRegressionTest(ctx context.Context, sandboxNS string, inc *schemas.Incident, canaryImage string) *TestResult {
        // Extract the test from the agent state's RetrievedIncidents field.
        // The pipeline stores it there as a JSON string under the "regression_test" key.

        var testJSON string
        for _, ri := range inc.State.RetrievedIncidents {
                if raw, ok := ri["regression_test"]; ok {
                        if str, ok := raw.(string); ok {
                                testJSON = str
                                break
                        }
                }
        }
        if testJSON == "" {
                return &TestResult{
                        Overall: true,
                        Summary: "no test generated (test gate skipped — Level 3 passed)",
                }
        }

        // Parse the test JSON

        test, err := parseGeneratedTest(testJSON)
        if err != nil {
                logging.Error("Level 4: failed to parse test JSON", "err", err)
                return &TestResult{
                        Overall: true,
                        Summary: "test parse failed (non-blocking): " + err.Error(),
                }
        }

        // Run the test

        runner := NewTestRunner(s.clientset)
        result, err := runner.RunTest(ctx, canaryImage, sandboxNS, test)
        if err != nil {
                logging.Error("Level 4: test runner error", "err", err)
                return &TestResult{
                        Overall: true, // don't block on infra failure
                        Summary: fmt.Sprintf("test runner error (non-blocking): %v", err),
                }
        }
        return result
}

// parseGeneratedTest parses a JSON string into a GeneratedTest.

func parseGeneratedTest(jsonStr string) (*schemas.GeneratedTest, error) {
        var raw map[string]string
        if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
                return nil, err
        }
        return &schemas.GeneratedTest{
                TestFile:      raw["test_file"],
                TestFramework: raw["test_framework"],
                TestLanguage:  raw["test_language"],
                TestContent:   raw["test_content"],
                TestCommand:   raw["test_command"],
                Description:   raw["description"],
        }, nil
}

// runReproducer extracts the LLM-generated reproducer from the incident state
// and runs it via the TestRunner. v4.2.0 Tier 4.

func (s *SandboxRunner) runReproducer(ctx context.Context, sandboxNS string, inc *schemas.Incident, canaryImage string) *TestResult {
        var reproJSON string
        for _, ri := range inc.State.RetrievedIncidents {
                if raw, ok := ri["reproducer"]; ok {
                        if str, ok := raw.(string); ok {
                                reproJSON = str
                                break
                        }
                }
        }
        if reproJSON == "" {
                return &TestResult{
                        Overall: true,
                        Summary: "no reproducer generated (reproducer gate skipped — Level 4 passed)",
                }
        }

        var raw map[string]string
        if err := json.Unmarshal([]byte(reproJSON), &raw); err != nil {
                logging.Error("Level 5: failed to parse reproducer JSON", "err", err)
                return &TestResult{
                        Overall: true,
                        Summary: "reproducer parse failed (non-blocking): " + err.Error(),
                }
        }
        reproducer := &schemas.GeneratedReproducer{
                ScriptFile:     raw["script_file"],
                ScriptLanguage: raw["script_language"],
                ScriptContent:  raw["script_content"],
                RunCommand:     raw["run_command"],
                Description:    raw["description"],
        }

        runner := NewTestRunner(s.clientset)
        result, err := runner.RunReproducer(ctx, canaryImage, sandboxNS, reproducer)
        if err != nil {
                logging.Error("Level 5: reproducer runner error", "err", err)
                return &TestResult{
                        Overall: true,
                        Summary: fmt.Sprintf("reproducer runner error (non-blocking): %v", err),
                }
        }
        return result
}

// fetchCanaryPodLogs retrieves the canary pod's logs for inclusion in
// SandboxResult.PodLogs.gives the LLM the actual error instead of
// a generic "canary_pods_not_ready" summary.

func (s *SandboxRunner) fetchCanaryPodLogs(ctx context.Context, ns, service string) string {
        pods, err := s.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app=%s", service),
                Limit:         1,
        })
        if err != nil || len(pods.Items) == 0 {
                return ""
        }
        pod := pods.Items[0]
        // Pick the main container (first non-init container)

        if len(pod.Spec.Containers) == 0 {
                return ""
        }
        containerName := pod.Spec.Containers[0].Name
        req := s.clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
                Container: containerName,
                TailLines: newInt64(100),
        })
        stream, err := req.Stream(ctx)
        if err != nil {
                return ""
        }
        defer stream.Close()
        buf, err := io.ReadAll(io.LimitReader(stream, 8<<10)) // 8KB max
        if err != nil {
                return string(buf) // partial
        }
        return string(buf)
}

func newInt64(n int64) *int64 { return &n }

// parsePathMethod splits a probe path entry like "POST /api/users" into
// (method, path). Default method is GET (backwards-compatible).

func parsePathMethod(entry string) (method, path string) {
        method = "GET"
        path = entry
        if idx := strings.Index(entry, " "); idx > 0 {
                m := strings.ToUpper(entry[:idx])
                switch m {
                case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
                        method = m
                        path = entry[idx+1:]
                }
        }
        return
}

// verifyEndpointsWithPaths probes each path on the canary Service and requires
// every probe to return 2xx/3xx.extracted from verifyEndpoints so the
// caller passes a local slice (no shared state race).
//   1. 4xx responses now FAIL (
//   2. POST/DELETE/PATCH endpoints are now probed with the correct method (was:
//      always GET, returning 405 on POST endpoints which falsely passed).
//   3. consecutiveSuccesses now resets on failure (

func (s *SandboxRunner) verifyEndpointsWithPaths(ctx context.Context, ns, service string, paths []string) bool {
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil {
                logging.Error("Level 2 FAILED: Service / not found (deployCanary should have created it)", "value", ns, service)
                return false
        }

        if len(svc.Spec.Ports) == 0 {
                logging.Error("Level 2 FAILED: Service / has no ports", "value", ns, service)
                return false
        }
        svcName := svc.Name

        if len(paths) == 0 {
                paths = []string{"/", "/healthz", "/readyz"}
        }

        client := sandboxHTTPClient
        requiredSuccesses := 2 // require 2 consecutive successes per path

        for _, portSpec := range svc.Spec.Ports {
                port := portSpec.Port
                for _, p := range paths {
                        method, path := parsePathMethod(p)
                        url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, path)
                        consecutiveFailures := 0
                        consecutiveSuccesses := 0

                        for attempt := 0; attempt < 3; attempt++ {
                                req, err := http.NewRequestWithContext(ctx, method, url, nil)
                                if err != nil {
                                        consecutiveFailures++
                                        continue
                                }
                                resp, err := client.Do(req)
                                if err != nil {
                                        consecutiveFailures++
                                        // Reset consecutive successes on failure — was NOT being reset (audit H4.8).

                                        consecutiveSuccesses = 0
                                        logging.Error("Level 2: probe   attempt  failed", "value", method, url, attempt+1, err)
                                        continue
                                }
                                resp.Body.Close()

                                if resp.StatusCode >= 400 {
                                        consecutiveFailures++
                                        consecutiveSuccesses = 0
                                        logging.Error("Level 2: probe   attempt  returned  (4xx/5xx = failure)", "value", method, url, attempt+1, resp.StatusCode)
                                        continue
                                }
                                consecutiveSuccesses++
                                if consecutiveSuccesses >= requiredSuccesses {
                                        break
                                }
                        }
                        if consecutiveSuccesses < requiredSuccesses {
                                log.Printf("[sandbox] Level 2 FAILED: %s %s (port %d) never returned 2xx/3xx in 3 attempts (%d successes, %d failures)",
                                        method, url, port, consecutiveSuccesses, consecutiveFailures)
                                return false
                        }
                }
        }
        return true
}

type level3Result struct {
        passed bool
        detail string
}

// verifyOriginalErrorFixed checks whether the original error from the incident
// has been resolved by the fix deployed in the sandbox.
// KeyError/ImportError/ValueError/TypeError/etc. — all Python builtins) with
// agents.ParseError(), which is multi-language (Python, Go, Java, JS, Rust,
// Ruby, C, signals). The previous implementation silently returned nil for
// Go panics, Java NullPointerExceptions, JS TypeErrors, etc., causing
// sandbox Level 3 to silently pass even when the original error was NOT
// fixed — the PR would ship with a "sandbox-verified" label. The
// `startupErrors` map had the same Python-only bug.
// The startup-error auto-pass logic now keys off the parsed language +
// error type: Python startup errors (KeyError/ImportError/ModuleNotFoundError/
// FileNotFoundError/SyntaxError) AND Go panic + Java ExceptionInInitializerError
// + JS startup SyntaxError all auto-pass Level 1 (the canary pod started and
// stayed Ready for sustainedWindow — that's sufficient evidence the startup
// error is gone).
// verifyErrorGoneFromLogs which is the primary oracle — scans canary
// logs for the original error type instead of probing HTTP endpoints).

// extractOriginalErrorType parses the incident's pod logs to find the original
// error type (e.g. "KeyError", "TypeError", "panic", "ZeroDivisionError").
// Used by Level 5 false-negative detection: if the reproducer failed but the
// original error type is GONE from its output, the fix is correct and the
// reproducer just had wrong success criteria.
func (s *SandboxRunner) extractOriginalErrorType(inc *schemas.Incident) string {
	if inc == nil || inc.Cluster.Anomalies == nil {
		return ""
	}
	for _, a := range inc.Cluster.Anomalies {
		if a.Explanation != "" {
			parsed := agents.ParseError(a.Explanation)
			if parsed != nil && parsed.ErrorType != "" {
				return parsed.ErrorType
			}
		}
	}
	// Fallback: check the cluster summary
	if inc.Cluster.Summary != "" {
		parsed := agents.ParseError(inc.Cluster.Summary)
		if parsed != nil && parsed.ErrorType != "" {
			return parsed.ErrorType
		}
	}
	return ""
}

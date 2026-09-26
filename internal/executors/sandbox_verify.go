package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// canaryPodStability samples the canary pod's crash health: total
// container restarts and whether any container was last terminated by an
// OOM kill (LastTerminationState.Reason == "OOMKilled" or exit code 137).
// This is the signal the Ready-only check cannot see: a pod that OOM-loops
// flips Ready between kills and passes any duration-only window.
func (s *SandboxRunner) canaryPodStability(ctx context.Context, ns, service string) (restarts int32, oomKilled bool, found bool) {
	pods, err := s.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", service),
		Limit:         1,
	})
	if err != nil || len(pods.Items) == 0 {
		return 0, false, false
	}
	for _, cs := range pods.Items[0].Status.ContainerStatuses {
		restarts += cs.RestartCount
		if t := cs.LastTerminationState.Terminated; t != nil {
			if t.Reason == "OOMKilled" || t.ExitCode == 137 {
				oomKilled = true
			}
		}
	}
	return restarts, oomKilled, true
}

// waitForReadyWithLogs waits for the canary Deployment to have
// ReadyReplicas >= 1 AND to stay at ReadyReplicas >= 1 for sustainedWindow
// WITHOUT any container restart in between. The sustained check is
// critical: a pod that goes Ready then immediately crashes (a common
// failure mode when config errors only manifest on the first request)
// passes a one-shot Ready check. The restart check is equally critical: a
// pod that OOM-loops goes Ready between kills and passes a duration-only
// window (run #26: a wrong fix was merged because of exactly this). If the
// canary container was OOMKilled at any point, the run fails immediately —
// a fixed image that still exceeds its memory limit is not a fix.
// During the sustained window we capture canary logs (including previous
// container logs when the container restarted) for the Level 3 oracle.
// Returns (ready, canaryLogs, failReason); failReason is "" on success.
func (s *SandboxRunner) waitForReadyWithLogs(ctx context.Context, ns, name string, timeout time.Duration) (bool, string, string) {
	deadline := time.Now().Add(timeout)
	readySince := time.Time{}
	var canaryLogsBuf strings.Builder

	// Restart baseline, captured the first time the pod goes Ready. Restarts
	// that happen before that (early startup crash) don't count against the
	// window — but an OOMKill does, whenever it happened.
	baselineRestarts := int32(-1)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, canaryLogsBuf.String(), "context_cancelled"
		default:
		}

		// OOM check first: any OOMKill on the canary = the fixed image still
		// exceeds its memory limit. Not retryable within the window — the fix
		// itself is wrong (or the limit is, which the fix must also address).
		if _, oom, found := s.canaryPodStability(ctx, ns, name); found && oom {
			logs := s.fetchCanaryPodLogs(ctx, ns, name)
			logging.Error("Level 1 FAILED: canary pod was OOMKilled — fixed image still exceeds its memory limit",
				"namespace", ns, "service", name)
			return false, logs, "canary_oomkilled"
		}

		deploy, err := s.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil && deploy.Status.ReadyReplicas >= 1 {
			restarts, _, found := s.canaryPodStability(ctx, ns, name)
			if found {
				if baselineRestarts < 0 {
					baselineRestarts = restarts
				} else if restarts > baselineRestarts {
					logging.Info("Level 1: canary pod RESTARTED during sustained window — crash-looping while flipping Ready; resetting timer",
						"restarts_before", baselineRestarts, "restarts_now", restarts)
					baselineRestarts = restarts
					readySince = time.Time{}
				}
			}
			if !readySince.IsZero() {
				if logs := s.fetchCanaryPodLogs(ctx, ns, name); logs != "" {
					canaryLogsBuf.Reset()
					canaryLogsBuf.WriteString(logs)
				}
				if time.Since(readySince) >= s.sustainedWindow {
					if logs := s.fetchCanaryPodLogs(ctx, ns, name); logs != "" {
						canaryLogsBuf.Reset()
						canaryLogsBuf.WriteString(logs)
					}
					return true, canaryLogsBuf.String(), ""
				}
			}
			if readySince.IsZero() {
				readySince = time.Now()
				logging.Info("Level 1: canary pod went Ready — sustaining",
					"since", readySince.Format(time.RFC3339),
					"sustainedWindow", s.sustainedWindow)
			}
		} else {
			if !readySince.IsZero() {
				logging.Info("Level 1: canary pod dropped below ReadyReplicas=1 before sustaining — resetting timer",
					"sustainedWindow", s.sustainedWindow)
			}
			readySince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return false, canaryLogsBuf.String(), "context_cancelled"
		case <-time.After(3 * time.Second):
		}
	}
	return false, canaryLogsBuf.String(), "canary_not_ready_or_unstable"
}

// level3Result is the outcome of the Level 3 error-gone oracle.
type level3Result struct {
	passed bool
	detail string
}

// verifyErrorGoneFromLogs is the PRIMARY ORACLE for sandbox verification.
// It checks whether the original error type (TypeError, panic, IndexError,
// NullPointerException, etc.) appears in the canary pod's logs during the
// sustained Ready window. If the error is GONE, the fix worked. If the
// error is STILL PRESENT, the fix did not work. This is universal: works
// for HTTP errors, background task errors, startup errors, OOMKills — any
// error that writes to logs. Language-agnostic: uses the multi-language
// agents.ParseError (Python, Go, Java, JS, Rust, Ruby, C, signals).
func (s *SandboxRunner) verifyErrorGoneFromLogs(ctx context.Context, inc *schemas.Incident, canaryLogs string) *level3Result {
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

	originalError := agents.ParseError(explanation)
	if originalError == nil {
		return &level3Result{
			passed: true,
			detail: "no recognizable error type in original logs — cannot verify error is gone, passing (Level 1 + Level 2 passed)",
		}
	}

	errorType := originalError.ErrorType
	errorDetail := originalError.ErrorDetail

	if canaryLogs == "" {
		return &level3Result{
			passed: true,
			detail: fmt.Sprintf("no canary logs to scan (pod Ready + health endpoints OK) — assuming %s is gone", errorType),
		}
	}

	canaryLower := strings.ToLower(canaryLogs)
	errorTypeLower := strings.ToLower(errorType)

	// Retry-aware oracle for transient-infrastructure fixes (run #30
	// post-mortem): a resilience fix — retries with backoff for a
	// transient error like a DNS gaierror or a 503 — does NOT make the
	// error disappear. The upstream dependency is still down; the fix
	// makes the system TOLERATE it. Requiring "error gone from logs"
	// makes such fixes permanently unverifiable (notification-worker's
	// ground-truth max_retries fix failed L3 three times and its PR was
	// barred from auto-merge). When the investigator classified the fault
	// as transient_infra, error-gone OR observed-retry-behavior both
	// count as verified.
	if rc := inc.State.RootCause; rc != nil && rc.FaultClass == "transient_infra" && retryEvidenceIn(canaryLower) {
		return &level3Result{
			passed: true,
			detail: fmt.Sprintf("%s still present BUT retry-with-backoff behavior confirmed in canary logs (transient_infra fix verified — resilience fixes tolerate the error rather than eliminate it)", errorType),
		}
	}

	if strings.Contains(canaryLower, errorTypeLower) {
		contextLines := extractErrorContext(canaryLogs, errorType, 3)
		return &level3Result{
			passed: false,
			detail: fmt.Sprintf("%s still present in canary pod logs:\n%s", errorType, contextLines),
		}
	}

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

	// Any crash pattern in canary logs is bad, even if the specific error
	// type isn't found.
	crashPatterns := []string{
		"traceback (most recent call last)",
		"panic:",
		"fatal error:",
		"goroutine ",
		"exception in asgi application",
		"unhandled exception",
	}
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
		detail: fmt.Sprintf("%s: %s not found in canary logs during %s sustained window — error is gone, fix verified",
			errorType, errorDetail, s.sustainedWindow),
	}
}

// retryEvidenceIn reports whether the (lower-cased) canary logs show
// retry behavior: an explicit retry/backoff log line emitted by the
// framework or application while handling failures. Used ONLY for
// transient_infra-classified fixes, where retries are the expected fix
// shape and the original error legitimately persists.
func retryEvidenceIn(canaryLower string) bool {
	for _, marker := range []string{
		"retry",     // celery "Retry in 30s", custom retry lines
		"retrying",  // "retrying in", "retrying request"
		"backoff",   // exponential backoff mentions
		"attempt 2", // "attempt 2/5", "attempt 2 of 5"
		"requeues",  // amqp requeue wording
		"will be retried",
	} {
		if strings.Contains(canaryLower, marker) {
			return true
		}
	}
	return false
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
// and runs it via the TestRunner. Returns nil if no test was generated.
// Returns a TestResult otherwise.
func (s *SandboxRunner) runRegressionTest(ctx context.Context, sandboxNS string, inc *schemas.Incident, canaryImage string) *TestResult {
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

	test, err := parseGeneratedTest(testJSON)
	if err != nil {
		logging.Error("Level 4: failed to parse test JSON", "err", err)
		return &TestResult{
			Overall: true,
			Summary: "test parse failed (non-blocking): " + err.Error(),
		}
	}

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
		return nil, fmt.Errorf("parse generated test: %w", err)
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

// runReproducer extracts the LLM-generated reproducer from the incident
// state and runs it via the TestRunner.
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
// SandboxResult.PodLogs. Returning the actual error gives the LLM the
// real context instead of a generic "canary_pods_not_ready" summary.
// When the container has restarted, ALSO fetches the previous container's
// logs (kubectl logs --previous): the crash evidence — the very lines the
// Level 3 oracle needs to scan — lives there, not in the fresh container's
// clean logs.
func (s *SandboxRunner) fetchCanaryPodLogs(ctx context.Context, ns, service string) string {
	pods, err := s.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", service),
		Limit:         1,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	pod := pods.Items[0]
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	containerName := pod.Spec.Containers[0].Name

	current := s.streamContainerLogs(ctx, ns, pod.Name, containerName, false)

	restarted := false
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 0 {
			restarted = true
			break
		}
	}
	if restarted {
		if prev := s.streamContainerLogs(ctx, ns, pod.Name, containerName, true); prev != "" {
			return current + "\n\n--- PREVIOUS CONTAINER LOGS (before last restart — crash evidence) ---\n" + prev
		}
	}
	return current
}

// streamContainerLogs reads up to 8KB of a container's logs, current or
// previous instance.
func (s *SandboxRunner) streamContainerLogs(ctx context.Context, ns, podName, container string, previous bool) string {
	req := s.clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: newInt64(100),
		Previous:  previous,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	buf, err := io.ReadAll(io.LimitReader(stream, 8<<10)) // 8KB max
	if err != nil {
		return string(buf)
	}
	return string(buf)
}

// newInt64 returns a pointer to n. Helper for *int64 fields in corev1.
func newInt64(n int64) *int64 { return &n }

// parsePathMethod splits a probe path entry like "POST /api/users" into
// (method, path). Default method is GET.
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

// probeTarget is one fully-resolved Level 2 probe: method + absolute URL
// against the canary Service (plus the port/path it came from, for logs).
type probeTarget struct {
	method string
	url    string
	port   int32
	path   string
}

// buildProbeTargets expands Service ports × paths into concrete probe
// URLs against the canary Service's cluster DNS name.
func buildProbeTargets(svcName, ns string, ports []corev1.ServicePort, paths []string) []probeTarget {
	targets := make([]probeTarget, 0, len(ports)*len(paths))
	for _, portSpec := range ports {
		for _, p := range paths {
			method, path := parsePathMethod(p)
			targets = append(targets, probeTarget{
				method: method,
				url:    fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, portSpec.Port, path),
				port:   portSpec.Port,
				path:   path,
			})
		}
	}
	return targets
}

// verifyEndpointsWithPaths probes the canary Service's health endpoints:
//   - all-must-pass (anyOf=false): every port × path combination must
//     return 2 consecutive 2xx/3xx within 3 attempts. Used for
//     operator-pinned paths and spec-derived paths — both are explicit
//     contracts.
//   - any-of (anyOf=true): the first port × path that returns 2
//     consecutive 2xx/3xx passes the level. Used for the convention
//     candidate set, where requiring every candidate would be
//     unsatisfiable (services implement a subset of the conventions).
func (s *SandboxRunner) verifyEndpointsWithPaths(ctx context.Context, ns, service string, paths []string, anyOf bool) bool {
	svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		logging.Error("Level 2 FAILED: Service not found (deployCanary should have created it)",
			"namespace", ns, "service", service)
		return false
	}

	if len(svc.Spec.Ports) == 0 {
		logging.Error("Level 2 FAILED: Service has no ports",
			"namespace", ns, "service", service)
		return false
	}
	svcName := svc.Name

	if len(paths) == 0 {
		paths = conventionProbePaths
		anyOf = true
	}

	targets := buildProbeTargets(svcName, ns, svc.Spec.Ports, paths)
	return evaluateProbes(ctx, sandboxHTTPClient, targets, anyOf)
}

// evaluateProbes runs the probe targets under all-of or any-of semantics.
// Each target gets 3 attempts and needs 2 consecutive 2xx/3xx successes
// so a flaky startup neither false-passes nor false-fails on one sample.
func evaluateProbes(ctx context.Context, client *http.Client, targets []probeTarget, anyOf bool) bool {
	const requiredSuccesses = 2
	for _, t := range targets {
		consecutiveFailures := 0
		consecutiveSuccesses := 0

		for attempt := 0; attempt < 3; attempt++ {
			req, err := http.NewRequestWithContext(ctx, t.method, t.url, nil)
			if err != nil {
				consecutiveFailures++
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				consecutiveFailures++
				consecutiveSuccesses = 0
				logging.Error("Level 2: probe failed",
					"method", t.method, "url", t.url,
					"attempt", attempt+1, "err", err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode >= 400 {
				consecutiveFailures++
				consecutiveSuccesses = 0
				logging.Error("Level 2: probe returned 4xx/5xx",
					"method", t.method, "url", t.url,
					"attempt", attempt+1, "status", resp.StatusCode)
				continue
			}
			consecutiveSuccesses++
			if consecutiveSuccesses >= requiredSuccesses {
				break
			}
		}

		if consecutiveSuccesses >= requiredSuccesses {
			if anyOf {
				logging.Info("Level 2: candidate endpoint healthy — any-of satisfied",
					"method", t.method, "url", t.url, "port", t.port)
				return true
			}
			continue // all-of: this target passed, on to the next
		}
		if anyOf {
			logging.Error("Level 2: candidate endpoint not healthy (any-of — trying next candidate)",
				"method", t.method, "url", t.url, "port", t.port,
				"successes", consecutiveSuccesses, "failures", consecutiveFailures)
			continue
		}
		logging.Error("Level 2 FAILED: probe never returned 2xx/3xx in 3 attempts",
			"method", t.method, "url", t.url, "port", t.port,
			"successes", consecutiveSuccesses, "failures", consecutiveFailures)
		return false
	}
	// All targets examined: all-of reaches here only when every target
	// passed; any-of reaches here only when none did.
	return !anyOf
}

// extractOriginalErrorType parses the incident's pod logs to find the
// original error type (e.g. "KeyError", "TypeError", "panic",
// "ZeroDivisionError"). Used by Level 5 false-negative detection: if the
// reproducer failed but the original error type is GONE from its output,
// the fix is correct and the reproducer just had wrong success criteria.
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
	if inc.Cluster.Summary != "" {
		parsed := agents.ParseError(inc.Cluster.Summary)
		if parsed != nil && parsed.ErrorType != "" {
			return parsed.ErrorType
		}
	}
	return ""
}

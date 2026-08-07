package executors

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// sandboxHTTPClient is the shared HTTP client used by Level 2 and Level 3
// probes. Reusing one client lets the kernel keep TCP+TLS connections to
// the canary Service warm across the multiple probes per incident.
var sandboxHTTPClient = &http.Client{Timeout: 5 * time.Second}

// SandboxResult is the outcome of a sandbox run. Per-level failure details
// are spread across Summary, PodLogs, and ProbeResults so the retry loop
// can feed actionable context back to the LLM.
type SandboxResult struct {
	Verified      bool
	Summary       string
	Namespace     string
	MirrorApplied bool
	ErrorCount5xx int
	RequestCount  int

	// InfrastructureError marks a failure caused by the sandbox itself
	// (RBAC denied, image pull failure, namespace creation failed). When
	// true, the retry loop should NOT feed the error back to the LLM —
	// the LLM cannot fix a permissions issue.
	InfrastructureError bool

	// PodLogs is the last N bytes of canary pod logs captured during
	// Level 1. Returned on failure so the LLM sees the actual error.
	PodLogs string

	// ProbeResults carries per-probe outcomes for Level 2/3/4/5 failures.
	ProbeResults []string
}

// SandboxRunner creates an isolated namespace, deploys the fix, and verifies
// it through the 5-level suite documented in doc.go.
type SandboxRunner struct {
	clientset         kubernetes.Interface
	sandboxPrefix     string
	enableMirroring   bool
	probePaths        []string
	sustainedWindow   time.Duration
	sentinelNamespace string

	// probePathsMu guards probePaths because Run is invoked from multiple
	// investigation goroutines on a shared instance.
	probePathsMu sync.RWMutex
}

// NewSandbox creates a SandboxRunner with default probe paths
// (/, /healthz, /readyz) and a 15-second sustained-ready window.
func NewSandbox(clientset kubernetes.Interface, sandboxPrefix string, enableMirroring bool) *SandboxRunner {
	return &SandboxRunner{
		clientset:         clientset,
		sandboxPrefix:     sandboxPrefix,
		enableMirroring:   enableMirroring,
		probePaths:        []string{"/", "/healthz", "/readyz"},
		sustainedWindow:   15 * time.Second,
		sentinelNamespace: "sentinel",
	}
}

// WithProbePaths overrides the default Level 2 probe paths.
func (s *SandboxRunner) WithProbePaths(paths []string) *SandboxRunner {
	if len(paths) > 0 {
		s.probePaths = paths
	}
	return s
}

// WithSustainedReady overrides the default 15s sustained-ready window.
// Set to 0 to disable (NOT recommended — reintroduces crash-after-ready
// false positives).
func (s *SandboxRunner) WithSustainedReady(d time.Duration) *SandboxRunner {
	s.sustainedWindow = d
	return s
}

// WithSentinelNamespace sets the namespace Sentinel itself runs in. Used by
// the sandbox NetworkPolicy ingress allow-list so probes from the Sentinel
// namespace reach the canary.
func (s *SandboxRunner) WithSentinelNamespace(ns string) *SandboxRunner {
	if ns != "" {
		s.sentinelNamespace = ns
	}
	return s
}

// Run creates a sandbox, deploys the fix, verifies it, returns the verdict.
func (s *SandboxRunner) Run(ctx context.Context, inc *schemas.Incident) SandboxResult {
	fp := inc.State.FixProposal
	if fp == nil {
		return SandboxResult{Verified: false, Summary: "no fix proposal"}
	}

	sandboxNS := fmt.Sprintf("%s-%s-%s", s.sandboxPrefix, fp.TargetService, fp.FixID.String()[:8])

	// 1. Create the sandbox namespace + NetworkPolicy.
	if err := s.createNamespace(ctx, sandboxNS); err != nil {
		return SandboxResult{Verified: false, Summary: fmt.Sprintf("create_namespace: %v", err), Namespace: sandboxNS}
	}
	if err := s.createSandboxNetworkPolicy(ctx, sandboxNS); err != nil {
		logging.Error("failed to create sandbox NetworkPolicy (canary will have unrestricted egress)",
			"namespace", sandboxNS, "err", err)
	}
	defer s.cleanup(ctx, sandboxNS)

	// 2. Deploy the canary into the sandbox.
	deployResult := s.deployCanary(ctx, inc, sandboxNS)
	if !deployResult.success {
		return SandboxResult{
			Verified:            false,
			Summary:             fmt.Sprintf("deploy_failed: %s", deployResult.err),
			Namespace:           sandboxNS,
			InfrastructureError: true,
		}
	}

	healthProbePaths := s.getProbePaths()

	// 3. Level 1 — wait for canary Ready AND stay Ready for sustainedWindow.
	// During the sustained window we also stream canary logs for Level 3.
	logging.Info("Level 1: waiting for canary pod Ready + sustained window",
		"sustainedWindow", s.sustainedWindow)
	ready, canaryLogs := s.waitForReadyWithLogs(ctx, sandboxNS, fp.TargetService, 240*time.Second)
	if !ready {
		summary := "canary_pods_not_ready_or_unstable (startup crash not fixed, or pod crashes after going Ready)"
		if canaryLogs != "" {
			summary += "\n\n--- CANARY POD LOGS (last 2KB) ---\n" + lastN(canaryLogs, 2048)
		}
		return SandboxResult{Verified: false, Summary: summary, Namespace: sandboxNS, PodLogs: canaryLogs}
	}
	logging.Info("Level 1 PASSED: canary pod stayed Ready for sustained window",
		"sustainedWindow", s.sustainedWindow)

	// 4. Level 2 — health endpoints only. 4xx/5xx = fail.
	if !s.verifyEndpointsWithPaths(ctx, sandboxNS, fp.TargetService, healthProbePaths) {
		return SandboxResult{
			Verified:  false,
			Summary:   "health_endpoints_failing (/, /healthz, /readyz returned 4xx/5xx — app is broken)",
			Namespace: sandboxNS,
		}
	}
	logging.Info("Level 2 PASSED: health endpoints returned 2xx/3xx")

	// 5. Level 3 (PRIMARY ORACLE) — original error type gone from canary logs.
	level3Result := s.verifyErrorGoneFromLogs(ctx, inc, canaryLogs)
	if !level3Result.passed {
		return SandboxResult{
			Verified:     false,
			Summary:      fmt.Sprintf("original_error_still_present: %s", level3Result.detail),
			Namespace:    sandboxNS,
			PodLogs:      canaryLogs,
			ProbeResults: []string{level3Result.detail},
		}
	}
	logging.Info("Level 3 PASSED", "detail", level3Result.detail)

	// 6+7. Level 4 (regression test) + Level 5 (reproducer) in parallel.
	// Both run against the same canary image in the same sandbox namespace.
	var (
		testResult  *TestResult
		reproResult *TestResult
	)
	if fp.CanaryImage != nil && *fp.CanaryImage != "" {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer agentsRecover("sandbox-runRegressionTest")
			testResult = s.runRegressionTest(ctx, sandboxNS, inc, *fp.CanaryImage)
		}()
		go func() {
			defer wg.Done()
			defer agentsRecover("sandbox-runReproducer")
			reproResult = s.runReproducer(ctx, sandboxNS, inc, *fp.CanaryImage)
		}()
		wg.Wait()
	}

	// Evaluate L4 (regression test).
	if testResult != nil {
		if !testResult.Overall && !strings.Contains(testResult.Summary, "no test generated") {
			logging.Error("Level 4 FAILED: regression test did not pass")
			return SandboxResult{
				Verified:     false,
				Summary:      fmt.Sprintf("regression_test_failed: %s", testResult.Summary),
				Namespace:    sandboxNS,
				PodLogs:      canaryLogs,
				ProbeResults: []string{testResult.Summary},
			}
		}
		logging.Info("Level 4 PASSED: regression test verified the fix")
	}

	// Evaluate L5 (reproducer). Infra errors (log fetch, runner error) are
	// non-blocking — a broken test runner must not block a correct fix.
	if reproResult != nil {
		if reproResult.Overall {
			logging.Info("Level 5 PASSED: reproducer script verified the fix")
		} else if isReproducerInfraError(reproResult.Summary) {
			logging.Error("Level 5 SKIPPED: reproducer infrastructure error (non-blocking)",
				"summary", reproResult.Summary)
		} else if strings.Contains(reproResult.Summary, "no reproducer generated") {
			logging.Info("Level 5 SKIPPED: no reproducer generated")
		} else {
			// Real reproducer failure — but if the original error type is
			// GONE from the reproducer output, the fix is correct and the
			// reproducer just had wrong success criteria.
			originalErrorType := s.extractOriginalErrorType(inc)
			stillPresent := originalErrorStillInReproducer(reproResult.Summary, originalErrorType)
			if originalErrorType != "" && !stillPresent {
				logging.Error("Level 5 FAILED but original error type is gone — fix is correct, reproducer had wrong success criteria",
					"originalErrorType", originalErrorType)
			} else {
				logging.Error("Level 5 FAILED: reproducer script crashed",
					"originalErrorType", originalErrorType)
				return SandboxResult{
					Verified:     false,
					Summary:      fmt.Sprintf("reproducer_failed: %s", reproResult.Summary),
					Namespace:    sandboxNS,
					PodLogs:      canaryLogs,
					ProbeResults: []string{reproResult.Summary},
				}
			}
		}
	}

	return SandboxResult{
		Verified:  true,
		Summary:   "all_checks_passed (pod ready + sustained + health endpoints ok + original error gone from logs + regression test passed + reproducer passed)",
		Namespace: sandboxNS,
	}
}

// isReproducerInfraError reports whether a Level 5 failure summary looks
// like an infrastructure error (broken test runner, log fetch failure, pod
// scheduling failure) rather than a real reproducer failure. Infra errors
// are non-blocking — they don't indicate the fix is wrong.
func isReproducerInfraError(summary string) bool {
	infraMarkers := []string{
		"fetch test pod logs",
		"pod failed:",
		"runner error",
		"not valid for pod",
	}
	for _, marker := range infraMarkers {
		if strings.Contains(summary, marker) {
			return true
		}
	}
	return false
}

// originalErrorStillInReproducer checks whether the reproducer's output
// contains the original error type. If the original error type is GONE, the
// fix is correct even if the reproducer "failed" — the reproducer just had
// wrong success criteria.
func originalErrorStillInReproducer(reproOutput, originalErrorType string) bool {
	if originalErrorType == "" {
		return false
	}
	actualOutput := reproOutput
	if idx := strings.Index(reproOutput, "--- Output ---"); idx >= 0 {
		actualOutput = reproOutput[idx:]
	}
	lowerOutput := strings.ToLower(actualOutput)
	lowerErrorType := strings.ToLower(originalErrorType)
	return strings.Contains(lowerOutput, lowerErrorType+":") ||
		strings.Contains(lowerOutput, lowerErrorType+" '") ||
		strings.Contains(lowerOutput, lowerErrorType+"(") ||
		strings.Contains(lowerOutput, "raise "+lowerErrorType)
}

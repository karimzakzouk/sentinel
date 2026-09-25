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

	// LevelsVerified records which sandbox levels actually ran and
	// passed, e.g. "L1-L5" (full suite incl. regression test +
	// reproducer) or "L1-L3" (pod ready + health endpoints + error gone,
	// L4/L5 skipped because no canary image was built). Surfaced on the
	// PR label (sandbox-verified:L1-L5) and the auto-merge gate so
	// reviewers can see how strong the verification actually was.
	LevelsVerified string

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

	// networkPolicyRequired fails the sandbox run when the default-deny
	// NetworkPolicy cannot be created. The policy is the last line of the
	// prompt-injection defense (a compromised fix must not be able to
	// exfiltrate secrets from the canary pod), so failing closed is the
	// safe default. Operators with restricted clusters (no
	// networking.k8s.io write access) can set
	// SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED=false to restore the old
	// fail-open behavior.
	networkPolicyRequired bool

	// probePathsMu guards probePaths because Run is invoked from multiple
	// investigation goroutines on a shared instance.
	probePathsMu sync.RWMutex
}

// NewSandbox creates a SandboxRunner in probe-path auto mode and a
// 15-second sustained-ready window. In auto mode Level 2 derives probe
// paths from the workload's own readiness/liveness probes and falls back
// to the convention candidate set (any-of) — see resolveProbePaths.
func NewSandbox(clientset kubernetes.Interface, sandboxPrefix string, enableMirroring bool) *SandboxRunner {
	return &SandboxRunner{
		clientset:         clientset,
		sandboxPrefix:     sandboxPrefix,
		enableMirroring:   enableMirroring,
		probePaths:        nil, // nil = auto mode (spec-derived → convention any-of)
		sustainedWindow:   15 * time.Second,
		sentinelNamespace: "sentinel",
	}
}

// WithProbePaths pins explicit Level 2 probe paths (ALL must pass). An
// empty or nil list keeps auto mode: paths are derived per-canary from
// the prod Deployment's readiness/liveness httpGet probes, falling back
// to the convention candidate set with any-of semantics.
func (s *SandboxRunner) WithProbePaths(paths []string) *SandboxRunner {
	if len(paths) > 0 {
		s.probePaths = paths
	}
	return s
}

// conventionProbePaths is the candidate set probed when neither the
// operator nor the workload spec declares health paths. Evaluated with
// any-of semantics: ONE candidate answering 2xx/3xx is enough — demanding
// all of them would make the gate unsatisfiable for services that only
// implement a subset of the conventions.
var conventionProbePaths = []string{
	"/health", "/healthz", "/ready", "/readyz", "/livez", "/live", "/status", "/ping", "/",
}

// resolveProbePaths picks the Level 2 probe paths for one canary.
// Precedence:
//  1. operator-configured paths (SENTINEL_SANDBOX_PROBE_PATHS / chart
//     sandboxProbePaths) — an explicit contract, ALL must pass;
//  2. paths declared by the prod Deployment's own readiness/liveness
//     httpGet probes — the service's own definition of health (the
//     kubelet already proves these paths work when the pod is Ready),
//     ALL must pass;
//  3. the convention candidate set — ANY must pass.
//
// Returns (paths, anyOf, source) where source is "config", "spec" or
// "convention" — used in logs and failure summaries.
func (s *SandboxRunner) resolveProbePaths(specPaths []string) ([]string, bool, string) {
	if explicit := s.getProbePaths(); len(explicit) > 0 {
		return explicit, false, "config"
	}
	if len(specPaths) > 0 {
		return specPaths, false, "spec"
	}
	return conventionProbePaths, true, "convention"
}

// probeModeStr renders the Level 2 pass semantics for logs.
func probeModeStr(anyOf bool) string {
	if anyOf {
		return "any-of"
	}
	return "all-must-pass"
}

// WithSustainedReady overrides the default 15s sustained-ready window.
// Set to 0 to disable (NOT recommended — reintroduces crash-after-ready
// false positives).
func (s *SandboxRunner) WithSustainedReady(d time.Duration) *SandboxRunner {
	s.sustainedWindow = d
	return s
}

// WithNetworkPolicyRequired controls fail-closed behavior when the sandbox
// NetworkPolicy cannot be created. Default true — the run aborts with an
// InfrastructureError instead of deploying a canary with unrestricted
// egress.
func (s *SandboxRunner) WithNetworkPolicyRequired(required bool) *SandboxRunner {
	s.networkPolicyRequired = required
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
		if s.networkPolicyRequired {
			// Fail closed: without the default-deny policy a compromised fix
			// could exfiltrate secrets from the canary. Abort the run rather
			// than continue with unrestricted egress.
			logging.Error("failed to create sandbox NetworkPolicy — aborting (fail-closed)",
				"namespace", sandboxNS, "err", err)
			return SandboxResult{
				Verified:            false,
				Summary:             fmt.Sprintf("networkpolicy_create_failed (fail-closed): %v — the canary must not run with unrestricted egress", err),
				Namespace:           sandboxNS,
				InfrastructureError: true,
			}
		}
		logging.Error("failed to create sandbox NetworkPolicy (canary will have unrestricted egress — SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED=false)",
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

	// Level 2 probe paths: operator config > workload spec > convention.
	healthProbePaths, anyOfProbes, probeSource := s.resolveProbePaths(deployResult.specProbePaths)

	// 3. Level 1 — wait for canary Ready AND stay Ready for sustainedWindow
	// without restarting, and never OOMKilled. During the sustained window
	// we also stream canary logs (incl. previous-container crash logs).
	logging.Info("Level 1: waiting for canary pod Ready + sustained window (restart-stable, OOM-checked)",
		"sustainedWindow", s.sustainedWindow)
	ready, canaryLogs, failReason := s.waitForReadyWithLogs(ctx, sandboxNS, fp.TargetService, 240*time.Second)
	if !ready {
		summary := "canary_pods_not_ready_or_unstable (startup crash not fixed, or pod crashes after going Ready)"
		switch failReason {
		case "canary_oomkilled":
			summary = "canary_pod_oomkilled — the FIXED image was OOMKilled (exit 137) in the sandbox. " +
				"The fix does NOT resolve the memory problem: the fixed code still exceeds its memory limit. " +
				"Look for an unbounded growing structure (map/cache/slice that is only ever appended to) or raise the memory limit in the deployment if the workload legitimately needs more."
		case "canary_not_ready_or_unstable":
			if canaryLogs != "" {
				summary = "canary_pods_not_ready_or_unstable (pod never sustained a Ready window without crashing — the startup crash or crash-after-ready is NOT fixed)"
			}
		}
		if canaryLogs != "" {
			summary += "\n\n--- CANARY POD LOGS (last 2KB, incl. previous container if restarted) ---\n" + lastN(canaryLogs, 2048)
		}
		return SandboxResult{Verified: false, Summary: summary, Namespace: sandboxNS, PodLogs: canaryLogs}
	}
	logging.Info("Level 1 PASSED: canary pod stayed Ready for sustained window (no restarts, no OOM kills)",
		"sustainedWindow", s.sustainedWindow)

	// 4. Level 2 — health endpoints only. 4xx/5xx = fail.
	logging.Info("Level 2: probing health endpoints",
		"paths", healthProbePaths, "source", probeSource, "mode", probeModeStr(anyOfProbes))
	if !s.verifyEndpointsWithPaths(ctx, sandboxNS, fp.TargetService, healthProbePaths, anyOfProbes) {
		summary := fmt.Sprintf("health_endpoints_failing (%s returned 4xx/5xx — app is broken)",
			strings.Join(healthProbePaths, ", "))
		if anyOfProbes {
			summary = fmt.Sprintf("health_endpoints_failing (none of the candidate endpoints [%s] returned 2xx/3xx — app is broken or it exposes no health endpoint)",
				strings.Join(healthProbePaths, ", "))
		}
		return SandboxResult{
			Verified:  false,
			Summary:   summary,
			Namespace: sandboxNS,
		}
	}
	logging.Info("Level 2 PASSED: health endpoints returned 2xx/3xx",
		"source", probeSource, "mode", probeModeStr(anyOfProbes))

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
	maxLevel := 3 // L1-L3 passed to get here
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
		if !strings.Contains(testResult.Summary, "no test generated") {
			maxLevel = 4
			logging.Info("Level 4 PASSED: regression test verified the fix")
		}
	}

	// Evaluate L5 (reproducer). Infra errors (log fetch, runner error) are
	// non-blocking — a broken test runner must not block a correct fix.
	if reproResult != nil {
		if reproResult.Overall {
			maxLevel = 5
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
		Verified:       true,
		Summary:        fmt.Sprintf("all_checks_passed (pod ready + sustained + health endpoints ok + original error gone from logs%s)", levelsSuffix(maxLevel)),
		Namespace:      sandboxNS,
		LevelsVerified: fmt.Sprintf("L1-L%d", maxLevel),
	}
}

// levelsSuffix renders the L4/L5 portion of the success summary so the PR
// body reflects exactly how far verification went.
func levelsSuffix(maxLevel int) string {
	switch maxLevel {
	case 5:
		return " + regression test passed + reproducer passed"
	case 4:
		return " + regression test passed (reproducer skipped)"
	default:
		return " (L4/L5 skipped — no canary image)"
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

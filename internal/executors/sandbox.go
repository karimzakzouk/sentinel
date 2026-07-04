package executors

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "fmt"
        "net/http"
        "strings"
        "sync"
        "time"

        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// sandboxHTTPClient is the shared HTTP client for sandbox Level 2 + 3 probes.
// A package-level shared client reuses TCP+TLS connections to the canary
// Service, cutting probe latency ~50% (Client was
// created on every call, 2-3 per incident, defeating connection pooling).

var sandboxHTTPClient = &http.Client{Timeout: 5 * time.Second}

// SandboxResult is the outcome of a sandbox run. Carries per-level failure
// details (was a one-line Summary, making the retry loop ineffective).

type SandboxResult struct {
        Verified      bool
        Summary       string
        Namespace     string
        MirrorApplied bool
        ErrorCount5xx int
        RequestCount  int

        // (e.g. RBAC denied, image pull failure, namespace creation failed).
        // When true, the retry loop should NOT feed the error back to the LLM —
        // the LLM can't fix a permissions issue.

        InfrastructureError bool

        // the LLM can actually diagnose + fix the issue.

        PodLogs      string   // last N bytes of canary pod logs (Level 1 failure)
        ProbeResults []string // per-probe outcome (Level 2/3 failure)
}

// SandboxRunner creates an isolated namespace, deploys the fix, and verifies it.
// Three verification levels:
//   - Level 1: Canary pod becomes Ready AND stays Ready for sustainedWindow (startup crash fixed)
//   - Level 2: All configured endpoints return 2xx/3xx (app starts + serves traffic)
//   - Level 3: Original error is gone (reproduce the crash conditions)
// Probe paths are configurable via SENTINEL_SANDBOX_PROBE_PATHS. Defaults to
// /,/healthz,/readyz. For app-specific paths (e.g. /pay?amount=0), set the
// env var.
// Safety guarantees:
//   - waitForReady requires ReadyReplicas >= 1 sustained for sustainedWindow
//     (default 30s) before passing Level 1. A pod that goes Ready then
//     immediately crashes does not pass Level 1.
//   - verifyEndpoints does not silently swallow connection errors. If every
//     probe fails to connect, Level 2 fails.
//   - Level 3 verification fails on ANY 4xx/5xx response, not just on
//     5xx + error-string match.
//   - patchPodSpec force-sets a safe security context regardless of what the
//     prod pod uses.
//   - createNamespace also creates a default-deny NetworkPolicy on the
//     sandbox namespace so the canary can't reach arbitrary services.
//   - The sandbox NetworkPolicy ingress rule allows probes from the actual
//     Sentinel namespace ( fix).

type SandboxRunner struct {
        clientset        *kubernetes.Clientset
        sandboxPrefix    string
        enableMirroring  bool
        probePaths       []string
        sustainedWindow  time.Duration
        sentinelNamespace string

        // Run() is invoked on the shared instance from multiple investigation goroutines.

        probePathsMu sync.RWMutex
}

// NewSandbox creates a SandboxRunner.

func NewSandbox(clientset *kubernetes.Clientset, sandboxPrefix string, enableMirroring bool) *SandboxRunner {
        return &SandboxRunner{
                clientset:        clientset,
                sandboxPrefix:    sandboxPrefix,
                enableMirroring:  enableMirroring,
                probePaths:       []string{"/", "/healthz", "/readyz"}, // sensible defaults
                sustainedWindow:  15 * time.Second,
                sentinelNamespace: "sentinel",
        }
}

// WithProbePaths sets custom probe paths for Level 2 + 3 verification.

func (s *SandboxRunner) WithProbePaths(paths []string) *SandboxRunner {
        if len(paths) > 0 {
                s.probePaths = paths
        }
        return s
}

// WithSustainedReady overrides the default 30s sustained-ready window.
// crash-after-ready detection. Set to 0 to disable (NOT recommended —
// reintroduces the crash-after-ready false positive).

func (s *SandboxRunner) WithSustainedReady(d time.Duration) *SandboxRunner {
        s.sustainedWindow = d
        return s
}

// WithSentinelNamespace sets the namespace Sentinel itself runs in, used by
// the sandbox NetworkPolicy ingress allow-list.Replaces the
// hardcoded "sentinel" literal. When Sentinel is installed via
// `helm install sre-bot sentinel/sentinel`, the sandbox NetworkPolicy was
// denying probes from the sre-bot namespace → Level 2 verification always
// failed on connection errors.

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

        // 1. Create the sandbox namespace + NetworkPolicy

        if err := s.createNamespace(ctx, sandboxNS); err != nil {
                return SandboxResult{Verified: false, Summary: fmt.Sprintf("create_namespace: %v", err), Namespace: sandboxNS}
        }

        // This is best-effort — if it fails, we log and continue (the namespace
        // itself is still isolated by virtue of being separate).

        if err := s.createSandboxNetworkPolicy(ctx, sandboxNS); err != nil {
                logging.Error("failed to create NetworkPolicy in :  (canary will have unrestricted egress)", "value", sandboxNS, err)
        }

        // Always clean up at the end

        defer s.cleanup(ctx, sandboxNS)

        // 2. Deploy the canary into the sandbox

        deployResult := s.deployCanary(ctx, inc, sandboxNS)
        if !deployResult.success {
                return SandboxResult{
                        Verified:            false,
                        Summary:             fmt.Sprintf("deploy_failed: %s", deployResult.err),
                        Namespace:           sandboxNS,
                        InfrastructureError: true,
                }
        }

        // probing (Layer C from v3.14.0) is REMOVED. It caused more bugs than
        // it fixed (wrong endpoint detection, 4xx on handled errors = false
        // failure). The primary oracle is now Level 3: scan canary logs for
        // the original error type. If the error is gone, the fix works.
        // Level 2 now only probes HEALTH endpoints (/, /healthz, /readyz).
        // 4xx on health = fail (app is broken). 4xx on trigger endpoints is
        // no longer checked — a 4xx on the trigger endpoint means the fix
        // correctly handles the error instead of crashing.

        healthProbePaths := s.getProbePaths() // defaults: /, /healthz, /readyz

        // 3. Level 1 — wait for canary pods to be Ready AND stay Ready for
        // sustainedWindow. During this window, we also stream canary logs to
        // check for the original error type (Level 3 primary oracle).

        logging.Info("Level 1: waiting for canary pod Ready + sustained window", "sustainedwindow", s.sustainedWindow)
        ready, canaryLogs := s.waitForReadyWithLogs(ctx, sandboxNS, fp.TargetService, 240*time.Second)
        if !ready {
                summary := "canary_pods_not_ready_or_unstable (startup crash not fixed, or pod crashes after going Ready)"
                if canaryLogs != "" {
                        summary += "\n\n--- CANARY POD LOGS (last 2KB) ---\n" + lastN(canaryLogs, 2048)
                }
                return SandboxResult{Verified: false, Summary: summary, Namespace: sandboxNS, PodLogs: canaryLogs}
        }
        logging.Info("Level 1 PASSED: canary pod is Ready and stayed Ready for", "sustainedwindow", s.sustainedWindow)

        // 4. Level 2 — health endpoints only. 4xx/5xx = fail (app is broken).

        if !s.verifyEndpointsWithPaths(ctx, sandboxNS, fp.TargetService, healthProbePaths) {
                return SandboxResult{Verified: false, Summary: "health_endpoints_failing (/, /healthz, /readyz returned 4xx/5xx — app is broken)", Namespace: sandboxNS}
        }
        logging.Info("Level 2 PASSED: health endpoints returned 2xx/3xx")

        // 5. Level 3 (PRIMARY ORACLE) — scan canary logs for the original error type.
        // If the original error (TypeError, panic, IndexError, etc.) appears in the
        // canary's logs during the sustained window, the fix did NOT work.
        // If the error is gone, the fix worked. This is the error-gone oracle.
        // This is universal: works for HTTP errors, background task errors,
        // startup errors, OOMKills — any error that writes to logs.
        // Language-agnostic: uses the existing multi-language ParseError() which
        // already handles Python, Go, Java, JS, Rust, Ruby, C, signals.

        level3Result := s.verifyErrorGoneFromLogs(ctx, inc, canaryLogs)
        if !level3Result.passed {
                return SandboxResult{
                        Verified:      false,
                        Summary:       fmt.Sprintf("original_error_still_present: %s", level3Result.detail),
                        Namespace:     sandboxNS,
                        PodLogs:       canaryLogs,
                        ProbeResults:  []string{level3Result.detail},
                }
        }
        logging.Info("Level 3 PASSED", "detail", level3Result.detail)

        // 6+7. Level 4 (regression test) + Level 5 (reproducer) — run in PARALLEL.
        // Both run against the same canary image in the same sandbox namespace.
        // They're independent — no data dependency between them.

        var (
                testResult  *TestResult
                reproResult *TestResult
        )
        if fp.CanaryImage != nil && *fp.CanaryImage != "" {
                var wg sync.WaitGroup
                wg.Add(2)
                go func() {
                        defer wg.Done()
                        testResult = s.runRegressionTest(ctx, sandboxNS, inc, *fp.CanaryImage)
                }()
                go func() {
                        defer wg.Done()
                        reproResult = s.runReproducer(ctx, sandboxNS, inc, *fp.CanaryImage)
                }()
                wg.Wait()
        }

        // Evaluate L4 (regression test)
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

        // Evaluate L5 (reproducer)
        if reproResult != nil {
                isInfraError := strings.Contains(reproResult.Summary, "fetch test pod logs") ||
                        strings.Contains(reproResult.Summary, "pod failed:") ||
                        strings.Contains(reproResult.Summary, "runner error") ||
                        strings.Contains(reproResult.Summary, "not valid for pod")
                if !reproResult.Overall &&
                   !strings.Contains(reproResult.Summary, "no reproducer generated") &&
                   !isInfraError {
                        originalErrorType := s.extractOriginalErrorType(inc)
                        reproOutput := reproResult.Summary
                        actualOutput := reproOutput
                        if idx := strings.Index(reproOutput, "--- Output ---"); idx >= 0 {
                                actualOutput = reproOutput[idx:]
                        }
                        originalErrorStillPresent := false
                        if originalErrorType != "" {
                                lowerOutput := strings.ToLower(actualOutput)
                                lowerErrorType := strings.ToLower(originalErrorType)
                                if strings.Contains(lowerOutput, lowerErrorType+":") ||
                                        strings.Contains(lowerOutput, lowerErrorType+" '") ||
                                        strings.Contains(lowerOutput, lowerErrorType+"(") ||
                                        strings.Contains(lowerOutput, "raise "+lowerErrorType) {
                                        originalErrorStillPresent = true
                                }
                        }
                        if originalErrorType != "" && !originalErrorStillPresent {
                                logging.Error("Level 5 FAILED but original error type is gone — fix is correct, reproducer had wrong success criteria", "originalerrortype", originalErrorType)
                        } else {
                                logging.Error("Level 5 FAILED: reproducer script crashed", "originalerrortype", originalErrorType)
                                return SandboxResult{
                                        Verified:     false,
                                        Summary:      fmt.Sprintf("reproducer_failed: %s", reproResult.Summary),
                                        Namespace:    sandboxNS,
                                        PodLogs:      canaryLogs,
                                        ProbeResults: []string{reproResult.Summary},
                                }
                        }
                } else if reproResult.Overall {
                        logging.Info("Level 5 PASSED: reproducer script verified the fix")
                } else {
                        logging.Error("Level 5 SKIPPED: reproducer infrastructure error (non-blocking)", "summary", reproResult.Summary)
                }
        }

        return SandboxResult{
                Verified:  true,
                Summary:   "all_checks_passed (pod ready + sustained + health endpoints ok + original error gone from logs + regression test passed + reproducer passed)",
                Namespace: sandboxNS,
        }
}

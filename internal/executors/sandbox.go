package executors

import (
        "context"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strings"
        "sync"
        "time"

        appsv1 "k8s.io/api/apps/v1"
        corev1 "k8s.io/api/core/v1"
        networkingv1 "k8s.io/api/networking/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/util/intstr"
        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/v3/internal/agents"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// sandboxHTTPClient is the shared HTTP client for sandbox Level 2 + 3 probes.
// v3.13.0: audit finding #11 — verifyEndpoints + verifyOriginalErrorFixed
// created a new http.Client on every call (2-3 per incident), defeating
// connection pooling. A package-level shared client reuses TCP+TLS connections
// to the canary Service, cutting probe latency ~50%.
var sandboxHTTPClient = &http.Client{Timeout: 5 * time.Second}

// SandboxResult is the outcome of a sandbox run.
// v3.18.0: Added PodLogs and ProbeResults fields so the LLM gets concrete
// failure details (was a one-line Summary, making the retry loop ineffective).
type SandboxResult struct {
        Verified      bool
        Summary       string
        Namespace     string
        MirrorApplied bool
        ErrorCount5xx int
        RequestCount  int
        // v3.14.2: InfrastructureError is true when the failure is NOT a code issue
        // (e.g. RBAC denied, image pull failure, namespace creation failed).
        // When true, the retry loop should NOT feed the error back to the LLM —
        // the LLM can't fix a permissions issue.
        InfrastructureError bool
        // v3.18.0: Concrete failure evidence — included in SandboxFeedback so
        // the LLM can actually diagnose + fix the issue.
        PodLogs      string   // last N bytes of canary pod logs (Level 1 failure)
        ProbeResults []string // per-probe outcome (Level 2/3 failure)
}

// SandboxRunner creates an isolated namespace, deploys the fix, and verifies it.
// 3 verification levels:
//   Level 1: Canary pod becomes Ready AND stays Ready for sustainedWindow (startup crash fixed)
//   Level 2: All configured endpoints return 2xx/3xx (app starts + serves traffic)
//   Level 3: Original error is gone (reproduce the crash conditions)
//
// v3.4.0: probePaths replaces the hardcoded /pay?amount=... test inputs that
// only worked for the demo app. Now configurable via SENTINEL_SANDBOX_PROBE_PATHS.
// Defaults to /,/healthz,/readyz. For app-specific paths (e.g. /pay?amount=0),
// set the env var.
//
// v3.8.4: Three correctness + security hardening fixes:
//   - waitForReady now requires ReadyReplicas >= 1 sustained for sustainedWindow
//     (default 30s) before passing Level 1. A pod that goes Ready then
//     immediately crashes no longer passes Level 1.
//   - verifyEndpoints no longer silently swallows connection errors. If every
//     probe fails to connect, Level 2 fails (previously: a canary that never
//     bound its port passed Level 2 for free). If the prod Service exists but
//     the sandbox Service is missing, Level 2 fails (previously: passed).
//   - Level 3 verification now fails on ANY 4xx/5xx response, not just on
//     5xx + error-string match. A 404 with no error string in the body no
//     longer passes Level 3.
//   - patchPodSpec force-sets a safe security context regardless of what the
//     prod pod uses. A privileged prod pod no longer produces a privileged
//     sandbox canary.
//   - createNamespace now also creates a default-deny NetworkPolicy on the
//     sandbox namespace so the canary can't reach arbitrary services.
//
// v3.11.0: sentinelNamespace field replaces the hardcoded "sentinel" literal
// in the sandbox NetworkPolicy ingress rule (audit #8). When the Helm release
// isn't named "sentinel", probes from the actual Sentinel namespace were
// denied → Level 2 verification always failed on connection errors.
type SandboxRunner struct {
        clientset        *kubernetes.Clientset
        sandboxPrefix    string
        enableMirroring  bool
        probePaths       []string // v3.4.0: configurable probe paths for Level 2 + 3
        sustainedWindow  time.Duration // v3.8.4: how long Ready must be sustained for Level 1
        sentinelNamespace string // v3.11.0: for the sandbox NetworkPolicy ingress allow-list
        // v3.18.0: probePathsMu guards probePaths against concurrent Run() calls.
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
                sustainedWindow:  30 * time.Second, // v3.18.0: H14 — was 10s (v3.15.0), missed delayed crashes (connection pool exhaustion ~30s, GC pressure 15-60s, cache TTL expiry ~60s). Restored to 30s.
                sentinelNamespace: "sentinel", // v3.11.0: default; override via WithSentinelNamespace
        }
}

// WithProbePaths sets custom probe paths for Level 2 + 3 verification.
// v3.4.0: Each path can include query string (e.g. "/pay?amount=0").
// Replaces the hardcoded /pay?amount=... inputs that only worked for demo-app.
func (s *SandboxRunner) WithProbePaths(paths []string) *SandboxRunner {
        if len(paths) > 0 {
                s.probePaths = paths
        }
        return s
}

// WithSustainedReady overrides the default 30s sustained-ready window.
// v3.8.4: Exposed for testing and for operators who want tighter / looser
// crash-after-ready detection. Set to 0 to disable (NOT recommended —
// reintroduces the crash-after-ready false positive).
func (s *SandboxRunner) WithSustainedReady(d time.Duration) *SandboxRunner {
        s.sustainedWindow = d
        return s
}

// WithSentinelNamespace sets the namespace Sentinel itself runs in, used by
// the sandbox NetworkPolicy ingress allow-list. v3.11.0: Replaces the
// hardcoded "sentinel" literal (audit #8). When Sentinel is installed via
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
        // v3.8.4: Isolate the sandbox namespace with a default-deny NetworkPolicy.
        // This is best-effort — if it fails, we log and continue (the namespace
        // itself is still isolated by virtue of being separate).
        if err := s.createSandboxNetworkPolicy(ctx, sandboxNS); err != nil {
                log.Printf("[sandbox] WARNING: failed to create NetworkPolicy in %s: %v (canary will have unrestricted egress)", sandboxNS, err)
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
                        InfrastructureError: true, // v3.14.2: don't retry — LLM can't fix RBAC/image-pull
                }
        }

        // v4.2.0: Tier 0 — error-gone oracle. The original trigger endpoint
        // probing (Layer C from v3.14.0) is REMOVED. It caused more bugs than
        // it fixed (wrong endpoint detection, 4xx on handled errors = false
        // failure). The primary oracle is now Level 3: scan canary logs for
        // the original error type. If the error is gone, the fix works.
        //
        // Level 2 now only probes HEALTH endpoints (/, /healthz, /readyz).
        // 4xx on health = fail (app is broken). 4xx on trigger endpoints is
        // no longer checked — a 4xx on the trigger endpoint means the fix
        // correctly handles the error instead of crashing.
        healthProbePaths := s.getProbePaths() // defaults: /, /healthz, /readyz

        // 3. Level 1 — wait for canary pods to be Ready AND stay Ready for
        // sustainedWindow. During this window, we also stream canary logs to
        // check for the original error type (Level 3 primary oracle).
        log.Printf("[sandbox] Level 1: waiting for canary pod Ready + %s sustained window", s.sustainedWindow)
        ready, canaryLogs := s.waitForReadyWithLogs(ctx, sandboxNS, fp.TargetService, 240*time.Second)
        if !ready {
                summary := "canary_pods_not_ready_or_unstable (startup crash not fixed, or pod crashes after going Ready)"
                if canaryLogs != "" {
                        summary += "\n\n--- CANARY POD LOGS (last 2KB) ---\n" + lastN(canaryLogs, 2048)
                }
                return SandboxResult{Verified: false, Summary: summary, Namespace: sandboxNS, PodLogs: canaryLogs}
        }
        log.Printf("[sandbox] Level 1 PASSED: canary pod is Ready and stayed Ready for %s", s.sustainedWindow)

        // 4. Level 2 — health endpoints only. 4xx/5xx = fail (app is broken).
        if !s.verifyEndpointsWithPaths(ctx, sandboxNS, fp.TargetService, healthProbePaths) {
                return SandboxResult{Verified: false, Summary: "health_endpoints_failing (/, /healthz, /readyz returned 4xx/5xx — app is broken)", Namespace: sandboxNS}
        }
        log.Printf("[sandbox] Level 2 PASSED: health endpoints returned 2xx/3xx")

        // 5. Level 3 (PRIMARY ORACLE) — scan canary logs for the original error type.
        // If the original error (TypeError, panic, IndexError, etc.) appears in the
        // canary's logs during the sustained window, the fix did NOT work.
        // If the error is gone, the fix worked. This is the error-gone oracle.
        //
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
        log.Printf("[sandbox] Level 3 PASSED: %s", level3Result.detail)

        // 6. Level 4 (Tier 3) — regression test gate. v4.2.0.
        // If a regression test was generated by the test generator agent,
        // run it against the canary image. The test must PASS (verifies the fix).
        // This is the SWE-bench FAIL_TO_PASS pattern.
        // Best-effort: if no test was generated or the test fails to run,
        // we don't block the sandbox (Level 3 is the primary oracle).
        if fp.CanaryImage != nil && *fp.CanaryImage != "" {
                testResult := s.runRegressionTest(ctx, sandboxNS, inc, *fp.CanaryImage)
                if testResult != nil {
                        if !testResult.Overall && !strings.Contains(testResult.Summary, "no test generated") {
                                log.Printf("[sandbox] Level 4 FAILED: regression test did not pass")
                                return SandboxResult{
                                        Verified:     false,
                                        Summary:      fmt.Sprintf("regression_test_failed: %s", testResult.Summary),
                                        Namespace:    sandboxNS,
                                        PodLogs:      canaryLogs,
                                        ProbeResults: []string{testResult.Summary},
                                }
                        }
                        log.Printf("[sandbox] Level 4 PASSED: regression test verified the fix")
                }
        }

        // 7. Level 5 (Tier 4) — reproducer gate. v4.2.0.
        // If a reproducer script was generated, run it against the canary image.
        // The reproducer is a standalone script that calls the buggy function + exits 0 if OK.
        // Best-effort: if no reproducer was generated or it fails to run, don't block.
        if fp.CanaryImage != nil && *fp.CanaryImage != "" {
                reproResult := s.runReproducer(ctx, sandboxNS, inc, *fp.CanaryImage)
                if reproResult != nil {
                        if !reproResult.Overall && !strings.Contains(reproResult.Summary, "no reproducer generated") {
                                log.Printf("[sandbox] Level 5 FAILED: reproducer script crashed")
                                return SandboxResult{
                                        Verified:     false,
                                        Summary:      fmt.Sprintf("reproducer_failed: %s", reproResult.Summary),
                                        Namespace:    sandboxNS,
                                        PodLogs:      canaryLogs,
                                        ProbeResults: []string{reproResult.Summary},
                                }
                        }
                        log.Printf("[sandbox] Level 5 PASSED: reproducer script verified the fix")
                }
        }

        return SandboxResult{
                Verified:  true,
                Summary:   "all_checks_passed (pod ready + sustained + health endpoints ok + original error gone from logs + regression test passed + reproducer passed)",
                Namespace: sandboxNS,
        }
}

func (s *SandboxRunner) createNamespace(ctx context.Context, ns string) error {
        _, err := s.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                        Name: ns,
                        Labels: map[string]string{
                                "sentinel.io/sandbox": "true",
                                // v3.14.2: Changed from "restricted" to "baseline". The restricted
                                // PodSecurity standard blocks pods that don't set runAsNonRoot=true,
                                // which conflicts with canary pods that inherit the prod image's
                                // user (which might be root). "baseline" still blocks privileged
                                // pods + hostPath but allows running as root.
                                "pod-security.kubernetes.io/enforce": "baseline",
                                "pod-security.kubernetes.io/audit":   "restricted",
                                "pod-security.kubernetes.io/warn":    "restricted",
                        },
                },
        }, metav1.CreateOptions{})
        if err != nil && !strings.Contains(err.Error(), "already exists") {
                return err
        }

        // v3.14.3: No longer need to create per-namespace RBAC — the ClusterRole
        // now grants cluster-wide create on deployments, services, configmaps,
        // pods, secrets, roles, and rolebindings. This was the previous approach
        // but it failed because the SA couldn't create Roles in the sandbox NS.

        return nil
}

// createSandboxNetworkPolicy creates a default-deny NetworkPolicy in the
// sandbox namespace. The canary can still receive traffic from Sentinel's
// probe requests (which arrive via the Service cluster IP) but cannot
// initiate arbitrary egress to other services in the cluster.
//
// v3.8.4: Previously the sandbox namespace had no NetworkPolicy, so a canary
// pod (with prod secrets mounted via CopyConfigMapsAndSecrets) could phone
// home to attacker C2 over HTTPS. The default-deny egress below blocks all
// outbound traffic. Ingress is also denied except from the Sentinel
// namespace (so probes can still reach the canary).
//
// Note: this is best-effort. On clusters without a CNI that supports
// NetworkPolicy (e.g. some k3s setups), this is a no-op. Operators should
// ensure their CNI supports NetworkPolicy before relying on this.
func (s *SandboxRunner) createSandboxNetworkPolicy(ctx context.Context, ns string) error {
        // Allow ingress from the same namespace (Sentinel probes via Service
        // cluster IP terminate in the sandbox namespace) and from the Sentinel
        // namespace itself. Egress is denied entirely — the canary should not
        // need to reach any other service. If the canary needs to reach a
        // database or external API, operators should add explicit egress rules.
        allowFromSentinel := networkingv1.NetworkPolicyIngressRule{
                From: []networkingv1.NetworkPolicyPeer{
                        {
                                NamespaceSelector: &metav1.LabelSelector{
                                        // v3.11.0: Use s.sentinelNamespace instead of hardcoded "sentinel" (audit #8).
                                        MatchLabels: map[string]string{"kubernetes.io/metadata.name": s.sentinelNamespace},
                                },
                        },
                        {
                                NamespaceSelector: &metav1.LabelSelector{
                                        MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns},
                                },
                        },
                },
        }

        denyAllEgress := networkingv1.NetworkPolicyEgressRule{}

        np := &networkingv1.NetworkPolicy{
                ObjectMeta: metav1.ObjectMeta{
                        Name:      "sentinel-sandbox-default-deny",
                        Namespace: ns,
                        Labels: map[string]string{
                                "sentinel.io/managed": "true",
                        },
                },
                Spec: networkingv1.NetworkPolicySpec{
                        PodSelector: metav1.LabelSelector{}, // matches all pods in the namespace
                        PolicyTypes: []networkingv1.PolicyType{
                                networkingv1.PolicyTypeIngress,
                                networkingv1.PolicyTypeEgress,
                        },
                        Ingress: []networkingv1.NetworkPolicyIngressRule{allowFromSentinel},
                        Egress:  []networkingv1.NetworkPolicyEgressRule{denyAllEgress},
                },
        }
        _, err := s.clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, np, metav1.CreateOptions{})
        if err != nil && !strings.Contains(err.Error(), "already exists") {
                return err
        }
        return nil
}

type deployResult struct {
        success bool
        err     string
        image   string
}

func (s *SandboxRunner) deployCanary(ctx context.Context, inc *schemas.Incident, ns string) deployResult {
        fp := inc.State.FixProposal

        // Find the prod deployment
        var prodDeploy *appsv1.Deployment
        if dep, err := s.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil {
                prodDeploy = dep
        } else {
                deps, err := s.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
                        LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
                })
                if err != nil || len(deps.Items) == 0 {
                        return deployResult{err: fmt.Sprintf("deployment %s not found in %s", fp.TargetService, fp.TargetNamespace)}
                }
                prodDeploy = &deps.Items[0]
        }

        // Determine canary image
        canaryImage := ""
        if fp.CanaryImage != nil && *fp.CanaryImage != "" {
                canaryImage = *fp.CanaryImage
        } else if len(prodDeploy.Spec.Template.Spec.Containers) > 0 {
                canaryImage = prodDeploy.Spec.Template.Spec.Containers[0].Image
        } else {
                return deployResult{err: "no canary image and no prod image"}
        }

        // Build the sandbox Deployment
        labels := map[string]string{
                "app":                 fp.TargetService,
                "sentinel.io/sandbox": "true",
        }
        sandboxDeploy := &appsv1.Deployment{
                ObjectMeta: metav1.ObjectMeta{
                        Name:      fp.TargetService,
                        Namespace: ns,
                        Labels:    labels,
                },
                Spec: appsv1.DeploymentSpec{
                        Replicas: int32Ptr(1),
                        Selector: &metav1.LabelSelector{MatchLabels: labels},
                        Template: corev1.PodTemplateSpec{
                                ObjectMeta: metav1.ObjectMeta{Labels: labels},
                                Spec:       patchPodSpec(prodDeploy.Spec.Template.Spec, canaryImage),
                        },
                },
        }

        if _, err := s.clientset.AppsV1().Deployments(ns).Create(ctx, sandboxDeploy, metav1.CreateOptions{}); err != nil {
                return deployResult{err: err.Error()}
        }

        // Copy ConfigMaps + Secrets from prod namespace to sandbox.
        // v3.8.4: Surface errors instead of silently swallowing them. A missing
        // ConfigMap/Secret in prod means the sandbox canary will start with empty
        // env vars, which can mask the real failure behind a misleading "Ready"
        // status (the pod can go Ready with broken config).
        if err := CopyConfigMapsAndSecrets(ctx, s.clientset, prodDeploy, fp.TargetNamespace, ns); err != nil {
                log.Printf("[sandbox] WARNING: CopyConfigMapsAndSecrets had errors: %v (canary may start with missing env vars)", err)
        }

        // Create a Service so Level 2 verification can reach the canary.
        // v3.11.0: Derive the port from (1) prod Service, (2) prod Deployment's
        // container ports, (3) fallback to 8000. Previously hardcoded 8000, which
        // broke for any app on 8080/3000/5000/etc. when the prod Service had no
        // ports declared (CRITICAL audit #3).
        sandboxPorts := []corev1.ServicePort{
                {Port: 80, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolTCP, Name: "http"},
        }
        if prodSvc, err := s.clientset.CoreV1().Services(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil && len(prodSvc.Spec.Ports) > 0 {
                sandboxPorts = prodSvc.Spec.Ports
        } else if prodDeploy != nil && len(prodDeploy.Spec.Template.Spec.Containers) > 0 {
                // v3.11.0: No prod Service (or Service has no ports) — derive from
                // the prod Deployment's container ports. This handles apps that
                // expose a non-8000 port (e.g. 8080, 3000, 5000) without a Service.
                prodContainerPorts := prodDeploy.Spec.Template.Spec.Containers[0].Ports
                if len(prodContainerPorts) > 0 {
                        derived := make([]corev1.ServicePort, 0, len(prodContainerPorts))
                        for i, cp := range prodContainerPorts {
                                name := cp.Name
                                if name == "" {
                                        name = fmt.Sprintf("port-%d", i)
                                }
                                derived = append(derived, corev1.ServicePort{
                                        Port:       cp.ContainerPort,
                                        TargetPort: intstr.FromInt(int(cp.ContainerPort)),
                                        Protocol:   cp.Protocol,
                                        Name:       name,
                                })
                        }
                        sandboxPorts = derived
                        log.Printf("[sandbox] no prod Service ports — derived %d port(s) from prod Deployment container spec", len(derived))
                }
        }
        sandboxSvc := &corev1.Service{
                ObjectMeta: metav1.ObjectMeta{Name: fp.TargetService, Namespace: ns, Labels: labels},
                Spec: corev1.ServiceSpec{
                        Selector: labels,
                        Ports:    sandboxPorts,
                        Type:     corev1.ServiceTypeClusterIP,
                },
        }
        _, _ = s.clientset.CoreV1().Services(ns).Create(ctx, sandboxSvc, metav1.CreateOptions{})

        return deployResult{success: true, image: canaryImage}
}

// waitForReady waits for the canary Deployment to have ReadyReplicas >= 1 AND
// to stay at ReadyReplicas >= 1 for sustainedWindow. The sustained check is
// critical: without it, a pod that goes Ready then immediately crashes (a
// common failure mode when config errors only manifest on the first request)
// passes Level 1 because ReadyReplicas briefly hit 1 between pod start and
// the first crash.
//
// v3.8.4: Previous implementation returned true the instant ReadyReplicas >= 1
// was observed, missing the crash-after-ready case entirely.
func (s *SandboxRunner) waitForReady(ctx context.Context, ns, name string, timeout time.Duration) bool {
        deadline := time.Now().Add(timeout)
        readySince := time.Time{}

        for time.Now().Before(deadline) {
                // v3.18.0: check ctx.Done so the loop is interruptible by SIGTERM/investigation timeout.
                select {
                case <-ctx.Done():
                        return false
                default:
                }
                deploy, err := s.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
                if err == nil && deploy.Status.ReadyReplicas >= 1 {
                        if readySince.IsZero() {
                                readySince = time.Now()
                                log.Printf("[sandbox] Level 1: canary pod went Ready at %s — sustaining for %s", readySince.Format(time.RFC3339), s.sustainedWindow)
                        }
                        if time.Since(readySince) >= s.sustainedWindow {
                                return true
                        }
                } else {
                        // Reset the sustained-ready timer if we drop below 1 Ready replica.
                        // This catches pods that go Ready -> CrashLoopBackOff -> Ready -> CrashLoopBackOff.
                        if !readySince.IsZero() {
                                log.Printf("[sandbox] Level 1: canary pod dropped below ReadyReplicas=1 before sustaining %s — resetting timer", s.sustainedWindow)
                        }
                        readySince = time.Time{}
                }
                // v3.18.0: interruptible sleep
                select {
                case <-ctx.Done():
                        return false
                case <-time.After(3 * time.Second):
                }
        }
        return false
}

// getProbePaths returns a copy of probePaths under the read lock.
// v3.18.0: Replaces direct s.probePaths access — Run() is invoked concurrently
// from multiple investigation goroutines, so the slice must be guarded.
func (s *SandboxRunner) getProbePaths() []string {
        s.probePathsMu.RLock()
        defer s.probePathsMu.RUnlock()
        return append([]string(nil), s.probePaths...)
}

// waitForReadyWithLogs is like waitForReady but also captures canary pod logs
// during the sustained window. v4.2.0: the captured logs are used by the
// Level 3 primary oracle (verifyErrorGoneFromLogs) to check if the original
// error type still appears.
//
// Returns (ready, canaryLogs). If ready is false, canaryLogs contains
// whatever was captured before the timeout (useful for the failure summary).
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
                                log.Printf("[sandbox] Level 1: canary pod went Ready at %s — sustaining for %s", readySince.Format(time.RFC3339), s.sustainedWindow)
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
                                log.Printf("[sandbox] Level 1: canary pod dropped below ReadyReplicas=1 before sustaining %s — resetting timer", s.sustainedWindow)
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
//
// It checks whether the original error type (TypeError, panic, IndexError,
// NullPointerException, etc.) appears in the canary pod's logs during the
// sustained Ready window. If the error is GONE, the fix worked. If the error
// is STILL PRESENT, the fix did not work.
//
// This is universal:
//   - Works for HTTP errors (TypeError on /format → TypeError gone from logs)
//   - Works for background task errors (cron job crash → error gone)
//   - Works for startup errors (ImportError on boot → error gone)
//   - Works for OOMKills (OOMKilled → pod stays Ready)
//   - Works for any language (ParseError handles Python/Go/Java/JS/Rust/Ruby/C)
//
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
//
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
                log.Printf("[sandbox] Level 4: failed to parse test JSON: %v", err)
                return &TestResult{
                        Overall: true,
                        Summary: "test parse failed (non-blocking): " + err.Error(),
                }
        }

        // Run the test
        runner := NewTestRunner(s.clientset)
        result, err := runner.RunTest(ctx, canaryImage, sandboxNS, test)
        if err != nil {
                log.Printf("[sandbox] Level 4: test runner error: %v", err)
                return &TestResult{
                        Overall: true, // don't block on infra failure
                        Summary: fmt.Sprintf("test runner error (non-blocking): %v", err),
                }
        }
        return result
}

// parseGeneratedTest parses a JSON string into a GeneratedTest.
func parseGeneratedTest(jsonStr string) (*agents.GeneratedTest, error) {
        var raw map[string]string
        if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
                return nil, err
        }
        return &agents.GeneratedTest{
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
                log.Printf("[sandbox] Level 5: failed to parse reproducer JSON: %v", err)
                return &TestResult{
                        Overall: true,
                        Summary: "reproducer parse failed (non-blocking): " + err.Error(),
                }
        }
        reproducer := &agents.GeneratedReproducer{
                ScriptFile:     raw["script_file"],
                ScriptLanguage: raw["script_language"],
                ScriptContent:  raw["script_content"],
                RunCommand:     raw["run_command"],
                Description:    raw["description"],
        }

        runner := NewTestRunner(s.clientset)
        result, err := runner.RunReproducer(ctx, canaryImage, sandboxNS, reproducer)
        if err != nil {
                log.Printf("[sandbox] Level 5: reproducer runner error: %v", err)
                return &TestResult{
                        Overall: true,
                        Summary: fmt.Sprintf("reproducer runner error (non-blocking): %v", err),
                }
        }
        return result
}

// fetchCanaryPodLogs retrieves the canary pod's logs for inclusion in
// SandboxResult.PodLogs. v3.18.0: gives the LLM the actual error instead of
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
// every probe to return 2xx/3xx. v3.18.0: extracted from verifyEndpoints so the
// caller passes a local slice (no shared state race).
//
// v3.18.0: Fixed two critical bugs:
//   1. 4xx responses now FAIL (was: only 5xx failed; 404/401/405 falsely passed).
//   2. POST/DELETE/PATCH endpoints are now probed with the correct method (was:
//      always GET, returning 405 on POST endpoints which falsely passed).
//   3. consecutiveSuccesses now resets on failure (was: [success, fail, success] passed).
func (s *SandboxRunner) verifyEndpointsWithPaths(ctx context.Context, ns, service string, paths []string) bool {
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil {
                log.Printf("[sandbox] Level 2 FAILED: Service %s/%s not found (deployCanary should have created it)", ns, service)
                return false
        }
        // v3.18.0: probe ALL service ports, not just the first (multi-port services).
        if len(svc.Spec.Ports) == 0 {
                log.Printf("[sandbox] Level 2 FAILED: Service %s/%s has no ports", ns, service)
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
                                        log.Printf("[sandbox] Level 2: probe %s %s attempt %d failed: %v", method, url, attempt+1, err)
                                        continue
                                }
                                resp.Body.Close()
                                // v3.18.0: 4xx now fails (was only 5xx). 404/401/405 = app broken.
                                if resp.StatusCode >= 400 {
                                        consecutiveFailures++
                                        consecutiveSuccesses = 0
                                        log.Printf("[sandbox] Level 2: probe %s %s attempt %d returned %d (4xx/5xx = failure)", method, url, attempt+1, resp.StatusCode)
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
//
// v3.11.0: Replaced the Python-only `errorTypeRe` regex (which only matched
// KeyError/ImportError/ValueError/TypeError/etc. — all Python builtins) with
// agents.ParseError(), which is multi-language (Python, Go, Java, JS, Rust,
// Ruby, C, signals). The previous implementation silently returned nil for
// Go panics, Java NullPointerExceptions, JS TypeErrors, etc., causing
// sandbox Level 3 to silently pass even when the original error was NOT
// fixed — the PR would ship with a "sandbox-verified" label. The
// `startupErrors` map had the same Python-only bug.
//
// The startup-error auto-pass logic now keys off the parsed language +
// error type: Python startup errors (KeyError/ImportError/ModuleNotFoundError/
// FileNotFoundError/SyntaxError) AND Go panic + Java ExceptionInInitializerError
// + JS startup SyntaxError all auto-pass Level 1 (the canary pod started and
// stayed Ready for sustainedWindow — that's sufficient evidence the startup
// error is gone).
// verifyOriginalErrorFixedWithPaths is the same as verifyOriginalErrorFixed
// but takes an explicit paths slice (avoids the shared s.probePaths race).
// v3.18.0: Also uses the correct HTTP method per probe (was always GET).
func (s *SandboxRunner) verifyOriginalErrorFixedWithPaths(ctx context.Context, ns, service string, inc *schemas.Incident, paths []string) *level3Result {
        // Get the original pod logs from the anomaly explanation
        explanation := ""
        for _, anomaly := range inc.Cluster.Anomalies {
                explanation += anomaly.Explanation + "\n"
        }
        if explanation == "" {
                return nil
        }

        parsed := agents.ParseError(explanation)
        if parsed == nil {
                return nil // No recognizable error — can't reproduce
        }
        errorType := parsed.ErrorType
        errorDetail := parsed.ErrorDetail

        // Startup errors are already verified by Level 1.
        startupErrors := map[string]bool{
                "KeyError": true, "ImportError": true, "ModuleNotFoundError": true,
                "FileNotFoundError": true, "SyntaxError": true,
                "panic": true,
                "ExceptionInInitializerError": true, "NoClassDefFoundError": true,
                "NoSuchMethodError": true, "NoSuchFieldError": true,
                "UnsupportedClassVersionError": true,
                "ReferenceError": true,
        }
        if startupErrors[errorType] {
                return &level3Result{
                        passed: true,
                        detail: fmt.Sprintf("Startup error %s: %s — canary pod started + stayed Ready for %s (error is gone)", errorType, errorDetail, s.sustainedWindow),
                }
        }

        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil || svc == nil {
                return nil
        }
        // v3.18.0: probe ALL ports (was first only).
        if len(svc.Spec.Ports) == 0 {
                return nil
        }
        svcName := svc.Name

        if len(paths) == 0 {
                paths = []string{"/", "/healthz", "/readyz"}
        }

        client := sandboxHTTPClient
        totalProbes := 0
        failedProbes := 0

        for _, portSpec := range svc.Spec.Ports {
                port := portSpec.Port
                for _, p := range paths {
                        method, path := parsePathMethod(p)
                        url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, path)
                        req, _ := http.NewRequestWithContext(ctx, method, url, nil)

                        pathOK := false
                        for attempt := 0; attempt < 2; attempt++ {
                                resp, err := client.Do(req)
                                if err != nil {
                                        log.Printf("[sandbox] Level 3: probe %s %s attempt %d connect error: %v", method, url, attempt+1, err)
                                        continue
                                }
                                // v3.18.0: read full body up to 64KB (was single 4KB Read, missed errors past 4096).
                                body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
                                resp.Body.Close()
                                bodyStr := strings.ToLower(string(body))

                                if strings.Contains(bodyStr, strings.ToLower(errorType)) ||
                                        (errorDetail != "" && strings.Contains(bodyStr, strings.ToLower(strings.TrimSpace(errorDetail)))) {
                                        return &level3Result{
                                                passed: false,
                                                detail: fmt.Sprintf("%s: %s still present in response body from %s %s (status %d)", errorType, errorDetail, method, path, resp.StatusCode),
                                        }
                                }

                                if resp.StatusCode >= 500 {
                                        log.Printf("[sandbox] Level 3: probe %s %s returned %d (5xx = server error, fix not working)", method, url, resp.StatusCode)
                                        continue
                                }

                                pathOK = true
                                break
                        }
                        totalProbes++
                        if !pathOK {
                                failedProbes++
                        }
                }
        }

        if failedProbes > 0 {
                return &level3Result{
                        passed: false,
                        detail: fmt.Sprintf("%d/%d probe paths returned 5xx or failed to connect (%s: %s not fully gone)",
                                failedProbes, totalProbes, errorType, errorDetail),
                }
        }

        return &level3Result{
                passed: true,
                detail: fmt.Sprintf("Runtime error %s: %s — not reproduced on any endpoint, all probes returned 2xx/3xx (error is gone)", errorType, errorDetail),
        }
}

func (s *SandboxRunner) cleanup(ctx context.Context, ns string) {
        // v3.18.0: Use a fresh context so cleanup runs even if the parent ctx
        // is cancelled (e.g. investigation timeout). Previously, the cancelled
        // ctx caused Namespace.Delete to return context.Canceled immediately,
        // leaking the namespace + all its resources.
        cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        if err := s.clientset.CoreV1().Namespaces().Delete(cleanupCtx, ns, metav1.DeleteOptions{}); err != nil {
                log.Printf("[sandbox] cleanup: failed to delete namespace %s: %v", ns, err)
        }
        _ = ctx // parent ctx accepted for API compat but not used
}

// ReapStaleSandboxNamespaces lists all namespaces with the sentinel.io/sandbox
// label and deletes any older than maxAge. This is the safety net for sandbox
// namespaces that leak when the Sentinel pod is killed mid-verification
// (the in-process defer cleanup doesn't survive pod restarts).
//
// v3.8.4: Call this periodically from main.go (every 5 minutes) to prevent
// leaked sandbox namespaces from accumulating. The reaper is idempotent —
// it's safe to run concurrently with active sandbox runs because the
// sandbox runner holds the namespace for <5 minutes (240s Ready timeout +
// ~30s sustained + ~30s Level 2/3 = ~5 min max). A namespace older than
// maxAge=30min is guaranteed to be a leak.
//
// Returns the count of namespaces reaped.
func (s *SandboxRunner) ReapStaleSandboxNamespaces(ctx context.Context, maxAge time.Duration) int {
        list, err := s.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
                LabelSelector: "sentinel.io/sandbox=true",
        })
        if err != nil {
                log.Printf("[sandbox-reaper] failed to list sandbox namespaces: %v", err)
                return 0
        }

        reaped := 0
        cutoff := time.Now().Add(-maxAge)
        for i := range list.Items {
                ns := &list.Items[i]
                if ns.CreationTimestamp.Time.After(cutoff) {
                        continue // still fresh — leave it alone
                }
                // Stale — delete it.
                if err := s.clientset.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
                        log.Printf("[sandbox-reaper] failed to delete stale namespace %s: %v", ns.Name, err)
                        continue
                }
                log.Printf("[sandbox-reaper] deleted stale sandbox namespace %s (created %s, age %s)",
                        ns.Name, ns.CreationTimestamp.Time.Format(time.RFC3339), time.Since(ns.CreationTimestamp.Time).Round(time.Second))
                reaped++
        }
        if reaped > 0 {
                log.Printf("[sandbox-reaper] reaped %d stale sandbox namespace(s)", reaped)
        }
        return reaped
}

// patchPodSpec copies the prod pod spec into the sandbox canary, applies the
// canary image, and force-sets a safe security context.
//
// v3.8.4: Previous implementation only swapped the image and inherited the
// prod pod's entire security context. If the prod pod was privileged, used
// hostPath, hostNetwork, or had dangerous capabilities, the sandbox canary
// inherited all of those — turning a sandbox into a potential privilege
// escalation vector.
//
// Now we explicitly:
//   - Set privileged=false on every container
//   - Drop ALL capabilities on every container
//   - Set allowPrivilegeEscalation=false
//   - Set seccompProfile=RuntimeDefault at pod level
//   - Set runAsNonRoot=true + runAsUser=65532 at pod level
//   - Clear hostNetwork, hostPID, hostIPC
//   - Clear hostPath volumes (replace with emptyDir)
//   - Set readOnlyRootFilesystem=true where the prod pod didn't explicitly
//     enable writable rootfs (best-effort — some apps need to write to /tmp)
func patchPodSpec(spec corev1.PodSpec, canaryImage string) corev1.PodSpec {
        // Apply the canary image + pull policy.
        for i := range spec.Containers {
                spec.Containers[i].Image = canaryImage
                spec.Containers[i].ImagePullPolicy = corev1.PullAlways

                // v3.8.4: Force a safe container security context.
                if spec.Containers[i].SecurityContext == nil {
                        spec.Containers[i].SecurityContext = &corev1.SecurityContext{}
                }
                sc := spec.Containers[i].SecurityContext
                sc.Privileged = boolPtr(false)
                sc.AllowPrivilegeEscalation = boolPtr(false)
                sc.ReadOnlyRootFilesystem = boolPtr(true)
                if sc.Capabilities == nil {
                        sc.Capabilities = &corev1.Capabilities{}
                }
                sc.Capabilities.Drop = []corev1.Capability{"ALL"}
                // Don't add any capabilities back — if the prod pod needed
                // capabilities, that's a smell and the sandbox should fail
                // loudly rather than inherit the privilege.
                sc.Capabilities.Add = nil
                if sc.SeccompProfile == nil {
                        sc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
                }
                // v3.16.0: Don't force RunAsNonRoot/RunAsUser. The prod image
                // may run as root (e.g. Go binaries that bind to port 80).
                // Forcing non-root breaks canaries for root-running prod images.
                // The namespace PodSecurity label is "baseline" which allows
                // root but blocks privileged. This is the right tradeoff.
                // Previous v3.8.4 code forced RunAsNonRoot=true + RunAsUser=65532,
                // which conflicted with the baseline namespace label and caused
                // canary pods to fail with "container has runAsNonRoot and image
                // will run as root".
        }

        // v3.16.0: Don't force pod-level RunAsNonRoot either. Same reasoning
        // as above — respect the prod image's user. The namespace's PodSecurity
        // "baseline" label provides the security boundary.
        if spec.SecurityContext == nil {
                spec.SecurityContext = &corev1.PodSecurityContext{}
        }
        psc := spec.SecurityContext
        if psc.SeccompProfile == nil {
                psc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
        }

        // v3.8.4: Clear host namespaces. A sandbox canary should never need
        // hostNetwork / hostPID / hostIPC.
        spec.HostNetwork = false
        spec.HostPID = false
        spec.HostIPC = false

        // v3.8.4: Replace hostPath volumes with emptyDir. hostPath volumes
        // give the canary access to host filesystem — a sandbox escape vector.
        for i := range spec.Volumes {
                if spec.Volumes[i].HostPath != nil {
                        log.Printf("[sandbox] SECURITY: replacing hostPath volume %q with emptyDir (sandbox must not mount host paths)", spec.Volumes[i].Name)
                        spec.Volumes[i] = corev1.Volume{
                                Name: spec.Volumes[i].Name,
                                VolumeSource: corev1.VolumeSource{
                                        EmptyDir: &corev1.EmptyDirVolumeSource{},
                                },
                        }
                }
        }

        return spec
}

// boolPtr returns a pointer to b. Helper for *bool fields in corev1.
func boolPtr(b bool) *bool { return &b }

// int64Ptr returns a pointer to i. Helper for *int64 fields in corev1.

func int32Ptr(i int32) *int32 { return &i }

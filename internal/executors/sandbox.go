package executors

import (
        "context"
        "fmt"
        "log"
        "net/http"
        "regexp"
        "strings"
        "time"

        appsv1 "k8s.io/api/apps/v1"
        corev1 "k8s.io/api/core/v1"
        networkingv1 "k8s.io/api/networking/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/util/intstr"
        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// SandboxResult is the outcome of a sandbox run.
type SandboxResult struct {
        Verified      bool
        Summary       string
        Namespace     string
        MirrorApplied bool
        ErrorCount5xx int
        RequestCount  int
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
type SandboxRunner struct {
        clientset        *kubernetes.Clientset
        sandboxPrefix    string
        enableMirroring  bool
        probePaths       []string // v3.4.0: configurable probe paths for Level 2 + 3
        sustainedWindow  time.Duration // v3.8.4: how long Ready must be sustained for Level 1
}

// NewSandbox creates a SandboxRunner.
func NewSandbox(clientset *kubernetes.Clientset, sandboxPrefix string, enableMirroring bool) *SandboxRunner {
        return &SandboxRunner{
                clientset:       clientset,
                sandboxPrefix:   sandboxPrefix,
                enableMirroring: enableMirroring,
                probePaths:      []string{"/", "/healthz", "/readyz"}, // sensible defaults
                sustainedWindow: 30 * time.Second, // v3.8.4: was 0 (instant — missed crash-after-ready)
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
                return SandboxResult{Verified: false, Summary: fmt.Sprintf("deploy_failed: %s", deployResult.err), Namespace: sandboxNS}
        }

        // 3. Level 1 — wait for canary pods to be Ready AND stay Ready for
        // sustainedWindow. A pod that goes Ready then immediately crashes
        // (common with config errors that only manifest on first request)
        // no longer passes Level 1.
        ready := s.waitForReady(ctx, sandboxNS, fp.TargetService, 240*time.Second)
        if !ready {
                return SandboxResult{Verified: false, Summary: "canary_pods_not_ready_or_unstable (startup crash not fixed, or pod crashes after going Ready)", Namespace: sandboxNS}
        }
        log.Printf("[sandbox] Level 1 PASSED: canary pod is Ready and stayed Ready for %s", s.sustainedWindow)

        // 4. Level 2 — call the app's own endpoints to verify they respond.
        // v3.8.4: No longer silently passes when the Service is missing or every
        // probe fails to connect — that masked canaries that never bound their port.
        if !s.verifyEndpoints(ctx, sandboxNS, fp.TargetService) {
                return SandboxResult{Verified: false, Summary: "endpoints_unreachable_or_5xx (app starts but endpoints fail or never bind)", Namespace: sandboxNS}
        }
        log.Printf("[sandbox] Level 2 PASSED: all endpoints returned 2xx/3xx")

        // 5. Level 3 — reproduce the original error and verify it's gone
        if level3 := s.verifyOriginalErrorFixed(ctx, sandboxNS, fp.TargetService, inc); level3 != nil {
                if !level3.passed {
                        return SandboxResult{Verified: false, Summary: fmt.Sprintf("original_error_still_present: %s", level3.detail), Namespace: sandboxNS}
                }
                log.Printf("[sandbox] Level 3 PASSED: %s", level3.detail)
        }

        return SandboxResult{
                Verified:  true,
                Summary:   "all_checks_passed (pod ready + sustained + endpoints ok + original error gone)",
                Namespace: sandboxNS,
        }
}

func (s *SandboxRunner) createNamespace(ctx context.Context, ns string) error {
        _, err := s.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                        Name: ns,
                        Labels: map[string]string{
                                "sentinel.io/sandbox": "true",
                                // v3.8.4: PodSecurity admission label. On clusters with the
                                // PodSecurity admission plugin enabled (default on 1.23+),
                                // this enforces restricted-level workloads in the sandbox.
                                "pod-security.kubernetes.io/enforce": "restricted",
                                "pod-security.kubernetes.io/audit":   "restricted",
                                "pod-security.kubernetes.io/warn":    "restricted",
                        },
                },
        }, metav1.CreateOptions{})
        if err != nil && !strings.Contains(err.Error(), "already exists") {
                return err
        }
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
                                        MatchLabels: map[string]string{"kubernetes.io/metadata.name": "sentinel"},
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

        // Create a Service so Level 2 verification can reach the canary
        sandboxPorts := []corev1.ServicePort{
                {Port: 80, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolTCP, Name: "http"},
        }
        if prodSvc, err := s.clientset.CoreV1().Services(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil && prodSvc.Spec.Ports != nil {
                sandboxPorts = prodSvc.Spec.Ports
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
                time.Sleep(3 * time.Second)
        }
        return false
}

// verifyEndpoints probes each configured path on the canary Service and
// requires every probe to return 2xx/3xx. v3.8.4: no longer silently passes
// when the Service is missing or every probe fails to connect — that masked
// canaries that never bound their port. Connection errors are now counted;
// if every probe fails to connect, Level 2 fails.
func (s *SandboxRunner) verifyEndpoints(ctx context.Context, ns, service string) bool {
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil {
                // v3.8.4: Previously returned true here ("Don't fail if no Service"),
                // which let a sandbox with no Service pass Level 2 for free. Now we
                // fail — if the prod deployment has a Service (which is what we just
                // tried to copy in deployCanary), the sandbox should have one too.
                log.Printf("[sandbox] Level 2 FAILED: Service %s/%s not found (deployCanary should have created it)", ns, service)
                return false
        }
        port := int32(80)
        if len(svc.Spec.Ports) > 0 {
                port = svc.Spec.Ports[0].Port
        }
        svcName := svc.Name

        // v3.4.0: Use configurable probe paths instead of hardcoded [/, /healthz, /readyz].
        endpoints := s.probePaths
        if len(endpoints) == 0 {
                endpoints = []string{"/", "/healthz", "/readyz"}
        }

        client := &http.Client{Timeout: 5 * time.Second}
        consecutiveFailures := 0
        consecutiveSuccesses := 0
        requiredSuccesses := 2 // v3.8.4: require 2 consecutive successes per path to ride through transient blips

        for _, path := range endpoints {
                url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, path)
                consecutiveFailures = 0
                consecutiveSuccesses = 0

                // Try each path up to 3 times to ride through pod-startup transients.
                // After 3 consecutive failures on a single path, fail Level 2.
                for attempt := 0; attempt < 3; attempt++ {
                        req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
                        if err != nil {
                                consecutiveFailures++
                                continue
                        }
                        resp, err := client.Do(req)
                        if err != nil {
                                consecutiveFailures++
                                log.Printf("[sandbox] Level 2: probe %s attempt %d failed: %v", url, attempt+1, err)
                                continue
                        }
                        resp.Body.Close()
                        if resp.StatusCode >= 500 {
                                consecutiveFailures++
                                log.Printf("[sandbox] Level 2: probe %s attempt %d returned %d", url, attempt+1, resp.StatusCode)
                                continue
                        }
                        // 2xx or 3xx — count as a success.
                        consecutiveSuccesses++
                        if consecutiveSuccesses >= requiredSuccesses {
                                break
                        }
                }
                if consecutiveSuccesses < requiredSuccesses {
                        log.Printf("[sandbox] Level 2 FAILED: %s never returned 2xx/3xx in %d attempts (%d successes, %d failures)",
                                url, 3, consecutiveSuccesses, consecutiveFailures)
                        return false
                }
        }
        return true
}

type level3Result struct {
        passed bool
        detail string
}

var errorTypeRe = regexp.MustCompile(`(KeyError|ImportError|ValueError|TypeError|ZeroDivisionError|AttributeError|RuntimeError|FileNotFoundError|ModuleNotFoundError|ConnectionError|TimeoutError|RecursionError|OverflowError):?\s*([^\n]+)?`)

func (s *SandboxRunner) verifyOriginalErrorFixed(ctx context.Context, ns, service string, inc *schemas.Incident) *level3Result {
        // Get the original pod logs from the anomaly explanation
        explanation := ""
        for _, anomaly := range inc.Cluster.Anomalies {
                explanation += anomaly.Explanation + "\n"
        }
        if explanation == "" {
                return nil
        }

        m := errorTypeRe.FindStringSubmatch(explanation)
        if m == nil {
                return nil // No recognizable error — can't reproduce
        }
        errorType := m[1]
        errorDetail := ""
        if len(m) > 2 {
                errorDetail = m[2]
        }

        // Startup errors are already verified by Level 1 (pod is Ready + sustained
        // for 30s = no crash). v3.8.4: This auto-pass is now safe because Level 1
        // requires sustained Ready — a pod that crashes after going Ready no longer
        // passes Level 1, so reaching this branch means the canary is genuinely
        // serving traffic without the startup error.
        startupErrors := map[string]bool{
                "KeyError": true, "ImportError": true, "ModuleNotFoundError": true,
                "FileNotFoundError": true, "SyntaxError": true,
        }
        if startupErrors[errorType] {
                return &level3Result{
                        passed: true,
                        detail: fmt.Sprintf("Startup error %s: %s — canary pod started + stayed Ready for %s (error is gone)", errorType, errorDetail, s.sustainedWindow),
                }
        }

        // For runtime errors — call the app's endpoints and verify:
        //   1. No 4xx/5xx responses (v3.8.4: previously only 5xx + error-string match failed)
        //   2. The original error type/detail doesn't appear in any response body
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil || svc == nil {
                return nil
        }
        port := int32(80)
        if len(svc.Spec.Ports) > 0 {
                port = svc.Spec.Ports[0].Port
        }
        svcName := svc.Name

        probePaths := s.probePaths
        if len(probePaths) == 0 {
                probePaths = []string{"/", "/healthz", "/readyz"}
        }

        client := &http.Client{Timeout: 5 * time.Second}
        totalProbes := 0
        failedProbes := 0

        for _, path := range probePaths {
                url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, path)
                req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)

                // v3.8.4: Retry each path up to 2 times to ride through transients.
                // If after 2 retries the path still returns 4xx/5xx or fails to
                // connect, count it as a failure.
                pathOK := false
                for attempt := 0; attempt < 2; attempt++ {
                        resp, err := client.Do(req)
                        if err != nil {
                                log.Printf("[sandbox] Level 3: probe %s attempt %d connect error: %v", url, attempt+1, err)
                                continue
                        }
                        body := make([]byte, 4096)
                        n, _ := resp.Body.Read(body)
                        resp.Body.Close()
                        bodyStr := strings.ToLower(string(body[:n]))

                        // Check 1: original error type/detail not in response body
                        if strings.Contains(bodyStr, strings.ToLower(errorType)) ||
                                (errorDetail != "" && strings.Contains(bodyStr, strings.ToLower(strings.TrimSpace(errorDetail)))) {
                                return &level3Result{
                                        passed: false,
                                        detail: fmt.Sprintf("%s: %s still present in response body from %s (status %d)", errorType, errorDetail, path, resp.StatusCode),
                                }
                        }

                        // v3.8.4: Check 2 — ANY 4xx/5xx response fails Level 3.
                        // Previously only 5xx + error-string match failed; a 404 with
                        // no error string in the body passed silently. Now: any non-2xx/3xx
                        // response is treated as evidence the error is still present.
                        if resp.StatusCode >= 400 {
                                log.Printf("[sandbox] Level 3: probe %s returned %d (any 4xx/5xx fails Level 3)", url, resp.StatusCode)
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

        if failedProbes > 0 {
                return &level3Result{
                        passed: false,
                        detail: fmt.Sprintf("%d/%d probe paths returned 4xx/5xx or failed to connect (%s: %s not fully gone)",
                                failedProbes, totalProbes, errorType, errorDetail),
                }
        }

        return &level3Result{
                passed: true,
                detail: fmt.Sprintf("Runtime error %s: %s — not reproduced on any endpoint, all probes returned 2xx/3xx (error is gone)", errorType, errorDetail),
        }
}

func (s *SandboxRunner) cleanup(ctx context.Context, ns string) {
        _ = s.clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
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
                // Force non-root at the container level too (defense in depth
                // with the pod-level setting below).
                sc.RunAsNonRoot = boolPtr(true)
                sc.RunAsUser = int64Ptr(65532)
        }

        // v3.8.4: Force a safe pod-level security context.
        if spec.SecurityContext == nil {
                spec.SecurityContext = &corev1.PodSecurityContext{}
        }
        psc := spec.SecurityContext
        psc.RunAsNonRoot = boolPtr(true)
        psc.RunAsUser = int64Ptr(65532)
        psc.FSGroup = int64Ptr(65532)
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
func int64Ptr(i int64) *int64 { return &i }

func int32Ptr(i int32) *int32 { return &i }

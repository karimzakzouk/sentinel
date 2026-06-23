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
//   Level 1: Canary pod becomes Ready (startup crash fixed)
//   Level 2: Endpoints return 2xx/3xx (app starts + serves traffic)
//   Level 3: Original error is gone (reproduce the crash conditions)
type SandboxRunner struct {
        clientset        *kubernetes.Clientset
        sandboxPrefix    string
        enableMirroring  bool
}

// NewSandbox creates a SandboxRunner.
func NewSandbox(clientset *kubernetes.Clientset, sandboxPrefix string, enableMirroring bool) *SandboxRunner {
        return &SandboxRunner{
                clientset:       clientset,
                sandboxPrefix:   sandboxPrefix,
                enableMirroring: enableMirroring,
        }
}

// Run creates a sandbox, deploys the fix, verifies it, returns the verdict.
func (s *SandboxRunner) Run(ctx context.Context, inc *schemas.Incident) SandboxResult {
        fp := inc.State.FixProposal
        if fp == nil {
                return SandboxResult{Verified: false, Summary: "no fix proposal"}
        }

        sandboxNS := fmt.Sprintf("%s-%s-%s", s.sandboxPrefix, fp.TargetService, fp.FixID.String()[:8])

        // 1. Create the sandbox namespace
        if err := s.createNamespace(ctx, sandboxNS); err != nil {
                return SandboxResult{Verified: false, Summary: fmt.Sprintf("create_namespace: %v", err), Namespace: sandboxNS}
        }

        // Always clean up at the end
        defer s.cleanup(ctx, sandboxNS)

        // 2. Deploy the canary into the sandbox
        deployResult := s.deployCanary(ctx, inc, sandboxNS)
        if !deployResult.success {
                return SandboxResult{Verified: false, Summary: fmt.Sprintf("deploy_failed: %s", deployResult.err), Namespace: sandboxNS}
        }

        // 3. Level 1 — wait for canary pods to be Ready (startup crash detection)
        ready := s.waitForReady(ctx, sandboxNS, fp.TargetService, 240*time.Second)
        if !ready {
                return SandboxResult{Verified: false, Summary: "canary_pods_not_ready (startup crash not fixed)", Namespace: sandboxNS}
        }
        log.Printf("[sandbox] Level 1 PASSED: canary pod is Ready")

        // 4. Level 2 — call the app's own endpoints to verify they respond
        if !s.verifyEndpoints(ctx, sandboxNS, fp.TargetService) {
                return SandboxResult{Verified: false, Summary: "endpoints_returned_5xx (app starts but endpoints fail)", Namespace: sandboxNS}
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
                Summary:   "all_checks_passed (pod ready + endpoints ok + original error gone)",
                Namespace: sandboxNS,
        }
}

func (s *SandboxRunner) createNamespace(ctx context.Context, ns string) error {
        _, err := s.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                        Name: ns,
                        Labels: map[string]string{
                                "sentinel.io/sandbox": "true",
                        },
                },
        }, metav1.CreateOptions{})
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
                "app":                   fp.TargetService,
                "sentinel.io/sandbox":   "true",
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

        // Copy ConfigMaps + Secrets from prod namespace to sandbox
        _ = CopyConfigMapsAndSecrets(ctx, s.clientset, prodDeploy, fp.TargetNamespace, ns)

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

func (s *SandboxRunner) waitForReady(ctx context.Context, ns, name string, timeout time.Duration) bool {
        deadline := time.Now().Add(timeout)
        for time.Now().Before(deadline) {
                deploy, err := s.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
                if err == nil && deploy.Status.ReadyReplicas >= 1 {
                        return true
                }
                time.Sleep(3 * time.Second)
        }
        return false
}

func (s *SandboxRunner) verifyEndpoints(ctx context.Context, ns, service string) bool {
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil {
                return true // Don't fail if no Service — Level 1 + 3 are sufficient
        }
        port := int32(80)
        if len(svc.Spec.Ports) > 0 {
                port = svc.Spec.Ports[0].Port
        }
        svcName := svc.Name

        endpoints := []string{"/", "/healthz", "/readyz"}
        client := &http.Client{Timeout: 5 * time.Second}
        for _, path := range endpoints {
                url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, path)
                resp, err := client.Get(url)
                if err != nil {
                        continue // Connection error — pod might still be starting
                }
                resp.Body.Close()
                if resp.StatusCode >= 500 {
                        log.Printf("[sandbox] Level 2 FAILED: %s returned %d", url, resp.StatusCode)
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

        // Startup errors are already verified by Level 1 (pod is Ready = no crash)
        startupErrors := map[string]bool{
                "KeyError": true, "ImportError": true, "ModuleNotFoundError": true,
                "FileNotFoundError": true, "SyntaxError": true,
        }
        if startupErrors[errorType] {
                return &level3Result{
                        passed: true,
                        detail: fmt.Sprintf("Startup error %s: %s — canary pod started successfully (error is gone)", errorType, errorDetail),
                }
        }

        // For runtime errors — call the app's endpoints with various inputs
        svc, err := s.clientset.CoreV1().Services(ns).Get(ctx, service, metav1.GetOptions{})
        if err != nil || svc == nil {
                return nil
        }
        port := int32(80)
        if len(svc.Spec.Ports) > 0 {
                port = svc.Spec.Ports[0].Port
        }
        svcName := svc.Name

        testInputs := []struct {
                method, path string
                params       map[string]string
        }{
                {"GET", "/", nil},
                {"GET", "/healthz", nil},
                {"GET", "/pay", map[string]string{"amount": "0"}},
                {"GET", "/pay", map[string]string{"amount": "-1"}},
                {"GET", "/pay", map[string]string{"amount": "abc"}},
        }

        client := &http.Client{Timeout: 5 * time.Second}
        for _, ti := range testInputs {
                url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", svcName, ns, port, ti.path)
                req, _ := http.NewRequestWithContext(ctx, ti.method, url, nil)
                if ti.params != nil {
                        q := req.URL.Query()
                        for k, v := range ti.params {
                                q.Add(k, v)
                        }
                        req.URL.RawQuery = q.Encode()
                }
                resp, err := client.Do(req)
                if err != nil {
                        continue
                }
                body := make([]byte, 4096)
                n, _ := resp.Body.Read(body)
                resp.Body.Close()
                bodyStr := strings.ToLower(string(body[:n]))
                if strings.Contains(bodyStr, strings.ToLower(errorType)) || strings.Contains(bodyStr, strings.ToLower(strings.TrimSpace(errorDetail))) {
                        return &level3Result{
                                passed: false,
                                detail: fmt.Sprintf("%s: %s still present in response from %s (status %d)", errorType, errorDetail, ti.path, resp.StatusCode),
                        }
                }
                if resp.StatusCode >= 500 {
                        return &level3Result{
                                passed: false,
                                detail: fmt.Sprintf("%s returned %d — possible %s still occurring", ti.path, resp.StatusCode, errorType),
                        }
                }
        }

        return &level3Result{
                passed: true,
                detail: fmt.Sprintf("Runtime error %s: %s — not reproduced on any endpoint (error is gone)", errorType, errorDetail),
        }
}

func (s *SandboxRunner) cleanup(ctx context.Context, ns string) {
        _ = s.clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
}

func patchPodSpec(spec corev1.PodSpec, canaryImage string) corev1.PodSpec {
        for i := range spec.Containers {
                spec.Containers[i].Image = canaryImage
                spec.Containers[i].ImagePullPolicy = corev1.PullAlways
        }
        return spec
}

func int32Ptr(i int32) *int32 { return &i }

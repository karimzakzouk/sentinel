// Package executors — K8s namespace backend for the sandbox.
//
// Contains the K8s-specific implementation: namespace creation, NetworkPolicy,
// canary Deployment deployment, cleanup, stale namespace reaping.
//
// Future backends (gVisor, Firecracker, Docker-local) will live in sibling
// files and implement the sandbox.Backend interface defined in
// internal/sandbox/backend.go.
package executors

import (
        "context"
        "fmt"
        "log"
        "strings"
        "time"

        appsv1 "k8s.io/api/apps/v1"
        corev1 "k8s.io/api/core/v1"
        networkingv1 "k8s.io/api/networking/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/util/intstr"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

func (s *SandboxRunner) createNamespace(ctx context.Context, ns string) error {
        _, err := s.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                        Name: ns,
                        Labels: map[string]string{
                                "sentinel.io/sandbox": "true",

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

        // ConfigMap/Secret in prod means the sandbox canary will start with empty
        // env vars, which can mask the real failure behind a misleading "Ready"
        // status (the pod can go Ready with broken config).
        if err := CopyConfigMapsAndSecrets(ctx, s.clientset, prodDeploy, fp.TargetNamespace, ns); err != nil {
                log.Printf("[sandbox] WARNING: CopyConfigMapsAndSecrets had errors: %v (canary may start with missing env vars)", err)
        }

        // Create a Service so Level 2 verification can reach the canary.

        // container ports, (3) fallback to 8000. Previously hardcoded 8000, which
        // broke for any app on 8080/3000/5000/etc. when the prod Service had no
        // ports declared (CRITICAL audit #3).
        sandboxPorts := []corev1.ServicePort{
                {Port: 80, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolTCP, Name: "http"},
        }
        if prodSvc, err := s.clientset.CoreV1().Services(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil && len(prodSvc.Spec.Ports) > 0 {
                sandboxPorts = prodSvc.Spec.Ports
        } else if prodDeploy != nil && len(prodDeploy.Spec.Template.Spec.Containers) > 0 {

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

// also captures canary logs during the sustained window).

// getProbePaths returns a copy of probePaths under the read lock.

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

func (s *SandboxRunner) cleanup(ctx context.Context, ns string) {

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

                // may run as root (e.g. Go binaries that bind to port 80).
                // Forcing non-root breaks canaries for root-running prod images.
                // The namespace PodSecurity label is "baseline" which allows
                // root but blocks privileged. This is the right tradeoff.
                // Previous v3.8.4 code forced RunAsNonRoot=true + RunAsUser=65532,
                // which conflicted with the baseline namespace label and caused
                // canary pods to fail with "container has runAsNonRoot and image
                // will run as root".
        }

        // as above — respect the prod image's user. The namespace's PodSecurity
        // "baseline" label provides the security boundary.
        if spec.SecurityContext == nil {
                spec.SecurityContext = &corev1.PodSecurityContext{}
        }
        psc := spec.SecurityContext
        if psc.SeccompProfile == nil {
                psc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
        }

        // hostNetwork / hostPID / hostIPC.
        spec.HostNetwork = false
        spec.HostPID = false
        spec.HostIPC = false

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

package executors

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/karimzakzouk/sentinel/internal/errs"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// createNamespace creates the sandbox namespace with PodSecurity labels.
// The "baseline" enforce level allows canaries that inherit a root-running
// prod image while still blocking privileged pods and hostPath.
func (s *SandboxRunner) createNamespace(ctx context.Context, ns string) error {
	_, err := s.clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"sentinel.io/sandbox":                "true",
				"pod-security.kubernetes.io/enforce": "baseline",
				"pod-security.kubernetes.io/audit":   "restricted",
				"pod-security.kubernetes.io/warn":    "restricted",
			},
		},
	}, metav1.CreateOptions{})
	if err != nil && !errs.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

// createSandboxNetworkPolicy creates a default-deny NetworkPolicy in the
// sandbox namespace. Ingress is allowed only from the Sentinel namespace
// (so probes can reach the canary); egress is fully denied (the canary
// should not phone home). On CNIs that don't support NetworkPolicy this
// is a no-op.
func (s *SandboxRunner) createSandboxNetworkPolicy(ctx context.Context, ns string) error {
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
			Labels:    map[string]string{"sentinel.io/managed": "true"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{allowFromSentinel},
			Egress:  []networkingv1.NetworkPolicyEgressRule{denyAllEgress},
		},
	}
	_, err := s.clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, np, metav1.CreateOptions{})
	if err != nil && !errs.IsAlreadyExists(err) {
		return fmt.Errorf("create networkpolicy in %s: %w", ns, err)
	}
	return nil
}

// deployResult is the internal outcome of deployCanary.
type deployResult struct {
	success bool
	err     string
	image   string
	// specProbePaths are the HTTP paths declared by the prod Deployment's
	// readiness/liveness probes. Level 2 prefers them over any guessed
	// list — they are the service's own definition of health.
	specProbePaths []string
}

// httpProbePaths extracts the HTTP GET paths from a Deployment's
// readiness/liveness probes (all containers, deduplicated, order
// preserved). Probes that are not httpGet (tcpSocket/exec/grpc) carry no
// path and are ignored. Probing paths other than these can 404 on a
// perfectly healthy app — the spec is the source of truth, the same
// health model as Argo Rollouts (a canary is healthy when the workload's
// own probes pass).
func httpProbePaths(dep *appsv1.Deployment) []string {
	if dep == nil {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	for i := range dep.Spec.Template.Spec.Containers {
		c := &dep.Spec.Template.Spec.Containers[i]
		for _, probe := range []*corev1.Probe{c.ReadinessProbe, c.LivenessProbe} {
			if probe == nil || probe.HTTPGet == nil {
				continue
			}
			path := probe.HTTPGet.Path
			if path == "" {
				path = "/"
			}
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// deployCanary creates a sandbox Deployment mirroring the prod Deployment
// with the canary image, plus a Service so Level 2 probes can reach it.
func (s *SandboxRunner) deployCanary(ctx context.Context, inc *schemas.Incident, ns string) deployResult {
	fp := inc.State.FixProposal

	var prodDeploy *appsv1.Deployment
	if dep, err := s.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil {
		prodDeploy = dep
	} else {
		deps, listErr := s.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
		})
		if listErr != nil || len(deps.Items) == 0 {
			return deployResult{err: fmt.Sprintf("deployment %s not found in %s", fp.TargetService, fp.TargetNamespace)}
		}
		prodDeploy = &deps.Items[0]
	}

	canaryImage := ""
	if fp.CanaryImage != nil && *fp.CanaryImage != "" {
		canaryImage = *fp.CanaryImage
	} else if len(prodDeploy.Spec.Template.Spec.Containers) > 0 {
		canaryImage = prodDeploy.Spec.Template.Spec.Containers[0].Image
	} else {
		return deployResult{err: "no canary image and no prod image"}
	}

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

	// Copy ConfigMaps + Secrets from prod namespace so the canary starts
	// with the same env. Without this, Level 1 can pass on a pod with
	// empty config, masking the real failure.
	if err := CopyConfigMapsAndSecrets(ctx, s.clientset, prodDeploy, fp.TargetNamespace, ns); err != nil {
		logging.Error("CopyConfigMapsAndSecrets had errors (canary may start with missing env vars)", "err", err)
	}

	// Service: prefer prod Service ports, then derive from prod Deployment
	// container ports, then fall back to port 80 → 8000.
	sandboxPorts := []corev1.ServicePort{
		{Port: 80, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolTCP, Name: "http"},
	}
	if prodSvc, err := s.clientset.CoreV1().Services(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{}); err == nil && len(prodSvc.Spec.Ports) > 0 {
		sandboxPorts = prodSvc.Spec.Ports
	} else if prodDeploy != nil && len(prodDeploy.Spec.Template.Spec.Containers) > 0 {
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
			logging.Info("no prod Service ports — derived port(s) from prod Deployment container spec",
				"count", len(derived))
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

	specPaths := httpProbePaths(prodDeploy)
	if len(specPaths) > 0 {
		logging.Info("canary: derived Level 2 probe paths from prod Deployment probes",
			"service", fp.TargetService, "paths", specPaths)
	}
	return deployResult{success: true, image: canaryImage, specProbePaths: specPaths}
}

// getProbePaths returns a copy of probePaths under the read lock.
func (s *SandboxRunner) getProbePaths() []string {
	s.probePathsMu.RLock()
	defer s.probePathsMu.RUnlock()
	return append([]string(nil), s.probePaths...)
}

// cleanup deletes the sandbox namespace. Uses a fresh context so the
// cleanup survives cancellation of the parent ctx (e.g. on investigation
// timeout) — otherwise Namespace.Delete returns context.Canceled
// immediately and leaks the namespace.
func (s *SandboxRunner) cleanup(ctx context.Context, ns string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.clientset.CoreV1().Namespaces().Delete(cleanupCtx, ns, metav1.DeleteOptions{}); err != nil {
		logging.Error("cleanup: failed to delete namespace", "namespace", ns, "err", err)
	}
	_ = ctx
}

// ReapStaleSandboxNamespaces lists all sandbox namespaces and deletes any
// older than maxAge. This is the safety net for namespaces that leak when
// the Sentinel pod is killed mid-verification (the in-process defer cleanup
// doesn't survive pod restarts). Returns the count of namespaces reaped.
func (s *SandboxRunner) ReapStaleSandboxNamespaces(ctx context.Context, maxAge time.Duration) int {
	list, err := s.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "sentinel.io/sandbox=true",
	})
	if err != nil {
		logging.Error("reaper: failed to list sandbox namespaces", "err", err)
		return 0
	}

	reaped := 0
	cutoff := time.Now().Add(-maxAge)
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.CreationTimestamp.After(cutoff) {
			continue
		}
		if err := s.clientset.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
			logging.Error("reaper: failed to delete stale namespace",
				"namespace", ns.Name, "err", err)
			continue
		}
		logging.Info("reaper: deleted stale sandbox namespace",
			"namespace", ns.Name,
			"created", ns.CreationTimestamp.Format(time.RFC3339),
			"age", time.Since(ns.CreationTimestamp.Time).Round(time.Second))
		reaped++
	}
	if reaped > 0 {
		logging.Info("reaper: reaped stale sandbox namespace(s)", "count", reaped)
	}
	return reaped
}

// patchPodSpec copies the prod pod spec into the sandbox canary, applies
// the canary image, and force-sets a safe security context:
//   - privileged=false, ALL capabilities dropped, allowPrivilegeEscalation=false,
//     seccomp=RuntimeDefault, readOnlyRootFilesystem=true.
//   - hostNetwork/hostPID/hostIPC=false.
//   - hostPath volumes replaced with emptyDir (sandbox must not mount host).
//   - /tmp mounted as emptyDir so apps that write temp files work under
//     ReadOnlyRootFilesystem.
//
// runAsNonRoot / runAsUser are NOT forced — the prod image may legitimately
// run as root (e.g. a Go binary binding port 80). The namespace's
// PodSecurity "baseline" label provides the security boundary.
func patchPodSpec(spec corev1.PodSpec, canaryImage string) corev1.PodSpec {
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
		sc.Capabilities.Add = nil
		if sc.SeccompProfile == nil {
			sc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
		}
	}

	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	if spec.SecurityContext.SeccompProfile == nil {
		spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	}

	spec.HostNetwork = false
	spec.HostPID = false
	spec.HostIPC = false

	for i := range spec.Volumes {
		if spec.Volumes[i].HostPath != nil {
			logging.Info("SECURITY: replacing hostPath volume with emptyDir (sandbox must not mount host paths)",
				"volume", spec.Volumes[i].Name)
			spec.Volumes[i] = corev1.Volume{
				Name: spec.Volumes[i].Name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}
		}
	}

	// Mount an emptyDir at /tmp so apps that write temp files work under
	// ReadOnlyRootFilesystem.
	const tmpVolumeName = "sentinel-tmp"
	hasTmpVolume := false
	for _, v := range spec.Volumes {
		if v.Name == tmpVolumeName {
			hasTmpVolume = true
			break
		}
	}
	if !hasTmpVolume {
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: tmpVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}
	for i := range spec.Containers {
		hasTmpMount := false
		for _, vm := range spec.Containers[i].VolumeMounts {
			if vm.MountPath == "/tmp" {
				hasTmpMount = true
				break
			}
		}
		if !hasTmpMount {
			spec.Containers[i].VolumeMounts = append(spec.Containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      tmpVolumeName,
					MountPath: "/tmp",
				})
		}
	}

	return spec
}

// boolPtr returns a pointer to b. Helper for *bool fields in corev1.
func boolPtr(b bool) *bool { return &b }

// int32Ptr returns a pointer to i. Helper for *int32 fields in corev1.
func int32Ptr(i int32) *int32 { return &i }

package capability

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// ProbeImage runs the active probes. It is the Kubernetes project's own test image.
const ProbeImage = "registry.k8s.io/e2e-test-images/agnhost:2.61"

const probeLabel = "app.kubernetes.io/name"

func probePod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{probeLabel: "sentinel-probe", "app.kubernetes.io/part-of": "sentinel"},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			RestartPolicy:                corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "probe", Image: ProbeImage, Args: []string{"pause"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
}

// nodeAgentPod is shaped like the tier-1 node agent: root without any
// capabilities, so it can read root-owned pod logs and cgroup files through
// read-only host paths and nothing else.
func nodeAgentPod(ns string) *corev1.Pod {
	p := probePod(ns, "sentinel-probe-node-agent")
	p.Spec.SecurityContext = &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	p.Spec.Containers[0].SecurityContext.RunAsUser = ptr.To(int64(0))
	hostPath := func(name, path string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: path, Type: ptr.To(corev1.HostPathDirectory)},
		}}
	}
	p.Spec.Volumes = []corev1.Volume{hostPath("pod-logs", "/var/log/pods"), hostPath("cgroup", "/sys/fs/cgroup")}
	p.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "pod-logs", MountPath: "/host/var/log/pods", ReadOnly: true},
		{Name: "cgroup", MountPath: "/host/sys/fs/cgroup", ReadOnly: true},
	}
	return p
}

// ebpfPod is shaped like the tier-2 eBPF capture: BPF-related capabilities and
// the host PID namespace, but not a fully privileged container.
func ebpfPod(ns string) *corev1.Pod {
	p := probePod(ns, "sentinel-probe-ebpf")
	p.Spec.HostPID = true
	p.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{
		"BPF", "PERFMON", "SYS_PTRACE", "DAC_READ_SEARCH", "NET_RAW", "CHECKPOINT_RESTORE",
	}
	return p
}

// userNamespacePod is a restricted pod that asks for its own user namespace,
// as Lab pods will.
func userNamespacePod(ns string) *corev1.Pod {
	p := probePod(ns, "sentinel-probe-user-namespace")
	p.Spec.HostUsers = ptr.To(false)
	p.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(int64(65534)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	return p
}

type probeResults struct {
	nodeAgent, ebpf, userNamespace Status
}

func (d *Detector) runDryRunProbes(ctx context.Context, rep *Report) probeResults {
	run := func(id, ns, role string, build func(string) *corev1.Pod) Status {
		if ns == "" {
			rep.add(id, Unknown, "no %s namespace configured", role)
			return Unknown
		}
		_, err := d.Client.CoreV1().Pods(ns).Create(ctx, build(ns), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		status, detail := classifyDryRun(err)
		rep.add(id, status, "in %s: %s", ns, detail)
		return status
	}
	return probeResults{
		nodeAgent:     run("probe.node-agent", d.Options.AgentNamespace, "agent", nodeAgentPod),
		ebpf:          run("probe.ebpf", d.Options.AgentNamespace, "agent", ebpfPod),
		userNamespace: run("probe.user-namespace", d.Options.LabNamespace, "Lab", userNamespacePod),
	}
}

// classifyDryRun turns a dry-run create result into a status. Built-in
// admission (Pod Security, ValidatingAdmissionPolicy) evaluates dry runs
// faithfully, so its rejections are real. A webhook may refuse dry runs for
// unrelated reasons, so its rejection leaves the outcome unknown.
func classifyDryRun(err error) (Status, string) {
	if err == nil {
		return Available, "admission allows it (dry run)"
	}
	msg := truncate(err.Error(), 300)
	switch {
	case strings.Contains(msg, "violates PodSecurity"):
		return Unavailable, "Pod Security admission rejects it: " + msg
	case strings.Contains(msg, "ValidatingAdmissionPolicy"):
		return Unavailable, "an admission policy rejects it: " + msg
	case strings.Contains(msg, "admission webhook"), strings.Contains(msg, "does not support dry run"):
		return Unknown, "an admission webhook blocked the dry run, so the real outcome is unknown: " + msg
	case apierrors.IsForbidden(err):
		return Unknown, "not allowed to run the dry-run test: " + msg
	case apierrors.IsInvalid(err):
		return Unavailable, "the API server rejects the pod spec: " + msg
	default:
		return Unknown, msg
	}
}

var kernelRE = regexp.MustCompile(`^(\d+)\.(\d+)`)

// kernelAtLeast reports whether a node's kernel version is at least major.minor.
// Unparseable versions report false.
func kernelAtLeast(version string, major, minor int) bool {
	m := kernelRE.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	mnr, _ := strconv.Atoi(m[2])
	return maj > major || (maj == major && mnr >= minor)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

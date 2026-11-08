package capability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// NetworkPolicyCheckID is the ID of the active NetworkPolicy enforcement check.
const NetworkPolicyCheckID = "network.policy-enforced"

// NetworkPolicyProbe proves, with real traffic, whether the cluster enforces
// NetworkPolicy. A NetworkPolicy API that exists but is not enforced is the
// most dangerous case for the Lab, so only this probe can enable it.
type NetworkPolicyProbe struct {
	Client    kubernetes.Interface
	Namespace string
	// Settle is how long a deny policy may take to start blocking traffic.
	Settle time.Duration
	// Poll is the wait between attempts.
	Poll time.Duration
}

const probePort = 8080

// Run proves enforcement in two steps. With a policy that allows traffic
// between the probe's own pods, a control connection must succeed; once that
// allow policy is replaced by a deny policy, the same connection must be
// blocked. Policies only ever add allowances, so this works in a namespace
// that already denies everything by default, and then tests that namespace's
// real isolation. Everything the probe creates is deleted before it returns.
func (p NetworkPolicyProbe) Run(ctx context.Context) Check {
	if p.Settle == 0 {
		p.Settle = 60 * time.Second
	}
	if p.Poll == 0 {
		p.Poll = 2 * time.Second
	}
	id := strings.ToLower(string(model.NewID())[24:])
	labels := map[string]string{probeLabel: "sentinel-netpol-probe", "sentinel-probe-id": id}
	defer p.cleanup(context.WithoutCancel(ctx), id)

	ownPods := []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"sentinel-probe-id": id}}}}
	allow := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sentinel-np-allow-" + id, Namespace: p.Namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"sentinel-probe-id": id}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: ownPods}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{To: ownPods}},
		},
	}
	if _, err := p.Client.NetworkingV1().NetworkPolicies(p.Namespace).Create(ctx, allow, metav1.CreateOptions{}); err != nil {
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "could not create the allow policy: " + truncate(err.Error(), 200)}
	}

	server := probePod(p.Namespace, "sentinel-np-server-"+id)
	server.Labels = mergeLabels(server.Labels, labels, map[string]string{"sentinel-probe-role": "server"})
	server.Spec.SecurityContext = restrictedPodSecurity()
	server.Spec.Containers[0].Args = []string{"netexec", "--http-port=" + strconv.Itoa(probePort)}
	server.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(probePort)},
	}, PeriodSeconds: 1}
	if _, err := p.Client.CoreV1().Pods(p.Namespace).Create(ctx, server, metav1.CreateOptions{}); err != nil {
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "could not start the probe server: " + truncate(err.Error(), 200)}
	}
	ip, err := p.waitReady(ctx, server.Name)
	if err != nil {
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "probe server did not become ready: " + err.Error()}
	}
	target := net.JoinHostPort(ip, strconv.Itoa(probePort))

	ok, err := p.connect(ctx, id, 0, target, labels)
	switch {
	case err != nil:
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "control connection could not run: " + err.Error()}
	case !ok:
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "the control connection failed even though a policy allowed it, so pod networking itself is broken here"}
	}

	deny := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sentinel-np-deny-" + id, Namespace: p.Namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"sentinel-probe-id": id, "sentinel-probe-role": "server"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	if _, err := p.Client.NetworkingV1().NetworkPolicies(p.Namespace).Create(ctx, deny, metav1.CreateOptions{}); err != nil {
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "could not create the deny policy: " + truncate(err.Error(), 200)}
	}
	if err := p.Client.NetworkingV1().NetworkPolicies(p.Namespace).Delete(ctx, allow.Name, metav1.DeleteOptions{}); err != nil {
		return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "could not remove the allow policy: " + truncate(err.Error(), 200)}
	}
	applied := time.Now()
	for attempt := 1; ; attempt++ {
		ok, err := p.connect(ctx, id, attempt, target, labels)
		if err != nil {
			return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "test connection could not run: " + err.Error()}
		}
		if !ok {
			return Check{ID: NetworkPolicyCheckID, Status: Available, Detail: fmt.Sprintf(
				"enforced: traffic was blocked within %s of removing the allow policy, and got through while it existed", time.Since(applied).Round(time.Second))}
		}
		if time.Since(applied) > p.Settle {
			return Check{ID: NetworkPolicyCheckID, Status: Unavailable, Detail: fmt.Sprintf(
				"not enforced: traffic still got through %s after the allow policy was replaced by a deny policy; the network plugin ignores NetworkPolicy", p.Settle)}
		}
		select {
		case <-ctx.Done():
			return Check{ID: NetworkPolicyCheckID, Status: Unknown, Detail: "probe cancelled: " + ctx.Err().Error()}
		case <-time.After(p.Poll):
		}
	}
}

// connect runs a one-shot client pod and reports whether it reached target.
func (p NetworkPolicyProbe) connect(ctx context.Context, id string, attempt int, target string, labels map[string]string) (bool, error) {
	client := probePod(p.Namespace, fmt.Sprintf("sentinel-np-client-%s-%d", id, attempt))
	client.Labels = mergeLabels(client.Labels, labels, map[string]string{"sentinel-probe-role": "client"})
	client.Spec.SecurityContext = restrictedPodSecurity()
	client.Spec.Containers[0].Args = []string{"connect", target, "--timeout=3s"}
	if _, err := p.Client.CoreV1().Pods(p.Namespace).Create(ctx, client, metav1.CreateOptions{}); err != nil {
		return false, err
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		pod, err := p.Client.CoreV1().Pods(p.Namespace).Get(ctx, client.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return true, nil
		case corev1.PodFailed:
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return false, errors.New("client pod did not finish within 90s")
}

func (p NetworkPolicyProbe) waitReady(ctx context.Context, name string) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		pod, err := p.Client.CoreV1().Pods(p.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod.Status.PodIP != "" {
				return pod.Status.PodIP, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", errors.New("not ready within 2m")
}

func (p NetworkPolicyProbe) cleanup(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sel := metav1.ListOptions{LabelSelector: "sentinel-probe-id=" + id}
	if pods, err := p.Client.CoreV1().Pods(p.Namespace).List(ctx, sel); err == nil {
		for _, pod := range pods.Items {
			_ = p.Client.CoreV1().Pods(p.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))})
		}
	}
	for _, name := range []string{"sentinel-np-allow-" + id, "sentinel-np-deny-" + id} {
		_ = p.Client.NetworkingV1().NetworkPolicies(p.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
}

func restrictedPodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(int64(65534)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func mergeLabels(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// Package lab seals a Lab namespace and proves the seal holds before any
// workload runs there: positive controls must answer, so "blocked" is told
// from "probe broken", and every forbidden destination must be refused. The
// proof runs with real traffic from a prober pod, whose results come back in
// its termination message, so the Lab never needs to exec into a pod.
package lab

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capability"
)

// Image is the probe image: agnhost serves the echo control and connects
// for the prober.
const Image = capability.ProbeImage

// Label marks every object the Lab creates, so teardown finds them.
const Label = "sentinel.karimzakzouk.github.io/lab"

// Probe is one destination the prober tries, and whether the seal must let
// it through.
type Probe struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Allowed bool   `json:"allowed"`
	// Outside marks a destination beyond the cluster, which some network
	// plugins leave out of egress policy.
	Outside bool `json:"outside,omitempty"`
}

// Result is what the prober found: Reached as it started, Later once it
// had run ten seconds, for destinations the seal forbids.
type Result struct {
	Probe
	Reached bool `json:"reached"`
	Later   bool `json:"later,omitempty"`
}

// Holds reports whether the result is what the seal requires.
func (r Result) Holds() bool {
	if r.Allowed {
		return r.Reached
	}
	return !r.Reached && !r.Later
}

// Prover seals and proves Lab namespaces.
type Prover struct {
	Client kubernetes.Interface
	// Twins are production namespaces whose pods and Services the Lab must
	// not reach.
	Twins []string
	// Timeout bounds each wait for a pod.
	Timeout time.Duration
	// Forbidden is the destination the seal gate tries, which must be
	// refused; the API server's Service when empty.
	Forbidden string
}

// Seal adds the one allow rule a Lab needs: its pods may reach each other.
// The default-deny policy created at install refuses everything else.
func (p *Prover) Seal(ctx context.Context, ns string) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "lab-internal", Namespace: ns, Labels: map[string]string{Label: "true"}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}}},
		},
	}
	_, err := p.Client.NetworkingV1().NetworkPolicies(ns).Create(ctx, np, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// Matrix lists the probes for a Lab namespace: the echo control, which the
// seal must let through, and every forbidden destination the cluster has.
func (p *Prover) Matrix(ctx context.Context, ns string, echo *corev1.Pod, echoService *corev1.Service) ([]Probe, error) {
	probes := []Probe{
		{Name: "echo service", Address: hostPort(echoService.Spec.ClusterIP, 8080), Allowed: true},
		{Name: "echo pod", Address: hostPort(echo.Status.PodIP, 8080), Allowed: true},
		{Name: "cloud metadata", Address: "169.254.169.254:80", Outside: true},
		{Name: "Azure metadata", Address: "168.63.129.16:80", Outside: true},
		{Name: "internet by IP", Address: "1.1.1.1:443", Outside: true},
	}
	if svc, err := p.Client.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{}); err == nil {
		probes = append(probes, Probe{Name: "API server service", Address: hostPort(svc.Spec.ClusterIP, 443)})
	}
	slices, err := p.Client.DiscoveryV1().EndpointSlices("default").List(ctx,
		metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=kubernetes"})
	if err == nil {
		for _, s := range slices.Items {
			for _, e := range s.Endpoints {
				for _, a := range e.Addresses {
					for _, port := range s.Ports {
						if port.Port != nil {
							probes = append(probes, Probe{Name: "API server " + a, Address: hostPort(a, int(*port.Port))})
						}
					}
				}
			}
		}
	}
	if svc, err := p.Client.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{}); err == nil {
		probes = append(probes, Probe{Name: "cluster DNS", Address: hostPort(svc.Spec.ClusterIP, 53)})
	}
	nodes, err := p.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				probes = append(probes, Probe{Name: "kubelet on " + n.Name, Address: hostPort(a.Address, 10250)})
			}
		}
	}
	for _, twin := range p.Twins {
		if twin == ns {
			continue
		}
		svcs, _ := p.Client.CoreV1().Services(twin).List(ctx, metav1.ListOptions{Limit: 3})
		for _, s := range svcs.Items {
			if s.Spec.ClusterIP != "" && s.Spec.ClusterIP != corev1.ClusterIPNone && len(s.Spec.Ports) > 0 {
				probes = append(probes, Probe{Name: "service " + twin + "/" + s.Name, Address: hostPort(s.Spec.ClusterIP, int(s.Spec.Ports[0].Port))})
			}
		}
		pods, _ := p.Client.CoreV1().Pods(twin).List(ctx, metav1.ListOptions{Limit: 3})
		for _, pod := range pods.Items {
			if pod.Status.PodIP != "" && !pod.Spec.HostNetwork && len(pod.Spec.Containers) > 0 && len(pod.Spec.Containers[0].Ports) > 0 {
				probes = append(probes, Probe{Name: "pod " + twin + "/" + pod.Name,
					Address: hostPort(pod.Status.PodIP, int(pod.Spec.Containers[0].Ports[0].ContainerPort))})
			}
		}
	}
	return probes, nil
}

func hostPort(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// Inside is a running world, seen by the matrix: the controls it adds, and
// the DNS the prober must ask, as the world's pods do.
type Inside struct {
	Probes []Probe
	DNS    *corev1.PodDNSConfig
}

// Prove starts the echo control and the prober in a sealed namespace and
// returns what every probe found; inside, when not nil, is the world
// running there. It removes what it started, and leaves the world.
func (p *Prover) Prove(ctx context.Context, ns string, inside *Inside) ([]Result, error) {
	defer p.Teardown(context.WithoutCancel(ctx), ns)
	echo, svc, err := p.startEcho(ctx, ns)
	if err != nil {
		return nil, err
	}
	probes, err := p.Matrix(ctx, ns, echo, svc)
	if err != nil {
		return nil, err
	}
	prober := proberPod(ns, probes)
	if inside != nil {
		probes = append(probes, inside.Probes...)
		prober = proberPod(ns, probes)
		if inside.DNS != nil {
			prober.Spec.DNSPolicy, prober.Spec.DNSConfig = corev1.DNSNone, inside.DNS
		}
	}
	if _, err := p.Client.CoreV1().Pods(ns).Create(ctx, prober, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("start the prober: %w", err)
	}
	done, err := p.waitFor(ctx, ns, prober.Name, func(pod *corev1.Pod) bool {
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	})
	if err != nil {
		return nil, fmt.Errorf("the prober did not finish: %w", err)
	}
	return parse(probes, terminationMessage(done))
}

func (p *Prover) startEcho(ctx context.Context, ns string) (*corev1.Pod, *corev1.Service, error) {
	labels := map[string]string{Label: "true", "app": "lab-echo"}
	pod := labPod(ns, "lab-echo", []string{"netexec", "--http-port=8080"}, nil)
	pod.Labels = labels
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 8080}}
	pod.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)}}, PeriodSeconds: 2}
	if _, err := p.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, nil, fmt.Errorf("start the echo control: %w", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "lab-echo", Namespace: ns, Labels: map[string]string{Label: "true"}},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "lab-echo"},
			Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}},
	}
	created, err := p.Client.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("create the echo service: %w", err)
	}
	ready, err := p.waitFor(ctx, ns, pod.Name, func(pod *corev1.Pod) bool {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
	if err != nil {
		return nil, nil, fmt.Errorf("the echo control did not become ready: %w", err)
	}
	return ready, created, nil
}

func (p *Prover) waitFor(ctx context.Context, ns, name string, ok func(*corev1.Pod) bool) (*corev1.Pod, error) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil && ok(pod) {
			return pod, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("pod %s is %s", name, pod.Status.Phase)
			}
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Teardown removes everything the Lab started in a namespace.
func (p *Prover) Teardown(ctx context.Context, ns string) {
	sel := metav1.ListOptions{LabelSelector: Label + "=true"}
	_ = p.Client.CoreV1().Pods(ns).DeleteCollection(ctx, metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)}, sel)
	svcs, _ := p.Client.CoreV1().Services(ns).List(ctx, sel)
	for _, s := range svcs.Items {
		_ = p.Client.CoreV1().Services(ns).Delete(ctx, s.Name, metav1.DeleteOptions{})
	}
}

// labPod is a pod that satisfies the restricted Pod Security standard and
// carries no service account token.
func labPod(ns, name string, args []string, command []string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{Label: "true"}},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			RestartPolicy:                corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "probe", Image: Image, Command: command, Args: args,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Resources: corev1.ResourceRequirements{},
			}},
		},
	}
}

// proberPod tries every probe at once as it starts, when some network
// plugins do not yet apply policy to a new pod, and the forbidden ones
// again ten seconds later. A forbidden destination gets one try a pass, a
// control up to ten, since a new Service can take a moment to answer; each
// try waits three seconds. Every result is a line in the pod's termination
// message: the pass, reached or refused, and the probe's index.
func proberPod(ns string, probes []Probe) *corev1.Pod {
	var script strings.Builder
	script.WriteString(`set +e; : > /dev/termination-log
probe() { r=refused; t=1; while [ "$t" -le "$4" ]; do if /agnhost connect "$3" --timeout=3s >/dev/null 2>&1; then r=reached; break; fi; t=$((t+1)); [ "$t" -le "$4" ] && sleep 1; done; echo "$1 $r $2" >> /dev/termination-log; }
`)
	for i, pr := range probes {
		tries := 1
		if pr.Allowed {
			tries = 10
		}
		fmt.Fprintf(&script, "probe 1 %d '%s' %d &\n", i, pr.Address, tries)
	}
	script.WriteString("wait\nsleep 10\n")
	for i, pr := range probes {
		if !pr.Allowed {
			fmt.Fprintf(&script, "probe 2 %d '%s' 1 &\n", i, pr.Address)
		}
	}
	script.WriteString("wait\n")
	pod := labPod(ns, "lab-prober", nil, []string{"sh", "-c", script.String()})
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
	return pod
}

func terminationMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			return t.Message
		}
	}
	return ""
}

// parse reads the prober's lines; a probe without its lines failed to run.
func parse(probes []Probe, message string) ([]Result, error) {
	type key struct{ pass, i int }
	reached, seen := map[key]bool{}, map[key]bool{}
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pass, err1 := strconv.Atoi(f[0])
		i, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || i < 0 || i >= len(probes) {
			continue
		}
		k := key{pass, i}
		seen[k], reached[k] = true, f[1] == "reached"
	}
	results := make([]Result, len(probes))
	var missing []string
	for i, pr := range probes {
		if !seen[key{1, i}] || !pr.Allowed && !seen[key{2, i}] {
			missing = append(missing, pr.Name)
		}
		results[i] = Result{Probe: pr, Reached: reached[key{1, i}], Later: reached[key{2, i}]}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return results, fmt.Errorf("the prober reported nothing for %s", strings.Join(missing, ", "))
	}
	return results, nil
}

package lab

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

var ctx = context.Background()

func cluster() *fake.Clientset {
	return fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.1"}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default",
			Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"}},
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"172.18.0.2"}}}, Ports: []discoveryv1.EndpointPort{{Port: ptr.To[int32](6443)}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.10"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.3"}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop"},
			Spec: corev1.ServiceSpec{ClusterIP: "10.96.5.5", Ports: []corev1.ServicePort{{Port: 7070}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cart-1", Namespace: "shop"},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: []corev1.ContainerPort{{ContainerPort: 7070}}}}},
			Status: corev1.PodStatus{PodIP: "10.244.1.7"}},
	)
}

func TestTheMatrixCoversEveryForbiddenDestination(t *testing.T) {
	p := &Prover{Client: cluster(), Twins: []string{"shop"}}
	echo := &corev1.Pod{Status: corev1.PodStatus{PodIP: "10.244.2.9"}}
	svc := &corev1.Service{Spec: corev1.ServiceSpec{ClusterIP: "10.96.9.9"}}
	probes, err := p.Matrix(ctx, "sentinel-lab-0", echo, svc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"10.96.9.9:8080": true, "10.244.2.9:8080": true, // the echo control
		"10.96.0.1:443": false, "172.18.0.2:6443": false, "10.96.0.10:53": false, "172.18.0.3:10250": false,
		"10.96.5.5:7070": false, "10.244.1.7:7070": false, "169.254.169.254:80": false, "1.1.1.1:443": false,
	}
	got := map[string]bool{}
	for _, pr := range probes {
		got[pr.Address] = pr.Allowed
	}
	for addr, allowed := range want {
		if a, ok := got[addr]; !ok || a != allowed {
			t.Errorf("%s: in matrix %v, allowed %v; want allowed %v", addr, ok, a, allowed)
		}
	}
}

func TestSealAddsTheInternalRuleOnce(t *testing.T) {
	client := cluster()
	p := &Prover{Client: client}
	for range 2 {
		if err := p.Seal(ctx, "sentinel-lab-0"); err != nil {
			t.Fatal(err)
		}
	}
	nps, _ := client.NetworkingV1().NetworkPolicies("sentinel-lab-0").List(ctx, metav1.ListOptions{})
	if len(nps.Items) != 1 || len(nps.Items[0].Spec.Egress) != 1 || nps.Items[0].Spec.Egress[0].To[0].PodSelector == nil ||
		nps.Items[0].Spec.Egress[0].To[0].NamespaceSelector != nil {
		t.Fatalf("policies = %+v, want one rule letting the Lab's pods reach each other and nothing else", nps.Items)
	}
}

func TestParse(t *testing.T) {
	probes := []Probe{{Name: "echo", Allowed: true}, {Name: "API"}, {Name: "DNS"}, {Name: "kubelet"}}
	results, err := parse(probes, "1 reached 0\n1 refused 1\n1 reached 2\n1 refused 3\n2 refused 1\n2 refused 2\n2 reached 3\n")
	switch {
	case err != nil:
		t.Fatal(err)
	case !results[0].Holds() || !results[1].Holds():
		t.Fatalf("the control and the refused destination should hold: %+v", results)
	case results[2].Holds() || !results[2].Reached || results[2].Later:
		t.Fatalf("DNS leaked as the prober started: %+v", results[2])
	case results[3].Holds() || results[3].Reached || !results[3].Later:
		t.Fatalf("the kubelet leaked later: %+v", results[3])
	}
	if _, err := parse(probes, "1 reached 0\n1 refused 1\n1 refused 2\n1 refused 3\n2 refused 1\n"); err == nil || !strings.Contains(err.Error(), "DNS, kubelet") {
		t.Fatalf("probes without a second pass: %v", err)
	}
}

func TestTheProberIsRestrictedAndTokenless(t *testing.T) {
	pod := proberPod("sentinel-lab-0", []Probe{{Name: "x", Address: "10.0.0.1:80"}})
	c := pod.Spec.Containers[0]
	if *pod.Spec.AutomountServiceAccountToken || !*pod.Spec.SecurityContext.RunAsNonRoot || *c.SecurityContext.AllowPrivilegeEscalation ||
		c.SecurityContext.Capabilities.Drop[0] != "ALL" || !strings.Contains(c.Command[2], "/agnhost connect") ||
		!strings.Contains(c.Command[2], "probe 1 0 '10.0.0.1:80' 1 &") || !strings.Contains(c.Command[2], "probe 2 0 '10.0.0.1:80' 1 &") {
		t.Fatalf("prober = %+v", pod.Spec)
	}
}

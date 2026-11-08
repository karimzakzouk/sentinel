package capability

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type cluster struct {
	client *fake.Clientset
	// allowed holds Rule.String() values the caller may perform.
	allowed map[string]bool
	podErr  map[string]error
}

func newCluster(t *testing.T, objects ...runtime.Object) *cluster {
	t.Helper()
	c := &cluster{client: fake.NewClientset(objects...), allowed: map[string]bool{}, podErr: map[string]error{}}
	disc := c.client.Discovery().(*fakediscovery.FakeDiscovery)
	disc.FakedServerVersion = &version.Info{GitVersion: "v1.37.0"}
	disc.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}, {Name: "pods/log"}, {Name: "nodes"}}},
		{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "networkpolicies"}}},
		{GroupVersion: "admissionregistration.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "validatingadmissionpolicies"}}},
		{GroupVersion: "node.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "runtimeclasses"}}},
		{GroupVersion: "argoproj.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "applications"}}},
	}
	c.client.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{
			UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:sentinel:sentinel"},
		}}, nil
	})
	c.client.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		review.Status.Allowed = c.allowed[ruleFromAttributes(review.Spec.ResourceAttributes).String()]
		return true, review, nil
	})
	c.client.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		review.Status.Allowed = c.allowed[review.Spec.User+" "+ruleFromAttributes(review.Spec.ResourceAttributes).String()]
		return true, review, nil
	})
	c.client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		return true, pod, c.podErr[pod.Name]
	})
	return c
}

func ruleFromAttributes(a *authorizationv1.ResourceAttributes) Rule {
	return Rule{Verb: a.Verb, Group: a.Group, Resource: a.Resource, Subresource: a.Subresource, Namespace: a.Namespace}
}

func (c *cluster) allow(rules ...Rule) {
	for _, r := range rules {
		c.allowed[r.String()] = true
	}
}

func kindNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{ProviderID: "kind://docker/kind/" + name},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
			KubeletVersion: "v1.37.0", KernelVersion: "6.12.113+", ContainerRuntimeVersion: "containerd://2.3.4",
		}},
	}
}

func run(t *testing.T, c *cluster, opts Options) *Report {
	t.Helper()
	rep, err := (&Detector{Client: c.client, Options: opts}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func wantFeature(t *testing.T, rep *Report, name string, enabled bool) Feature {
	t.Helper()
	f, ok := rep.Feature(name)
	if !ok {
		t.Fatalf("feature %s missing", name)
	}
	if f.Enabled != enabled {
		t.Fatalf("feature %s enabled=%v, want %v (%s)", name, f.Enabled, enabled, f.Reason)
	}
	return f
}

func TestReportOnAKindCluster(t *testing.T) {
	helmApp := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}}}
	c := newCluster(t, kindNode("kind-control-plane"), helmApp)
	c.allow(WatcherRules("shop")...)
	c.allow(Rule{Verb: "list", Resource: "nodes"}, Rule{Verb: "watch", Resource: "nodes"})
	c.podErr["sentinel-probe-node-agent"] = apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "sentinel-probe-node-agent",
		errors.New(`violates PodSecurity "baseline:latest": hostPath volumes`))
	c.podErr["sentinel-probe-ebpf"] = apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "sentinel-probe-ebpf",
		errors.New(`admission webhook "policy.example.com" does not support dry run`))

	rep := run(t, c, Options{Namespaces: []string{"shop", "payments"}, AgentNamespace: "sentinel", LabNamespace: "sentinel-lab-0"})

	if rep.Cluster.Platform != "kind" || rep.Cluster.ServerVersion != "v1.37.0" || rep.Cluster.Nodes != 1 {
		t.Fatalf("cluster = %+v", rep.Cluster)
	}
	if rep.Identity != "system:serviceaccount:sentinel:sentinel" {
		t.Fatalf("identity = %q", rep.Identity)
	}
	if f := wantFeature(t, rep, "detect.api", true); !strings.Contains(f.Reason, "shop") || strings.Contains(f.Reason, "payments") {
		t.Fatalf("detect.api reason = %q; want only the readable namespace", f.Reason)
	}
	if ch, _ := rep.Check("access.watch/payments"); ch.Status != Unavailable {
		t.Fatalf("payments access = %+v", ch)
	}
	wantFeature(t, rep, "detect.metrics", false)
	wantFeature(t, rep, "detect.node-agent", false)
	if ch, _ := rep.Check("probe.ebpf"); ch.Status != Unknown {
		t.Fatalf("a webhook-blocked dry run must be unknown, got %+v", ch)
	}
	wantFeature(t, rep, "lab.user-namespaces", true)
	if f := wantFeature(t, rep, "lab", false); !strings.Contains(f.Reason, "not verified") {
		t.Fatalf("lab reason = %q", f.Reason)
	}
	wantFeature(t, rep, "act", false)
	wantFeature(t, rep, "owner.argocd", true)
	wantFeature(t, rep, "owner.flux", false)
	wantFeature(t, rep, "owner.helm", true)
	if ch, _ := rep.Check("security.least-privilege"); ch.Status != Available || len(rep.Warnings) != 0 {
		t.Fatalf("least privilege = %+v, warnings %v", ch, rep.Warnings)
	}
	if len(rep.Nodes) != 1 || strings.Join(rep.Nodes[0].Tiers, ",") != "api" {
		t.Fatalf("nodes = %+v", rep.Nodes)
	}
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil || !strings.Contains(buf.String(), "detect.api") {
		t.Fatalf("WriteText = %v\n%s", err, buf.String())
	}
}

// Without log access Sentinel still watches; only log evidence turns off.
func TestLogAccessIsItsOwnFeature(t *testing.T) {
	c := newCluster(t, kindNode("n1"))
	c.allow(WatcherRules("shop")...)
	rep := run(t, c, Options{Namespaces: []string{"shop"}})
	wantFeature(t, rep, "detect.api", true)
	wantFeature(t, rep, "detect.logs", false)
	if ch, _ := rep.Check("access.logs/shop"); ch.Status != Unavailable || !strings.Contains(ch.Detail, "pods/log") {
		t.Fatalf("log access = %+v", ch)
	}
	c.allow(LogRules("shop")...)
	if f := wantFeature(t, run(t, c, Options{Namespaces: []string{"shop"}}), "detect.logs", true); !strings.Contains(f.Reason, "shop") {
		t.Fatalf("detect.logs reason = %q", f.Reason)
	}
}

func TestOverPrivilegedIdentityIsFlagged(t *testing.T) {
	c := newCluster(t, kindNode("n1"))
	c.allow(WatcherRules("shop")...)
	c.allow(Rule{Verb: "list", Resource: "secrets"}, Rule{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: "shop"})
	rep := run(t, c, Options{Namespaces: []string{"shop"}})
	if ch, _ := rep.Check("security.least-privilege"); ch.Status != Unavailable {
		t.Fatalf("least privilege = %+v", ch)
	}
	if len(rep.Warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", rep.Warnings)
	}
}

func TestActorAndProvenNetworkPolicyEnableActAndLab(t *testing.T) {
	c := newCluster(t, kindNode("n1"))
	c.allow(WatcherRules("shop")...)
	const actor = "system:serviceaccount:sentinel:sentinel-actor"
	for _, r := range ActorRules("shop") {
		c.allowed[actor+" "+r.String()] = true
	}
	proven := &Check{Status: Available, Detail: "enforced"}
	rep := run(t, c, Options{Namespaces: []string{"shop"}, LabNamespace: "sentinel-lab-0", ActorUser: actor, NetworkPolicy: proven})
	wantFeature(t, rep, "act", true)
	wantFeature(t, rep, "lab", true)
}

func TestRunRefusesWithoutNamespaces(t *testing.T) {
	if _, err := (&Detector{Client: fake.NewClientset()}).Run(context.Background()); err == nil {
		t.Fatal("Run guessed namespaces instead of refusing")
	}
}

func TestClassifyDryRun(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	cases := []struct {
		err  error
		want Status
	}{
		{nil, Available},
		{apierrors.NewForbidden(gr, "p", errors.New(`violates PodSecurity "restricted:latest": privileged`)), Unavailable},
		{apierrors.NewForbidden(gr, "p", errors.New(`ValidatingAdmissionPolicy 'sentinel-fence' with binding 'b' denied request`)), Unavailable},
		{apierrors.NewForbidden(gr, "p", errors.New(`admission webhook "gatekeeper" denied the request`)), Unknown},
		{apierrors.NewForbidden(gr, "p", errors.New(`User "x" cannot create resource "pods"`)), Unknown},
		{apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, "p", field.ErrorList{field.Forbidden(field.NewPath("spec", "hostUsers"), "unsupported")}), Unavailable},
	}
	for _, tc := range cases {
		if got, detail := classifyDryRun(tc.err); got != tc.want {
			t.Errorf("classifyDryRun(%v) = %s (%s), want %s", tc.err, got, detail, tc.want)
		}
	}
}

func TestDetectPlatform(t *testing.T) {
	node := func(pid string, labels map[string]string, kubelet string) corev1.Node {
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.NodeSpec{ProviderID: pid},
			Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: kubelet}}}
	}
	empty := apiSet{has: map[string]bool{}}
	cases := map[string]corev1.Node{
		"kind":     node("kind://docker/x/y", nil, "v1.37.0"),
		"k3s":      node("k3s://n1", nil, "v1.37.0+k3s1"),
		"minikube": node("", map[string]string{"minikube.k8s.io/name": "minikube"}, "v1.37.0"),
		"eks":      node("aws:///eu-west-1a/i-1", map[string]string{"eks.amazonaws.com/nodegroup": "ng"}, "v1.37.0-eks"),
		"gke":      node("gce://p/z/n", map[string]string{"cloud.google.com/gke-nodepool": "default"}, "v1.37.0-gke.1"),
		"aks":      node("azure:///subscriptions/x", map[string]string{"kubernetes.azure.com/cluster": "c"}, "v1.37.0"),
		"unknown":  node("", nil, "v1.37.0"),
	}
	for want, n := range cases {
		if got := detectPlatform([]corev1.Node{n}, empty); got != want {
			t.Errorf("detectPlatform = %s, want %s", got, want)
		}
	}
	openshift := apiSet{has: map[string]bool{"config.openshift.io/clusterversions": true}}
	if got := detectPlatform(nil, openshift); got != "openshift" {
		t.Errorf("detectPlatform = %s, want openshift", got)
	}
}

func TestDetectNetworkPlugin(t *testing.T) {
	annotated := func(annotations map[string]string) []corev1.Node {
		return []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}}
	}
	empty := apiSet{has: map[string]bool{}}
	calicoCRDs := apiSet{has: map[string]bool{"crd.projectcalico.org/networkpolicies": true}}
	flannel := annotated(map[string]string{"flannel.alpha.coreos.com/backend-type": "host-gw"})
	cases := []struct {
		nodes    []corev1.Node
		apis     apiSet
		platform string
		want     string
	}{
		{flannel, empty, "kind", "flannel"},
		{flannel, calicoCRDs, "", "calico"}, // Canal
		{annotated(map[string]string{"projectcalico.org/IPv4Address": "10.0.0.1/24"}), empty, "", "calico"},
		{annotated(nil), empty, "kind", "kindnet (kind default, unless replaced)"},
		{annotated(nil), empty, "eks", ""},
	}
	for _, tc := range cases {
		if got := detectNetworkPlugin(tc.nodes, tc.apis, tc.platform); got != tc.want {
			t.Errorf("detectNetworkPlugin(%v, %s) = %q, want %q", tc.nodes[0].Annotations, tc.platform, got, tc.want)
		}
	}
}

func TestNodeCoverage(t *testing.T) {
	fargate := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "fargate-1", Labels: map[string]string{"eks.amazonaws.com/compute-type": "fargate"}}}
	old := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old-1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KernelVersion: "4.14.0"}}}
	modern := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new-1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KernelVersion: "6.1.0"}}}
	got := coverNodes([]corev1.Node{modern, fargate, old}, probeResults{nodeAgent: Available, ebpf: Available}, true)
	tiers := map[string]string{}
	for _, n := range got {
		tiers[n.Name] = strings.Join(n.Tiers, ",")
	}
	want := map[string]string{"fargate-1": "api", "old-1": "api,node-agent", "new-1": "api,node-agent,ebpf"}
	for name, w := range want {
		if tiers[name] != w {
			t.Errorf("%s tiers = %q, want %q", name, tiers[name], w)
		}
	}
}

func TestKernelAtLeast(t *testing.T) {
	cases := map[string]bool{"6.12.113+": true, "5.8.0": true, "5.15.0-1052-aws": true, "5.4.0": false, "4.19": false, "garbage": false}
	for v, want := range cases {
		if got := kernelAtLeast(v, 5, 8); got != want {
			t.Errorf("kernelAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestNetworkPolicyProbeReportsUnknownWhenItCannotStart(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "x", errors.New("no"))
	})
	got := NetworkPolicyProbe{Client: client, Namespace: "lab"}.Run(context.Background())
	if got.Status != Unknown || got.ID != NetworkPolicyCheckID {
		t.Fatalf("probe = %+v, want unknown", got)
	}
}

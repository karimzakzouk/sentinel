//go:build e2e

// Package e2e tests Sentinel on a real cluster brought up by hack/e2e-up.sh.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/harness"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/lab"
	"github.com/karimzakzouk/sentinel/internal/model"
)

const (
	sentinelNS     = "sentinel"
	smokeNS        = "e2e-smoke"
	labNS          = "sentinel-lab-0"
	controllerUser = "system:serviceaccount:sentinel:sentinel"
	actorUser      = "system:serviceaccount:sentinel-actor:sentinel-actor"
	labUser        = "system:serviceaccount:sentinel:sentinel-lab"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func flags(as string) kube.Flags {
	return kube.Flags{Context: "kind-" + env("E2E_CLUSTER", "sentinel-e2e"), As: as}
}

func clientAs(t *testing.T, as string) kubernetes.Interface {
	t.Helper()
	c, err := flags(as).Clientset("sentinel-e2e")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dynamicAs(t *testing.T, as string) dynamic.Interface {
	t.Helper()
	cfg, err := flags(as).RESTConfig("sentinel-e2e")
	if err != nil {
		t.Fatal(err)
	}
	return dynamic.NewForConfigOrDie(cfg)
}

// actionRequest is a request to restart web, which nothing will run: the
// e2e namespaces hold no SentinelPolicy.
func actionRequest(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "ActionRequest",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{"incidentId": "fence-test", "action": "restart", "reason": "fence test",
			"expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
			"target":    map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": ns, "name": "web"}},
	}}
}

func eventually(t *testing.T, timeout time.Duration, what string, ok func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		done, err := ok()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s (last error: %v)", timeout, what, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// waitForLogEntry waits for the controller to log, at or after since, a JSON
// entry that match accepts, and returns it.
func waitForLogEntry(t *testing.T, client kubernetes.Interface, since time.Time, timeout time.Duration, what string, match func(entry map[string]any) bool) map[string]any {
	t.Helper()
	ctx := context.Background()
	var found map[string]any
	eventually(t, timeout, what, func() (bool, error) {
		pods, err := client.CoreV1().Pods(sentinelNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=controller"})
		if err != nil || len(pods.Items) == 0 {
			return false, err
		}
		opts := &corev1.PodLogOptions{}
		if !since.IsZero() {
			opts.SinceTime = &metav1.Time{Time: since}
		}
		raw, err := client.CoreV1().Pods(sentinelNS).GetLogs(pods.Items[0].Name, opts).DoRaw(ctx)
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && match(entry) {
				found = entry
				return true, nil
			}
		}
		return false, nil
	})
	return found
}

func TestControllerRecordsACapabilityReport(t *testing.T) {
	ctx := context.Background()
	admin := clientAs(t, "")
	eventually(t, 3*time.Minute, "the controller to be available", func() (bool, error) {
		d, err := admin.AppsV1().Deployments(sentinelNS).Get(ctx, "sentinel", metav1.GetOptions{})
		return err == nil && d.Status.AvailableReplicas == 1, err
	})
	report := waitForLogEntry(t, admin, time.Time{}, 2*time.Minute, "a capability report in the controller log", func(e map[string]any) bool {
		return e["msg"] == "capability report recorded"
	})
	if report["identity"] != controllerUser || report["platform"] != "kind" {
		t.Fatalf("report logged as %v on %v", report["identity"], report["platform"])
	}
}

// The UI and its API listen on localhost inside the pod; kubectl
// port-forward is the way in, and the image carries the built web app.
func TestTheUIAnswersThroughPortForward(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	const port = "18090"
	pf := exec.CommandContext(ctx, "kubectl", "--context", flags("").Context, "-n", sentinelNS,
		"port-forward", "deploy/sentinel", port+":8090")
	if err := pf.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = pf.Wait()
	})
	fetch := func(path string) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+path, nil)
		if err != nil {
			return 0, "", err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		return res.StatusCode, string(body), err
	}
	var summary struct {
		Mode   string `json:"mode"`
		Ledger struct {
			HeadSeq int64 `json:"headSeq"`
		} `json:"ledger"`
	}
	eventually(t, time.Minute, "the API through port-forward", func() (bool, error) {
		status, body, err := fetch("/api/summary")
		if err != nil || status != http.StatusOK {
			return false, fmt.Errorf("status %d: %w", status, err)
		}
		return json.Unmarshal([]byte(body), &summary) == nil, nil
	})
	if summary.Mode != "observe" || summary.Ledger.HeadSeq == 0 {
		t.Fatalf("summary = %+v", summary)
	}
	status, body, err := fetch("/")
	if err != nil || status != http.StatusOK || !strings.Contains(body, `<div id="root">`) {
		t.Fatalf("the web app is not in the image: %d %v\n%.300s", status, err, body)
	}
}

func TestCapabilityReportAsSentinel(t *testing.T) {
	rep, err := (&capability.Detector{Client: clientAs(t, controllerUser), Options: capability.Options{
		Namespaces: []string{smokeNS, "boutique"}, AgentNamespace: sentinelNS, LabNamespace: labNS, ActorUser: actorUser,
	}}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cluster.Platform != "kind" || rep.Cluster.Nodes != 2 || rep.Identity != controllerUser {
		t.Fatalf("cluster %+v as %s", rep.Cluster, rep.Identity)
	}
	if c, _ := rep.Check("security.least-privilege"); c.Status != capability.Available || len(rep.Warnings) > 0 {
		t.Fatalf("least privilege: %+v, warnings %v", c, rep.Warnings)
	}
	want := map[string]bool{"detect.api": true, "detect.logs": true, "detect.metrics": true, "act": true, "lab": false,
		"lab.user-namespaces": true, "detect.node-agent": true}
	for name, enabled := range want {
		f, ok := rep.Feature(name)
		if !ok || f.Enabled != enabled {
			t.Errorf("feature %s = %+v, want enabled=%v", name, f, enabled)
		}
	}
}

func TestNetworkPolicyProbeMatchesTheNetworkPlugin(t *testing.T) {
	network := env("E2E_NETWORK", "kindnet")
	got := capability.NetworkPolicyProbe{Client: clientAs(t, ""), Namespace: labNS, Settle: 30 * time.Second}.Run(context.Background())
	t.Logf("%s: %s (%s)", network, got.Status, got.Detail)
	switch network {
	case "calico", "cilium":
		if got.Status != capability.Available {
			t.Fatalf("%s enforces NetworkPolicy, but the probe says %s", network, got.Status)
		}
	case "flannel":
		if got.Status != capability.Unavailable {
			t.Fatalf("Flannel never enforces NetworkPolicy, but the probe says %s", got.Status)
		}
	default:
		// kindnet enforces only on kernels with NFQUEUE, so either answer can be
		// right; what matters is that the probe reaches one.
		if got.Status == capability.Unknown {
			t.Fatalf("the probe could not decide: %s", got.Detail)
		}
	}
}

// The Lab's seal holds exactly where the cluster enforces NetworkPolicy:
// every forbidden destination is refused there, and where policy is not
// enforced the matrix must catch leaks, or it proves nothing. Either way
// the positive controls answer, so "refused" never means "probe broken".
func TestTheLabSealHoldsOnlyWherePolicyIsEnforced(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	enforced := capability.NetworkPolicyProbe{Client: client, Namespace: labNS, Settle: 30 * time.Second}.Run(ctx)
	if enforced.Status == capability.Unknown {
		t.Fatalf("the NetworkPolicy probe could not decide: %s", enforced.Detail)
	}
	p := &lab.Prover{Client: client, Twins: []string{smokeNS, "boutique"}, Timeout: 3 * time.Minute}
	if err := p.Seal(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.NetworkingV1().NetworkPolicies(labNS).Delete(ctx, "lab-internal", metav1.DeleteOptions{})
	})
	results, err := p.Prove(ctx, labNS, nil)
	if err != nil {
		t.Fatal(err)
	}
	var leaks []string
	for _, r := range results {
		t.Logf("%-40s %-22s allowed=%-5v reached=%-5v later=%v", r.Name, r.Address, r.Allowed, r.Reached, r.Later)
		switch {
		case r.Allowed && !r.Reached:
			t.Fatalf("the positive control %s did not answer: a refusal would mean nothing", r.Name)
		case !r.Holds():
			leaks = append(leaks, r.Name)
		}
	}
	switch enforced.Status {
	case capability.Available:
		var failing []string
		for _, r := range results {
			if !r.Holds() && !r.Allowed && (strictEgress() || r.Later && !r.Outside) {
				failing = append(failing, r.Name)
			}
		}
		if len(failing) > 0 {
			t.Fatalf("NetworkPolicy is enforced, yet the seal leaks to %v", failing)
		}
		if len(leaks) > 0 {
			t.Logf("this network plugin lets pods reach %v, outside the cluster or as they start; the Lab refuses to run here", leaks)
		}
	case capability.Unavailable:
		if len(leaks) == 0 {
			t.Fatal("NetworkPolicy is not enforced, yet the matrix caught no leak: it proves nothing")
		}
	}
}

// An Experiment runs only behind a proven seal: the Lab passes it where the
// cluster enforces NetworkPolicy, and elsewhere stays off and says why
// rather than running unsealed. Where it is enforced, a policy someone else
// added that opens the namespace must fail the next experiment: the Lab
// trusts traffic, not the policies it reads. Either way it leaves nothing
// behind.
func TestALabExperimentRunsOnlyBehindAProvenSeal(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	phase, msg := runExperiment(t, "prove-seal")
	enforced := capability.NetworkPolicyProbe{Client: client, Namespace: labNS, Settle: 30 * time.Second}.Run(ctx)
	switch enforced.Status {
	case capability.Available:
		if phase != lab.PhasePassed && !pluginLimit(phase, msg) {
			t.Fatalf("NetworkPolicy is enforced, yet the experiment ended %s: %s", phase, msg)
		}
		leak := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "e2e-leak", Namespace: labNS},
			Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}}}}}}}
		if _, err := client.NetworkingV1().NetworkPolicies(labNS).Create(ctx, leak, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = client.NetworkingV1().NetworkPolicies(labNS).Delete(ctx, leak.Name, metav1.DeleteOptions{})
		})
		phase, msg = runExperiment(t, "prove-leaky-seal")
		if phase != lab.PhaseFailed || !strings.Contains(msg, "the seal leaks") {
			t.Fatalf("a policy opened the Lab, yet the experiment ended %s: %s", phase, msg)
		}
	case capability.Unavailable:
		if phase != lab.PhaseFailed || !strings.Contains(msg, "the Lab stays off") {
			t.Fatalf("NetworkPolicy is not enforced, yet the experiment ended %s: %s", phase, msg)
		}
	default:
		t.Fatalf("the NetworkPolicy probe could not decide: %s", enforced.Detail)
	}
	if _, err := client.NetworkingV1().NetworkPolicies(labNS).Get(ctx, "lab-internal", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the seal outlived the experiment (%v)", err)
	}
	eventually(t, time.Minute, "the Lab's pods to go", func() (bool, error) {
		pods, err := client.CoreV1().Pods(labNS).List(ctx, metav1.ListOptions{LabelSelector: lab.Label})
		return err == nil && len(pods.Items) == 0, err
	})
}

// strictEgress says the network plugin is known to apply egress policy to
// addresses outside the cluster, and to new pods from their first packet,
// so any leak is a failure.
func strictEgress() bool {
	return os.Getenv("E2E_NETWORK") == "calico" || os.Getenv("E2E_NETWORK") == "cilium"
}

// pluginLimit says the Lab refused a cluster for a known limit of its
// network plugin, outside the cluster or as pods start, which only a
// plugin not known to be strict may have.
func pluginLimit(phase v1alpha1.ExperimentPhase, msg string) bool {
	return !strictEgress() && phase == lab.PhaseFailed &&
		(strings.HasPrefix(msg, "the seal leaks outside the cluster") || strings.HasPrefix(msg, "the seal leaks while a pod starts"))
}

// runExperiment creates a prove Experiment in the Lab namespace and waits
// for its verdict.
func runExperiment(t *testing.T, name string) (v1alpha1.ExperimentPhase, string) {
	t.Helper()
	phase, msg, _ := runExperimentOn(t, name, nil)
	return phase, msg
}

// runExperimentOn runs an Experiment in the Lab namespace: a reproduction
// of workload, or a prove when workload is nil. It returns the verdict and
// the fidelity list.
func runExperimentOn(t *testing.T, name string, workload map[string]any) (v1alpha1.ExperimentPhase, string, []string) {
	t.Helper()
	ctx := context.Background()
	exps := dynamicAs(t, "").Resource(lab.ExperimentsResource).Namespace(labNS)
	_ = exps.Delete(ctx, name, metav1.DeleteOptions{})
	spec := map[string]any{"incidentId": "e2e", "purpose": "prove", "labNamespace": labNS}
	if workload != nil {
		spec["purpose"], spec["workload"] = "reproduce", workload
	}
	exp := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "Experiment",
		"metadata": map[string]any{"name": name, "namespace": labNS}, "spec": spec,
	}}
	if _, err := exps.Create(ctx, exp, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exps.Delete(ctx, name, metav1.DeleteOptions{}) })
	var phase v1alpha1.ExperimentPhase
	var msg string
	var fidelity []string
	eventually(t, 12*time.Minute, "experiment "+name+" to finish", func() (bool, error) {
		u, err := exps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		p, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		msg, _, _ = unstructured.NestedString(u.Object, "status", "message")
		fidelity, _, _ = unstructured.NestedStringSlice(u.Object, "status", "fidelity")
		phase = v1alpha1.ExperimentPhase(p)
		return phase == lab.PhasePassed || phase == lab.PhaseFailed || phase == lab.PhaseNotSure || phase == lab.PhaseAborted, nil
	})
	t.Logf("%s: %s: %s", name, phase, msg)
	return phase, msg, fidelity
}

// The two-tier front's world, built directly as the Lab's identity: the
// front and the backend it calls are copied into a sealed namespace with a
// Lab DNS. The copies
// answer by their production names, the API server's name resolves to
// nothing, and production's Secret appears nowhere in the Lab. This runs
// on any cluster; where policy is enforced, the reproduction test below
// also proves the seal with the world up.
func TestTheLabBuildsAWorkloadsStatelessWorld(t *testing.T) {
	ctx := context.Background()
	client, admin := clientAs(t, labUser), clientAs(t, "")
	ref := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: smokeNS, Name: "front"}
	world, err := lab.ReadWorld(ctx, client, smokeNS, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lab.Synthesize(world, ref, labNS)
	if err != nil {
		t.Fatal(err)
	}
	var copied []string
	for _, w := range plan.Workloads {
		copied = append(copied, w.Name)
	}
	if !slices.Equal(copied, []string{"front", "backend"}) {
		t.Fatalf("copied %v; want the front and the backend it calls", copied)
	}
	for _, want := range []string{"runs one replica instead of 2", "API_TOKEN holds a placeholder instead of secret front-secrets",
		"names api.payments.example, which is outside the Lab"} {
		if !slices.ContainsFunc(plan.Fidelity, func(f string) bool { return strings.Contains(f, want) }) {
			t.Errorf("the fidelity list lacks %q: %v", want, plan.Fidelity)
		}
	}
	p := &lab.Prover{Client: client, Twins: []string{smokeNS}, Timeout: 3 * time.Minute}
	if err := p.Clean(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	if err := p.Seal(ctx, labNS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Clean(ctx, labNS)
		_ = client.NetworkingV1().NetworkPolicies(labNS).Delete(ctx, "lab-internal", metav1.DeleteOptions{})
	})
	built, err := p.Build(ctx, labNS, smokeNS, "cluster.local", plan)
	if err != nil {
		t.Fatal(err)
	}
	for w, state := range built.States {
		if state != "ready" {
			t.Fatalf("the copy of %s is %s", w, state)
		}
	}
	pods, err := admin.CoreV1().Pods(labNS).List(ctx, metav1.ListOptions{LabelSelector: lab.Label})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(pods); strings.Contains(string(raw), "canary-") {
		t.Fatal("production's Secret reached the Lab")
	}
	probes := lab.WorldProbes(built.Services, smokeNS, "cluster.local")
	results, err := p.Prove(ctx, labNS, &lab.Inside{Probes: probes, DNS: lab.DNSConfig(built.DNS, smokeNS, "cluster.local")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results[len(results)-len(probes):] {
		t.Logf("%-45s %-48s allowed=%-5v reached=%v", r.Name, r.Address, r.Allowed, r.Reached)
		if r.Allowed != r.Reached {
			t.Errorf("%s: allowed=%v but reached=%v", r.Name, r.Allowed, r.Reached)
		}
	}
	if log, err := p.DNSLog(ctx, labNS); err == nil {
		t.Logf("names the world asked for that the Lab does not provide: %v",
			lab.Unresolved(log, lab.DNSConfig(built.DNS, smokeNS, "cluster.local").Searches, []string{"kubernetes.default.svc.cluster.local"}))
	}
}

// A reproduction through the Lab: it proves the seal, copies the front's
// world, proves the seal again with the world up, and reports how the copy
// differs from production. It only means something where the Lab runs.
func TestALabReproductionRunsTheWorldBehindAProvenSeal(t *testing.T) {
	enforced := capability.NetworkPolicyProbe{Client: clientAs(t, ""), Namespace: labNS, Settle: 30 * time.Second}.Run(context.Background())
	if enforced.Status != capability.Available {
		t.Skipf("the Lab refuses this cluster (%s); TestALabExperimentRunsOnlyBehindAProvenSeal covers that", enforced.Detail)
	}
	phase, msg, fidelity := runExperimentOn(t, "reproduce-front",
		map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": smokeNS, "name": "front"})
	if pluginLimit(phase, msg) {
		t.Skipf("the Lab refuses this network plugin: %s", msg)
	}
	if phase != lab.PhaseNotSure || !strings.Contains(msg, "the world ran sealed (front ready; backend ready)") {
		t.Fatalf("the reproduction ended %s: %s", phase, msg)
	}
	if !slices.ContainsFunc(fidelity, func(f string) bool { return strings.Contains(f, "API_TOKEN holds a placeholder") }) {
		t.Fatalf("the fidelity list lacks the secret placeholder: %v", fidelity)
	}
	pods, err := clientAs(t, "").CoreV1().Pods(labNS).List(context.Background(), metav1.ListOptions{LabelSelector: lab.Label})
	if err != nil || len(pods.Items) != 0 {
		t.Fatalf("the world outlived its experiment: %d pods (%v)", len(pods.Items), err)
	}
}

func TestFenceBlocksForbiddenWrites(t *testing.T) {
	ctx := context.Background()
	actor, controller := clientAs(t, actorUser), clientAs(t, controllerUser)
	dry := metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}
	patch := func(c kubernetes.Interface, ns, kind, name, body string, sub ...string) func() error {
		return func() error {
			var err error
			if kind == "statefulset" {
				_, err = c.AppsV1().StatefulSets(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(body), dry, sub...)
			} else {
				_, err = c.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(body), dry, sub...)
			}
			return err
		}
	}
	probe := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fence-test", Namespace: sentinelNS},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "registry.k8s.io/pause:3.10"}}, AutomountServiceAccountToken: ptr.To(false)},
	}
	createPod := func(dryRun bool) func() error {
		return func() error {
			opts := metav1.CreateOptions{}
			if dryRun {
				opts.DryRun = []string{metav1.DryRunAll}
			}
			_, err := controller.CoreV1().Pods(sentinelNS).Create(ctx, probe, opts)
			if err == nil && !dryRun {
				_ = clientAs(t, "").CoreV1().Pods(sentinelNS).Delete(ctx, probe.Name, metav1.DeleteOptions{})
			}
			return err
		}
	}
	restart := `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-01-01T00:00:00Z"}}}}}`
	requests := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "actionrequests"}
	createRequest := func(as dynamic.Interface, ns string) func() error {
		return func() error {
			_, err := as.Resource(requests).Namespace(ns).Create(ctx, actionRequest(ns, "fence-test-dry"),
				metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		}
	}
	if _, err := dynamicAs(t, "").Resource(requests).Namespace(smokeNS).Create(ctx, actionRequest(smokeNS, "fence-test"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = dynamicAs(t, "").Resource(requests).Namespace(smokeNS).Delete(ctx, "fence-test", metav1.DeleteOptions{})
	})
	updateStatus := func() error {
		// The actor may already have rejected it: no policy lets it run.
		u, err := dynamicAs(t, actorUser).Resource(requests).Namespace(smokeNS).Get(ctx, "fence-test", metav1.GetOptions{})
		if err != nil {
			return err
		}
		u.Object["status"] = map[string]any{"phase": "Rejected", "message": "fence test"}
		_, err = dynamicAs(t, actorUser).Resource(requests).Namespace(smokeNS).UpdateStatus(ctx, u,
			metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		return err
	}
	me, err := clientAs(t, "").AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	approveAs := func(who string) func() error {
		return func() error {
			patch := fmt.Sprintf(`{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/approved-by":%q}}}`, who)
			_, err := dynamicAs(t, "").Resource(requests).Namespace(smokeNS).Patch(ctx, "fence-test", types.MergePatchType, []byte(patch),
				metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		}
	}
	labClient := clientAs(t, labUser)
	labPod := func(ns, image string, token bool, volumes ...corev1.Volume) func() error {
		return func() error {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "fence-test", Namespace: ns},
				Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr.To(token), Volumes: volumes,
					SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{Name: "c", Image: image, SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}}},
			}
			_, err := labClient.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		}
	}
	labPolicy := func(peer networkingv1.NetworkPolicyPeer) func() error {
		return func() error {
			np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "fence-test", Namespace: labNS},
				Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
					Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{peer}}}}}
			_, err := labClient.NetworkingV1().NetworkPolicies(labNS).Create(ctx, np, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		}
	}
	cases := []struct {
		name string
		do   func() error
		// fence means the write must be refused by a sentinel-fence policy, not merely by RBAC.
		allowed, fence bool
	}{
		{"lab starts a probe pod", labPod(labNS, capability.ProbeImage, false), true, false},
		{"lab starts a copy of a production pod", labPod(labNS, "registry.k8s.io/pause:3.10", false), true, false},
		{"lab starts a pod that carries a token", labPod(labNS, capability.ProbeImage, true), false, true},
		{"lab starts a pod that mounts a secret", labPod(labNS, capability.ProbeImage, false, corev1.Volume{Name: "s",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "front-secrets"}}}), false, true},
		{"lab starts a pod outside its pool", labPod(smokeNS, capability.ProbeImage, false), false, false},
		{"lab seals its namespace", labPolicy(networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}), true, false},
		{"lab opens its seal to an address", labPolicy(networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}}), false, true},
		{"lab opens its seal to another namespace", labPolicy(networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}), false, true},
		{"a person approves as themselves", approveAs(me.Status.UserInfo.Username), true, false},
		{"a person approves in someone else's name", approveAs("mallory"), false, true},
		{"actor scales within bounds", patch(actor, smokeNS, "deployment", "web", `{"spec":{"replicas":2}}`), true, false},
		{"actor restarts a Deployment", patch(actor, smokeNS, "deployment", "web", restart), true, false},
		{"actor scales a StatefulSet within bounds", patch(actor, smokeNS, "statefulset", "store", `{"spec":{"replicas":3}}`, "scale"), true, false},
		{"actor scales past the bound", patch(actor, smokeNS, "deployment", "web", `{"spec":{"replicas":9}}`, "scale"), false, true},
		// A rollback restores an earlier template, whatever changed in it.
		{"actor restores a Deployment's template", patch(actor, smokeNS, "deployment", "web", `{"spec":{"template":{"spec":{"containers":[{"name":"web","image":"busybox"}]}}}}`), true, false},
		{"actor pauses a Deployment", patch(actor, smokeNS, "deployment", "web", `{"spec":{"paused":true}}`), false, true},
		{"actor changes a Deployment's labels", patch(actor, smokeNS, "deployment", "web", `{"metadata":{"labels":{"x":"y"}}}`), false, true},
		{"actor changes a StatefulSet's image", patch(actor, smokeNS, "statefulset", "store", `{"spec":{"template":{"spec":{"containers":[{"name":"store","image":"busybox"}]}}}}`), false, true},
		{"actor changes StatefulSet pod labels", patch(actor, smokeNS, "statefulset", "store", `{"spec":{"template":{"metadata":{"labels":{"x":"y"}}}}}`), false, true},
		{"actor acts outside its namespaces", patch(actor, "kube-system", "deployment", "coredns", `{"spec":{"replicas":2}}`), false, false},
		{"actor records an action request's outcome", updateStatus, true, false},
		{"actor makes its own action request", createRequest(dynamicAs(t, actorUser), smokeNS), false, false},
		{"controller creates a real pod", createPod(false), false, true},
		{"controller makes a dry-run probe", createPod(true), true, false},
		{"controller asks the actor for an action", createRequest(dynamicAs(t, controllerUser), smokeNS), true, false},
		{"controller asks for an action outside its namespaces", createRequest(dynamicAs(t, controllerUser), "kube-system"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			switch {
			case tc.allowed && err != nil:
				t.Fatalf("expected the write to be allowed: %v", err)
			case !tc.allowed && err == nil:
				t.Fatal("expected the write to be refused, but it was allowed")
			case !tc.allowed && !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err):
				t.Fatalf("refused for an unexpected reason: %v", err)
			case tc.fence && !strings.Contains(err.Error(), "ValidatingAdmissionPolicy 'sentinel-fence"):
				t.Fatalf("refused, but not by the fence: %v", err)
			}
		})
	}
}

// The bad-image scenario end to end: the harness records the ground truth,
// and Sentinel, observing only, opens an incident with the right fault class,
// blames the image change and proposes a rollback without making it.
func newInjector(t *testing.T) (*harness.Injector, kubernetes.Interface) {
	t.Helper()
	cfg, err := flags("").RESTConfig("sentinel-e2e")
	if err != nil {
		t.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	return &harness.Injector{
		Client:  client,
		Dynamic: dynamic.NewForConfigOrDie(cfg),
		Mapper:  restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(client.Discovery())),
	}, client
}

// inject injects a scenario that must manifest, and reverts it when the test ends.
func inject(t *testing.T, in *harness.Injector, s *harness.Scenario) *harness.Run {
	t.Helper()
	ctx := context.Background()
	run, err := in.Inject(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Revert(ctx, run) })
	if run.ManifestedAt == nil {
		t.Fatalf("fault did not manifest: %s", run.Observation)
	}
	return run
}

func revertAndRecover(t *testing.T, in *harness.Injector, client kubernetes.Interface, run *harness.Run) {
	t.Helper()
	ctx := context.Background()
	if err := in.Revert(ctx, run); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Minute, "web to recover after the revert", func() (bool, error) {
		d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		return err == nil && rolledOut(d), err
	})
}

// waitForQuiet waits until a workload has no open incident, so a new fault
// opens its own rather than joining one left by an earlier run.
func waitForQuiet(t *testing.T, client kubernetes.Interface, workload string) {
	t.Helper()
	ctx := context.Background()
	eventually(t, 5*time.Minute, "no open incident on "+workload, func() (bool, error) {
		pods, err := client.CoreV1().Pods(sentinelNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=controller"})
		if err != nil || len(pods.Items) == 0 {
			return false, err
		}
		raw, err := client.CoreV1().Pods(sentinelNS).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			return false, err
		}
		open := 0
		for _, line := range strings.Split(string(raw), "\n") {
			var e map[string]any
			if json.Unmarshal([]byte(line), &e) != nil || e["workload"] != workload {
				continue
			}
			switch e["msg"] {
			case "incident opened":
				open++
			case "incident resolved":
				open--
			}
		}
		return open <= 0, nil
	})
}

func TestBadImageScenario(t *testing.T) {
	ctx := context.Background()
	in, client := newInjector(t)
	waitForQuiet(t, client, "Deployment.apps/"+smokeNS+"/web")
	s, err := harness.LoadScenario("../scenarios/smoke/bad-image.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run := inject(t, in, s)
	t.Run("harness records ground truth", func(t *testing.T) {
		if run.GroundTruth.FaultClass != "deploy.bad-image" || run.GroundTruth.RootCause.Name != "web" {
			t.Fatalf("ground truth = %+v", run.GroundTruth)
		}
	})
	t.Run("Sentinel explains it without acting", func(t *testing.T) {
		injected, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		incident := waitForLogEntry(t, client, run.InjectedAt, 2*time.Minute, "an incident on web", func(e map[string]any) bool {
			return e["msg"] == "incident opened" && e["workload"] == "Deployment.apps/"+smokeNS+"/web"
		})
		fields, _ := incident["culpritFields"].([]any)
		if incident["class"] != "deploy.bad-image" || incident["decision"] != "rollout.undo" || !slices.Contains(fields, any("image:web")) {
			t.Fatalf("incident = %v, want class deploy.bad-image, culprit image:web, decision rollout.undo", incident)
		}
		d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Generation != injected.Generation {
			t.Fatalf("web changed while Sentinel was only observing: generation %d, then %d", injected.Generation, d.Generation)
		}
	})
	revertAndRecover(t, in, client, run)
}

// With a SentinelPolicy that lets Sentinel roll back on its own, the
// bad-image scenario ends in Sentinel's rollback: the controller asks, the
// actor restores the earlier template, and the controller verifies that the
// incident's signals cleared.
func TestAssistModeRollsBackABadImage(t *testing.T) {
	ctx := context.Background()
	in, client := newInjector(t)
	waitForQuiet(t, client, "Deployment.apps/"+smokeNS+"/web")
	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	admin := dynamicAs(t, "")
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-assist", "namespace": smokeNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "rollout.undo", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(smokeNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(smokeNS).Delete(ctx, "e2e-assist", metav1.DeleteOptions{})
	})
	before, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image := before.Spec.Template.Spec.Containers[0].Image
	s, err := harness.LoadScenario("../scenarios/smoke/bad-image.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run := inject(t, in, s)

	waitForLogEntry(t, client, run.InjectedAt, 2*time.Minute, "a request for the rollback", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "rollout.undo"
	})
	eventually(t, 3*time.Minute, "web back on its image, rolled out", func() (bool, error) {
		d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return d.Spec.Template.Spec.Containers[0].Image == image && d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas == *d.Spec.Replicas && d.Status.AvailableReplicas == *d.Spec.Replicas, nil
	})
	verified := waitForLogEntry(t, client, run.InjectedAt, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
		return e["msg"] == "action verified" || e["msg"] == "action unverified"
	})
	if verified["msg"] != "action verified" {
		t.Fatalf("the rollback was not verified: %v", verified)
	}
	d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if managers := managedBy(d.ManagedFields); !slices.Contains(managers, "sentinel-actor") {
		t.Fatalf("web's managers are %v; the rollback was not the actor's", managers)
	}
	revertAndRecover(t, in, client, run)
}

// With a policy that holds rollbacks for approval, Sentinel asks and waits;
// once a person approves, the actor rolls back and records who approved.
func TestAnApprovedRollbackRunsAndNamesItsApprover(t *testing.T) {
	ctx := context.Background()
	in, client := newInjector(t)
	waitForQuiet(t, client, "Deployment.apps/"+smokeNS+"/web")
	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	requests := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "actionrequests"}
	admin := dynamicAs(t, "")
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-approve", "namespace": smokeNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "rollout.undo", "level": "approve"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(smokeNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(smokeNS).Delete(ctx, "e2e-approve", metav1.DeleteOptions{})
	})
	me, err := clientAs(t, "").AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image := before.Spec.Template.Spec.Containers[0].Image
	s, err := harness.LoadScenario("../scenarios/smoke/bad-image.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run := inject(t, in, s)

	var name string
	eventually(t, 3*time.Minute, "a rollback request waiting for approval", func() (bool, error) {
		list, err := admin.Resource(requests).Namespace(smokeNS).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, u := range list.Items {
			phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
			action, _, _ := unstructured.NestedString(u.Object, "spec", "action")
			if action == "rollout.undo" && phase == "Pending" && u.GetCreationTimestamp().After(run.InjectedAt.Add(-time.Second)) {
				name = u.GetName()
				return true, nil
			}
		}
		return false, nil
	})
	d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Spec.Template.Spec.Containers[0].Image == image {
		t.Fatal("web was rolled back before anyone approved")
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/approved-by":%q}}}`, me.Status.UserInfo.Username)
	if _, err := admin.Resource(requests).Namespace(smokeNS).Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Minute, "the approved rollback", func() (bool, error) {
		u, err := admin.Resource(requests).Namespace(smokeNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		approvedBy, _, _ := unstructured.NestedString(u.Object, "status", "approvedBy")
		switch phase {
		case "Succeeded":
			if approvedBy != me.Status.UserInfo.Username {
				return false, fmt.Errorf("approvedBy = %q, want %q", approvedBy, me.Status.UserInfo.Username)
			}
			return true, nil
		case "Failed", "Rejected", "Expired":
			msg, _, _ := unstructured.NestedString(u.Object, "status", "message")
			t.Fatalf("the approved request ended %s: %s", phase, msg)
		}
		return false, nil
	})
	eventually(t, 3*time.Minute, "web back on its image", func() (bool, error) {
		d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		return err == nil && d.Spec.Template.Spec.Containers[0].Image == image, err
	})
	revertAndRecover(t, in, client, run)
}

func managedBy(fields []metav1.ManagedFieldsEntry) []string {
	var out []string
	for _, f := range fields {
		out = append(out, f.Manager)
	}
	return out
}

func randomString(alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

// A workload prints secrets of many kinds and crashes. Sentinel fetches its
// logs and attaches them to the incident, and none of the secrets reaches the
// Ledger or the controller's own log. The secrets are made up at run time,
// so none is ever committed.
func TestLeakedSecretsNeverReachTheLedger(t *testing.T) {
	ctx := context.Background()
	const base62 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	secrets := map[string]string{
		"AWS access key":   "AKIA" + randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 16),
		"GitHub token":     "ghp_" + randomString(base62, 36),
		"password":         "Cn" + randomString(base62, 14),
		"bearer token":     randomString(base62, 32),
		"private key body": randomString(base62+"+/", 64),
		"card number":      "4111 1111 1111 1111",
		"email address":    "jane." + randomString("abcdefghijklmnopqrstuvwxyz", 8) + "@example.com",
	}
	lines := []string{
		"starting with credentials " + secrets["AWS access key"],
		"git clone https://" + secrets["GitHub token"] + "@github.com/shop/catalog.git",
		`{"level":"error","msg":"database login failed","password":"` + secrets["password"] + `"}`,
		"Authorization: Bearer " + secrets["bearer token"],
		"-----BEGIN RSA PRIVATE KEY-----", secrets["private key body"], "-----END RSA PRIVATE KEY-----",
		"charging card " + secrets["card number"],
		"notifying " + secrets["email address"],
		"fatal error: catalog config invalid",
	}
	script := "printf '%s\\n'"
	for _, l := range lines {
		script += " '" + l + "'"
	}
	script += "; exit 1"

	// A workload of its own, so no earlier incident on a shared one absorbs it.
	name := "leaky-" + randomString("abcdefghijklmnopqrstuvwxyz", 6)
	labels := map[string]string{"app": name}
	client := clientAs(t, "")
	started := time.Now()
	_, err := client.AppsV1().Deployments(smokeNS).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: smokeNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65534),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{
						Name: "app", Image: "registry.k8s.io/e2e-test-images/agnhost:2.61", Command: []string{"/bin/sh", "-c", script},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")},
						},
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					}},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.AppsV1().Deployments(smokeNS).Delete(ctx, name, metav1.DeleteOptions{}) })

	attached := waitForLogEntry(t, client, started, 4*time.Minute, "crash logs attached to an incident on "+name, func(e map[string]any) bool {
		summary, _ := e["summary"].(string)
		return e["msg"] == "evidence attached" && e["workload"] == "Deployment.apps/"+smokeNS+"/"+name && strings.HasPrefix(summary, "crash logs")
	})
	if summary, _ := attached["summary"].(string); !strings.Contains(summary, "catalog config invalid") {
		t.Errorf("the evidence does not carry the container's error: %q", summary)
	}
	export, err := exec.CommandContext(ctx, "kubectl", "--context", flags("").Context, "-n", sentinelNS, "exec", "deploy/sentinel",
		"--", "/sentinel", "ledger", "export", "--path", "/var/lib/sentinel/ledger.db").Output()
	if err != nil {
		t.Fatalf("exporting the Ledger: %v", err)
	}
	if !strings.Contains(string(export), `"redactions"`) {
		t.Error("the Ledger holds no redacted log evidence")
	}
	pods, err := client.CoreV1().Pods(sentinelNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=controller"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("controller pod not found: %v", err)
	}
	controllerLog, err := client.CoreV1().Pods(sentinelNS).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for kind, secret := range secrets {
		if strings.Contains(string(export), secret) {
			t.Errorf("the %s reached the Ledger", kind)
		}
		if strings.Contains(string(controllerLog), secret) {
			t.Errorf("the %s reached the controller log", kind)
		}
	}
}

func rolledOut(d *appsv1.Deployment) bool {
	want := ptr.Deref(d.Spec.Replicas, 1)
	return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want &&
		d.Status.AvailableReplicas == want && d.Status.Replicas == want
}

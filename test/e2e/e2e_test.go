//go:build e2e

// Package e2e tests Sentinel on a real cluster brought up by hack/e2e-up.sh.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/harness"
	"github.com/karimzakzouk/sentinel/internal/kube"
)

const (
	sentinelNS     = "sentinel"
	smokeNS        = "e2e-smoke"
	labNS          = "sentinel-lab-0"
	controllerUser = "system:serviceaccount:sentinel:sentinel"
	actorUser      = "system:serviceaccount:sentinel-actor:sentinel-actor"
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

func TestControllerRecordsACapabilityReport(t *testing.T) {
	ctx := context.Background()
	admin := clientAs(t, "")
	eventually(t, 3*time.Minute, "the controller to be available", func() (bool, error) {
		d, err := admin.AppsV1().Deployments(sentinelNS).Get(ctx, "sentinel", metav1.GetOptions{})
		return err == nil && d.Status.AvailableReplicas == 1, err
	})
	pods, err := admin.CoreV1().Pods(sentinelNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=controller"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("controller pod not found: %v", err)
	}
	var report map[string]any
	eventually(t, 2*time.Minute, "a capability report in the controller log", func() (bool, error) {
		raw, err := admin.CoreV1().Pods(sentinelNS).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "capability report recorded" {
				report = entry
				return true, nil
			}
		}
		return false, nil
	})
	if report["identity"] != controllerUser || report["platform"] != "kind" {
		t.Fatalf("report logged as %v on %v", report["identity"], report["platform"])
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
	want := map[string]bool{"detect.api": true, "act": true, "lab": false, "lab.user-namespaces": true, "detect.node-agent": true}
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
	case "calico":
		if got.Status != capability.Available {
			t.Fatalf("Calico enforces NetworkPolicy, but the probe says %s", got.Status)
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
	cases := []struct {
		name string
		do   func() error
		// fence means the write must be refused by a sentinel-fence policy, not merely by RBAC.
		allowed, fence bool
	}{
		{"actor scales within bounds", patch(actor, smokeNS, "deployment", "web", `{"spec":{"replicas":2}}`), true, false},
		{"actor restarts a Deployment", patch(actor, smokeNS, "deployment", "web", restart), true, false},
		{"actor scales a StatefulSet within bounds", patch(actor, smokeNS, "statefulset", "store", `{"spec":{"replicas":3}}`, "scale"), true, false},
		{"actor scales past the bound", patch(actor, smokeNS, "deployment", "web", `{"spec":{"replicas":9}}`, "scale"), false, true},
		{"actor changes the service account", patch(actor, smokeNS, "deployment", "web", `{"spec":{"template":{"spec":{"serviceAccountName":"default"}}}}`), false, true},
		{"actor changes the image", patch(actor, smokeNS, "deployment", "web", `{"spec":{"template":{"spec":{"containers":[{"name":"web","image":"busybox"}]}}}}`), false, true},
		{"actor adds an environment variable", patch(actor, smokeNS, "deployment", "web", `{"spec":{"template":{"spec":{"containers":[{"name":"web","env":[{"name":"X","value":"1"}]}]}}}}`), false, true},
		{"actor changes StatefulSet pod labels", patch(actor, smokeNS, "statefulset", "store", `{"spec":{"template":{"metadata":{"labels":{"x":"y"}}}}}`), false, true},
		{"actor acts outside its namespaces", patch(actor, "kube-system", "deployment", "coredns", `{"spec":{"replicas":2}}`), false, false},
		{"controller creates a real pod", createPod(false), false, true},
		{"controller makes a dry-run probe", createPod(true), true, false},
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

func TestScenarioInjectionRecordsGroundTruth(t *testing.T) {
	ctx := context.Background()
	cfg, err := flags("").RESTConfig("sentinel-e2e")
	if err != nil {
		t.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	in := &harness.Injector{
		Client:  client,
		Dynamic: dynamic.NewForConfigOrDie(cfg),
		Mapper:  restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(client.Discovery())),
	}
	s, err := harness.LoadScenario("../scenarios/smoke/bad-image.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run, err := in.Inject(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Revert(ctx, run) })
	if run.ManifestedAt == nil {
		t.Fatalf("fault did not manifest: %s", run.Observation)
	}
	if run.GroundTruth.FaultClass != "deploy.bad-image" || run.GroundTruth.RootCause.Name != "web" {
		t.Fatalf("ground truth = %+v", run.GroundTruth)
	}
	if err := in.Revert(ctx, run); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Minute, "web to recover after the revert", func() (bool, error) {
		d, err := client.AppsV1().Deployments(smokeNS).Get(ctx, "web", metav1.GetOptions{})
		return err == nil && rolledOut(d), err
	})
}

func rolledOut(d *appsv1.Deployment) bool {
	want := ptr.Deref(d.Spec.Replicas, 1)
	return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want &&
		d.Status.AvailableReplicas == want && d.Status.Replicas == want
}

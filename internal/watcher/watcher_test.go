package watcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// fakeK8sHelper is a stand-in for *k8s.Client that records calls and
// returns canned responses. Used to test handleItem without a real K8s
// API server.
type fakeK8sHelper struct {
	patchErr      error
	patchCalls    []patchCall
	annotations   map[string]string
	annotationErr error
	podLogs       string
	podLogErr     error
}

type patchCall struct {
	name      string
	namespace string
	phase     string
}

func (f *fakeK8sHelper) PatchCRStatus(ctx context.Context, name, namespace, phase string) error {
	f.patchCalls = append(f.patchCalls, patchCall{name, namespace, phase})
	return f.patchErr
}

func (f *fakeK8sHelper) LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error) {
	return f.annotations, f.annotationErr
}

func (f *fakeK8sHelper) FetchCrashingPodLogs(ctx context.Context, service, namespace string) (string, error) {
	return f.podLogs, f.podLogErr
}

// newFakeWatcher builds a watcher with a fake dynamic client preloaded
// with the given CR objects + a fake k8s helper.
func newFakeWatcher(t *testing.T, helper *fakeK8sHelper, handler Handler, objs ...runtime.Object) *Watcher {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		sentinelIncidentGVR: "SentinelIncidentList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	return newForTest(dyn, helper, handler)
}

// makeCR builds an unstructured SentinelIncident CR with the given spec.
func makeCR(name, namespace, service, svcNs, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "sentinel.io/v1alpha1",
			"kind":       "SentinelIncident",
			"metadata": map[string]any{
				"name":            name,
				"namespace":       namespace,
				"resourceVersion": "1",
			},
			"spec": map[string]any{
				"service":   service,
				"namespace": svcNs,
				"reason":    reason,
			},
		},
	}
}

// TestWatcherNilHandler verifies that the watcher can be constructed
// without panicking when the handler is nil (e.g. during init).
func TestWatcherNilHandler(t *testing.T) {
	w := &Watcher{
		handler: nil,
	}
	if w == nil {
		t.Error("expected non-nil Watcher")
	}
}

// TestWatcherNilClient verifies New rejects a nil k8s client.
func TestWatcherNilClient(t *testing.T) {
	_, err := New(nil, nil)
	if err == nil {
		t.Error("expected error when k8s client is nil")
	}
}

// TestWatcher_CRDTrigger verifies that a SentinelIncident CR with no
// status triggers the handler with a synthetic AnomalyCluster assembled
// from the CR spec + pod logs + workload annotations.
func TestWatcher_CRDTrigger(t *testing.T) {
	helper := &fakeK8sHelper{
		annotations: map[string]string{"sentinel.io/repo": "acme/payments"},
		podLogs:     "panic: nil pointer dereference\nmain.foo()",
	}

	var (
		mu      sync.Mutex
		got     schemas.AnomalyCluster
		gotName string
		gotNs   string
		called  = make(chan struct{}, 1)
	)
	handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
		mu.Lock()
		got = cluster
		gotName = crName
		gotNs = crNamespace
		mu.Unlock()
		called <- struct{}{}
	}

	cr := makeCR("payments-2026-06-29", "sentinel", "payments-api", "production", "manual trigger from on-call")
	w := newFakeWatcher(t, helper, handler, cr)

	w.handleItem(context.Background(), cr)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called within 2s")
	}

	mu.Lock()
	defer mu.Unlock()

	// The CR was patched to "investigating" so the same CR isn't re-triggered.
	if len(helper.patchCalls) != 1 {
		t.Fatalf("expected 1 PatchCRStatus call, got %d", len(helper.patchCalls))
	}
	if helper.patchCalls[0].phase != "investigating" {
		t.Errorf("patch phase = %q, want investigating", helper.patchCalls[0].phase)
	}
	if helper.patchCalls[0].name != "payments-2026-06-29" {
		t.Errorf("patch name = %q", helper.patchCalls[0].name)
	}

	// Handler got the right CR coordinates.
	if gotName != "payments-2026-06-29" || gotNs != "sentinel" {
		t.Errorf("handler called with name=%q ns=%q", gotName, gotNs)
	}

	// The cluster carries the service + namespace + reason from the CR spec.
	if got.Service != "payments-api" {
		t.Errorf("cluster.Service = %q, want payments-api", got.Service)
	}
	if got.Namespace != "production" {
		t.Errorf("cluster.Namespace = %q, want production", got.Namespace)
	}
	if got.Severity != schemas.SeverityWarning {
		t.Errorf("cluster.Severity = %q, want warning", got.Severity)
	}
	if len(got.Anomalies) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(got.Anomalies))
	}
	a := got.Anomalies[0]
	if a.Service != "payments-api" {
		t.Errorf("anomaly.Service = %q", a.Service)
	}
	if a.Detector != "crd_watcher" {
		t.Errorf("anomaly.Detector = %q, want crd_watcher", a.Detector)
	}
	// The anomaly explanation should contain the pod logs + the CR reason.
	if a.Explanation == "" {
		t.Error("anomaly.Explanation is empty")
	}
	if want := "manual trigger from on-call"; !contains(a.Explanation, want) {
		t.Errorf("anomaly.Explanation missing %q\nfull: %s", want, a.Explanation)
	}
	if want := "panic: nil pointer dereference"; !contains(a.Explanation, want) {
		t.Errorf("anomaly.Explanation missing pod log line %q\nfull: %s", want, a.Explanation)
	}
	// Workload annotations are attached.
	if got.Annotations["sentinel.io/repo"] != "acme/payments" {
		t.Errorf("cluster.Annotations = %v, want sentinel.io/repo=acme/payments", got.Annotations)
	}
	// Cluster + anomaly IDs are populated.
	if got.ClusterID == uuid.Nil {
		t.Error("cluster.ClusterID is nil")
	}
	if a.AnomalyID == uuid.Nil {
		t.Error("anomaly.AnomalyID is nil")
	}
}

// TestWatcher_CRDTriggerRepoFromSpec verifies a spec.repo overrides any
// workload annotation lookup — useful when the workload is not yet
// annotated but the operator knows the repo.
func TestWatcher_CRDTriggerRepoFromSpec(t *testing.T) {
	helper := &fakeK8sHelper{
		annotations: nil, // no workload annotations
	}
	var (
		mu     sync.Mutex
		got    schemas.AnomalyCluster
		called = make(chan struct{}, 1)
	)
	handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
		mu.Lock()
		got = cluster
		mu.Unlock()
		called <- struct{}{}
	}

	cr := makeCR("trigger-1", "sentinel", "svc", "default", "test")
	// Inject a spec.repo.
	spec, _, _ := unstructured.NestedMap(cr.Object, "spec")
	spec["repo"] = "acme/svc"
	_ = unstructured.SetNestedMap(cr.Object, spec, "spec")

	w := newFakeWatcher(t, helper, handler, cr)
	w.handleItem(context.Background(), cr)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called within 2s")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.Annotations["sentinel.io/repo"] != "acme/svc" {
		t.Errorf("expected spec.repo to be attached; got Annotations=%v", got.Annotations)
	}
}

// TestWatcher_CRDTriggerDefaultFields verifies that missing spec fields
// fall back to safe defaults (service="unknown", namespace=CR namespace,
// reason="triggered by CRD").
func TestWatcher_CRDTriggerDefaultFields(t *testing.T) {
	helper := &fakeK8sHelper{}
	var (
		mu     sync.Mutex
		got    schemas.AnomalyCluster
		called = make(chan struct{}, 1)
	)
	handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
		mu.Lock()
		got = cluster
		mu.Unlock()
		called <- struct{}{}
	}

	// Build a CR with an empty spec.
	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "sentinel.io/v1alpha1",
			"kind":       "SentinelIncident",
			"metadata": map[string]any{
				"name":      "empty-spec",
				"namespace": "sentinel",
			},
			"spec": map[string]any{},
		},
	}

	w := newFakeWatcher(t, helper, handler, cr)
	w.handleItem(context.Background(), cr)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called within 2s")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.Service != "unknown" {
		t.Errorf("default Service = %q, want unknown", got.Service)
	}
	if got.Namespace != "sentinel" {
		t.Errorf("default Namespace = %q, want sentinel (CR namespace)", got.Namespace)
	}
	if !contains(got.Anomalies[0].Explanation, "triggered by CRD") {
		t.Errorf("default reason should be 'triggered by CRD'; explanation: %s", got.Anomalies[0].Explanation)
	}
}

// TestWatcher_CRDSkipsItemsWithStatus verifies that CRs that already have a
// status.phase are not re-triggered by the initial list. This is the
// "Sentinel restart shouldn't re-fire pending CRs" guarantee.
func TestWatcher_CRDSkipsItemsWithStatus(t *testing.T) {
	// Build a CR that already has status.phase = "investigating".
	cr := makeCR("already-handled", "sentinel", "svc", "default", "test")
	_ = unstructured.SetNestedField(cr.Object, "investigating", "status", "phase")

	helper := &fakeK8sHelper{}
	called := make(chan struct{}, 1)
	handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
		called <- struct{}{}
	}

	w := newFakeWatcher(t, helper, handler, cr)

	// Simulate the watchStream() initial list pass.
	list, err := w.dynamic.Resource(sentinelIncidentGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range list.Items {
		item := &list.Items[i]
		status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if status != "" {
			continue
		}
		w.handleItem(context.Background(), item)
	}

	select {
	case <-called:
		t.Fatal("handler should NOT be called for a CR with status.phase set")
	case <-time.After(100 * time.Millisecond):
		// Good — handler was not called.
	}
}

// TestWatcher_NilHandlerDoesNotPanic verifies handleItem is a no-op when
// the handler is nil (e.g. during initialization).
func TestWatcher_NilHandlerDoesNotPanic(t *testing.T) {
	helper := &fakeK8sHelper{}
	w := newFakeWatcher(t, helper, nil, makeCR("x", "ns", "svc", "default", "r"))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleItem panicked with nil handler: %v", r)
		}
	}()
	w.handleItem(context.Background(), makeCR("x", "ns", "svc", "default", "r"))

	// PatchCRStatus should still have been called (status flip happens
	// before the handler nil-check).
	if len(helper.patchCalls) != 1 {
		t.Errorf("expected 1 patch call, got %d", len(helper.patchCalls))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

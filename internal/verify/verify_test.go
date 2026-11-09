package verify

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/model"
)

const incident model.ID = "01a1bdb4-f9ef-78b3-87ce-7008bde3abe4"

var front = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "front"}

func experimentIn(t *testing.T, ns, name string, phase v1alpha1.ExperimentPhase) *unstructured.Unstructured {
	t.Helper()
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&v1alpha1.Experiment{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Experiment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       v1alpha1.ExperimentSpec{IncidentID: string(incident), Purpose: "verify-fix", LabNamespace: ns},
		Status:     v1alpha1.ExperimentStatus{Phase: phase, Message: "the previous revision fixes it", Runs: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: u}
}

func newLab(t *testing.T, objects ...runtime.Object) (*Lab, func() []Verdict) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{ExperimentsResource: "ExperimentList"}, objects...)
	var mu sync.Mutex
	var verdicts []Verdict
	l := &Lab{Dynamic: dyn, Pool: []string{"lab-0", "lab-1"}, Watched: []string{"shop"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnVerdict: func(_ context.Context, v Verdict) {
			mu.Lock()
			verdicts = append(verdicts, v)
			mu.Unlock()
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return l, func() []Verdict {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(verdicts)
	}
}

// The Lab takes a namespace where no experiment runs, verifies the
// revision before the failing one, and is asked once per incident.
func TestTheLabIsAskedOncePerIncidentWhereItIsFree(t *testing.T) {
	ctx := context.Background()
	l, _ := newLab(t, experimentIn(t, "lab-0", "someone-elses", "Running"))
	exp, asked, err := l.Verify(ctx, incident, front, "front-7d9c")
	if err != nil || !asked || exp != "lab-1/"+ExperimentName(incident) {
		t.Fatalf("Verify = %q, %v, %v; want the free namespace lab-1", exp, asked, err)
	}
	got, err := l.Dynamic.Resource(ExperimentsResource).Namespace("lab-1").Get(ctx, ExperimentName(incident), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var e v1alpha1.Experiment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(got.Object, &e); err != nil {
		t.Fatal(err)
	}
	if e.Spec.Purpose != "verify-fix" || e.Spec.Revision != "front-7d9c" || e.Spec.LabNamespace != "lab-1" || e.Spec.Workload.Name != "front" {
		t.Fatalf("spec = %+v", e.Spec)
	}
	time.Sleep(100 * time.Millisecond)
	if again, asked, err := l.Verify(ctx, incident, front, "front-7d9c"); err != nil || !asked || again != exp {
		t.Fatalf("asking again = %q, %v, %v; want the same experiment", again, asked, err)
	}
	if list, _ := l.Dynamic.Resource(ExperimentsResource).Namespace("lab-0").List(ctx, metav1.ListOptions{}); len(list.Items) != 1 {
		t.Fatalf("asking again started another experiment: %d in lab-0", len(list.Items))
	}
}

func TestTheLabIsNotAskedWhenBusyOrOutsideWatchedNamespaces(t *testing.T) {
	ctx := context.Background()
	l, _ := newLab(t, experimentIn(t, "lab-0", "a", "Sealing"), experimentIn(t, "lab-1", "b", "Running"))
	if _, asked, err := l.Verify(ctx, incident, front, "front-7d9c"); err != nil || asked {
		t.Fatalf("asked=%v, %v; want the Lab busy", asked, err)
	}
	other := front
	other.Namespace = "kube-system"
	if _, asked, _ := newLabOnly(t).Verify(ctx, incident, other, "x"); asked {
		t.Fatal("the Lab was asked to copy from a namespace Sentinel does not watch")
	}
}

func newLabOnly(t *testing.T) *Lab {
	l, _ := newLab(t)
	return l
}

// A verdict is passed on once, and verdicts listed at start, which an
// earlier run passed on, are not replayed.
func TestVerdictsArePassedOnOnce(t *testing.T) {
	ctx := context.Background()
	l, verdicts := newLab(t, experimentIn(t, "lab-0", "old", "Passed"))
	exp := experimentIn(t, "lab-1", ExperimentName(incident), "Running")
	res := l.Dynamic.Resource(ExperimentsResource).Namespace("lab-1")
	if _, err := res.Create(ctx, exp, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := unstructured.SetNestedField(exp.Object, "Passed", "status", "phase"); err != nil {
			t.Fatal(err)
		}
		if _, err := res.Update(ctx, exp, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(verdicts()) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	got := verdicts()
	if len(got) != 1 || got[0].Phase != "Passed" || got[0].IncidentID != incident || got[0].Runs != 15 {
		t.Fatalf("verdicts = %+v; want one, for the incident", got)
	}
}

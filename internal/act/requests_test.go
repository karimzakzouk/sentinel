package act

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
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/model"
)

var web = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}

func unstructuredOf(t *testing.T, obj any) *unstructured.Unstructured {
	t.Helper()
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: u}
}

func newRequests(t *testing.T, objects ...runtime.Object) (*Requests, func() []Outcome) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{actor.PoliciesResource: "SentinelPolicyList", actor.RequestsResource: "ActionRequestList"},
		objects...)
	var mu sync.Mutex
	var outcomes []Outcome
	r := &Requests{Dynamic: dyn, Namespaces: []string{"shop"}, TTL: time.Minute, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnOutcome: func(_ context.Context, o Outcome) {
			mu.Lock()
			outcomes = append(outcomes, o)
			mu.Unlock()
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r, func() []Outcome {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(outcomes)
	}
}

func policyAt(t *testing.T, level v1alpha1.AutonomyLevel) *unstructured.Unstructured {
	return unstructuredOf(t, &v1alpha1.SentinelPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SentinelPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "shop"},
		Spec:       v1alpha1.SentinelPolicySpec{Autonomy: []v1alpha1.ActionAutonomy{{Action: actor.ActionRolloutUndo, Level: level}}},
	})
}

func rollback() model.Action {
	return model.Action{ID: model.NewID(), IncidentID: "01a1bdb4-f9ef-78b3-87ce-7008bde3abe4", Type: "rollout.undo", Target: web,
		Params: map[string]string{actor.ParamFromTemplateHash: "bbb"}}
}

func TestARequestNeedsAPolicyThatAllowsIt(t *testing.T) {
	ctx := context.Background()
	for level, want := range map[v1alpha1.AutonomyLevel]bool{v1alpha1.AutonomyRecommend: false, v1alpha1.AutonomyApprove: true,
		v1alpha1.AutonomyActUnlessVetoed: true, v1alpha1.AutonomyAutonomous: true} {
		r, _ := newRequests(t, policyAt(t, level))
		asked, err := r.Request(ctx, rollback(), "a bad image")
		if err != nil || asked != want {
			t.Fatalf("at %s: asked %v, %v; want %v", level, asked, err, want)
		}
		list, _ := r.Dynamic.Resource(actor.RequestsResource).Namespace("shop").List(ctx, metav1.ListOptions{})
		if want != (len(list.Items) == 1) {
			t.Fatalf("at %s: %d requests", level, len(list.Items))
		}
	}
	r, _ := newRequests(t)
	if asked, _ := r.Request(ctx, rollback(), "no policy"); asked {
		t.Fatal("asked without any policy")
	}
}

func TestAskingTwiceIsAskingOnce(t *testing.T) {
	ctx := context.Background()
	r, _ := newRequests(t, policyAt(t, v1alpha1.AutonomyAutonomous))
	a := rollback()
	for range 2 {
		if asked, err := r.Request(ctx, a, "a bad image"); !asked || err != nil {
			t.Fatalf("asked %v, %v", asked, err)
		}
	}
	list, _ := r.Dynamic.Resource(actor.RequestsResource).Namespace("shop").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("%d requests for one incident and action", len(list.Items))
	}
	var req v1alpha1.ActionRequest
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[0].Object, &req)
	if req.Spec.Action != actor.ActionRolloutUndo || req.Spec.Target.Name != "web" || req.Spec.Params[actor.ParamFromTemplateHash] != "bbb" ||
		req.Spec.IncidentID != string(a.IncidentID) || req.Spec.ExpiresAt.IsZero() {
		t.Fatalf("request = %+v", req.Spec)
	}
}

func TestOutcomesArePassedOnOnceAndNotReplayed(t *testing.T) {
	ctx := context.Background()
	old := &v1alpha1.ActionRequest{TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ActionRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: "earlier", Namespace: "shop"},
		Spec:       v1alpha1.ActionRequestSpec{IncidentID: "old", Action: actor.ActionScale},
		Status:     v1alpha1.ActionRequestStatus{Phase: v1alpha1.ActionSucceeded}}
	r, outcomes := newRequests(t, policyAt(t, v1alpha1.AutonomyAutonomous), unstructuredOf(t, old))
	a := rollback()
	if _, err := r.Request(ctx, a, "a bad image"); err != nil {
		t.Fatal(err)
	}
	res := r.Dynamic.Resource(actor.RequestsResource).Namespace("shop")
	name := requestName(a.IncidentID, actor.ActionRolloutUndo)
	for _, phase := range []v1alpha1.ActionPhase{v1alpha1.ActionExecuting, v1alpha1.ActionSucceeded, v1alpha1.ActionSucceeded} {
		u, err := res.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		u.Object["status"] = map[string]any{"phase": string(phase), "message": "rolled web back"}
		if _, err := res.UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	got := outcomes()
	if len(got) != 1 || got[0].IncidentID != a.IncidentID || got[0].Phase != v1alpha1.ActionSucceeded || got[0].Message != "rolled web back" {
		t.Fatalf("outcomes = %+v, want the new request's success once, and nothing for the earlier one", got)
	}
}

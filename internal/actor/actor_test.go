package actor

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
)

func template(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}},
	}
}

func replicaSet(name, hash, revision, image string) *appsv1.ReplicaSet {
	controller := true
	tmpl := template(image)
	tmpl.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = hash
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: hash},
			Annotations:     map[string]string{revisionAnnotation: revision},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep-uid", Controller: &controller}}},
		Spec: appsv1.ReplicaSetSpec{Template: tmpl},
	}
}

func sentinelPolicy(level v1alpha1.AutonomyLevel, actions ...string) *v1alpha1.SentinelPolicy {
	p := &v1alpha1.SentinelPolicy{TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SentinelPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "shop"}}
	for _, a := range actions {
		p.Spec.Autonomy = append(p.Spec.Autonomy, v1alpha1.ActionAutonomy{Action: a, Level: level})
	}
	return p
}

// newActor returns an actor over a shop namespace holding a web Deployment
// on revision 2 (a broken image) with revision 1 before it, and the
// namespace's policies. Dry-run writes are answered without being applied,
// as the API server does.
func newActor(t *testing.T, replicas int32, policies ...*v1alpha1.SentinelPolicy) (*Actor, *fake.Clientset, *int32) {
	t.Helper()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: types.UID("dep-uid")},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: template("shop/web:2-broken")}}
	client := fake.NewClientset(dep, replicaSet("web-aaa", "aaa", "1", "shop/web:1"), replicaSet("web-bbb", "bbb", "2", "shop/web:2-broken"))
	scale := replicas
	client.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if u, ok := action.(k8stesting.UpdateActionImpl); ok && len(u.UpdateOptions.DryRun) > 0 {
			return true, u.Object, nil
		}
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		switch a := action.(type) {
		case k8stesting.GetActionImpl:
			return true, &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: "shop"},
				Spec: autoscalingv1.ScaleSpec{Replicas: scale}}, nil
		case k8stesting.UpdateActionImpl:
			scale = a.Object.(*autoscalingv1.Scale).Spec.Replicas
			return true, a.Object, nil
		}
		return false, nil, nil
	})
	var objects []runtime.Object
	for _, p := range policies {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: u})
	}
	objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop-app", "namespace": "argocd"},
		"spec": map[string]any{"project": "default",
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}}},
	}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{PoliciesResource: "SentinelPolicyList", RequestsResource: "ActionRequestList",
			ApplicationsResource: "ApplicationList", ApplicationSetsResource: "ApplicationSetList", KustomizationsResource: "KustomizationList",
			HelmReleasesResource: "HelmReleaseList", ExperimentsResource: "ExperimentList"}, objects...)
	dyn.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if p, ok := action.(k8stesting.PatchActionImpl); ok && len(p.PatchOptions.DryRun) > 0 {
			return true, &unstructured.Unstructured{Object: map[string]any{}}, nil
		}
		return false, nil, nil
	})
	a := &Actor{Client: client, Dynamic: dyn, Namespaces: []string{"shop"}, PerHour: 5, ArgoCDNamespace: "argocd",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return t0 }}
	return a, client, &scale
}

func request(action string, params map[string]string) *v1alpha1.ActionRequest {
	return &v1alpha1.ActionRequest{ObjectMeta: metav1.ObjectMeta{Name: "req", Namespace: "shop"},
		Spec: v1alpha1.ActionRequestSpec{IncidentID: "inc-1", Action: action, Reason: "test", Params: params,
			ExpiresAt: metav1.NewTime(t0.Add(10 * time.Minute)),
			Target:    v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "shop", Name: "web"}}}
}

func TestARollbackRestoresThePreviousRevisionsTemplate(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo))
	phase, msg := a.Handle(ctx, request(ActionRolloutUndo, map[string]string{ParamFromTemplateHash: "bbb"}))
	if phase != v1alpha1.ActionSucceeded {
		t.Fatalf("%s: %s", phase, msg)
	}
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != "shop/web:1" {
		t.Fatalf("image after the rollback = %s, want revision 1's", img)
	}
	if _, ok := dep.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; ok {
		t.Fatal("the rollback copied the ReplicaSet's pod-template-hash label")
	}
	if !strings.Contains(msg, "revision 2 to revision 1") {
		t.Fatalf("message = %q", msg)
	}
}

func TestARollbackOfAWorkloadThatChangedFails(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo))
	phase, msg := a.Handle(ctx, request(ActionRolloutUndo, map[string]string{ParamFromTemplateHash: "aaa"}))
	if phase != v1alpha1.ActionFailed || !strings.Contains(msg, "changed since the decision") {
		t.Fatalf("%s: %s", phase, msg)
	}
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != "shop/web:2-broken" {
		t.Fatalf("the Deployment was changed: %s", img)
	}
}

func TestScaleRestoresReplicasOnlyFromWhatTheDecisionSaw(t *testing.T) {
	a, _, scale := newActor(t, 0, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionScale))
	if phase, msg := a.Handle(ctx, request(ActionScale, map[string]string{ParamReplicas: "1", ParamFromReplicas: "2"})); phase != v1alpha1.ActionFailed {
		t.Fatalf("scaling from a count the workload no longer has: %s: %s", phase, msg)
	}
	if phase, msg := a.Handle(ctx, request(ActionScale, map[string]string{ParamReplicas: "1", ParamFromReplicas: "0"})); phase != v1alpha1.ActionSucceeded {
		t.Fatalf("%s: %s", phase, msg)
	}
	if *scale != 1 {
		t.Fatalf("replicas = %d, want 1", *scale)
	}
}

func TestRequestsTheActorRefuses(t *testing.T) {
	undo := map[string]string{ParamFromTemplateHash: "bbb"}
	cases := map[string]struct {
		policies []*v1alpha1.SentinelPolicy
		prepare  func(*Actor, *v1alpha1.ActionRequest)
		want     v1alpha1.ActionPhase
		why      string
	}{
		"no policy": {nil, nil, v1alpha1.ActionRejected, "observe"},
		"policy needs a person": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyApprove, ActionRolloutUndo)}, nil,
			v1alpha1.ActionPending, "approve"},
		"policy only recommends": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyRecommend, ActionRolloutUndo)}, nil,
			v1alpha1.ActionRejected, "recommend"},
		"kill switch": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo)},
			func(a *Actor, _ *v1alpha1.ActionRequest) { a.Disabled = func() bool { return true } }, v1alpha1.ActionRejected, "kill switch"},
		"expired": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo)},
			func(_ *Actor, r *v1alpha1.ActionRequest) { r.Spec.ExpiresAt = metav1.NewTime(t0.Add(-time.Minute)) }, v1alpha1.ActionExpired, "lapsed"},
		"another namespace": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo)},
			func(_ *Actor, r *v1alpha1.ActionRequest) { r.Spec.Target.Namespace = "payments" }, v1alpha1.ActionRejected, "may not act"},
		"budget spent": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo)},
			func(a *Actor, _ *v1alpha1.ActionRequest) { a.PerHour = 0 }, v1alpha1.ActionRejected, "budget"},
		"dry run only": {[]*v1alpha1.SentinelPolicy{sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo)},
			func(_ *Actor, r *v1alpha1.ActionRequest) { r.Spec.DryRunOnly = true }, v1alpha1.ActionDryRunPassed, "dry run"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, client, _ := newActor(t, 3, tc.policies...)
			req := request(ActionRolloutUndo, undo)
			if tc.prepare != nil {
				tc.prepare(a, req)
			}
			phase, msg := a.Handle(ctx, req)
			if phase != tc.want || !strings.Contains(msg, tc.why) {
				t.Fatalf("%s: %s; want %s mentioning %q", phase, msg, tc.want, tc.why)
			}
			dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
			if img := dep.Spec.Template.Spec.Containers[0].Image; img != "shop/web:2-broken" {
				t.Fatalf("a refused request changed the Deployment: %s", img)
			}
		})
	}
}

func TestTheActorRunsARequestAndRecordsItsOutcome(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo))
	req := request(ActionRolloutUndo, map[string]string{ParamFromTemplateHash: "bbb"})
	req.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ActionRequest"}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Dynamic.Resource(RequestsResource).Namespace("shop").Create(ctx, &unstructured.Unstructured{Object: obj},
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- a.Run(run) }()
	var got v1alpha1.ActionRequest
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		u, err := a.Dynamic.Resource(RequestsResource).Namespace("shop").Get(ctx, "req", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.Phase.Done() {
			break
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.ActionSucceeded || len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Reason != "Succeeded" {
		t.Fatalf("status = %+v", got.Status)
	}
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != "shop/web:1" {
		t.Fatalf("image = %s, want the rollback", img)
	}
}

// Restoring replicas after a scale-up takes capacity away; the policy's cap
// bounds how much.
func TestScaleTakesAwayNoMoreCapacityThanThePolicyAllows(t *testing.T) {
	a, _, scale := newActor(t, 5, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionScale))
	phase, msg := a.Handle(ctx, request(ActionScale, map[string]string{ParamReplicas: "2", ParamFromReplicas: "5"}))
	if phase != v1alpha1.ActionFailed || !strings.Contains(msg, "60% of its capacity") || *scale != 5 {
		t.Fatalf("%s: %s; replicas %d", phase, msg, *scale)
	}
	if phase, msg := a.Handle(ctx, request(ActionScale, map[string]string{ParamReplicas: "4", ParamFromReplicas: "5"})); phase != v1alpha1.ActionSucceeded {
		t.Fatalf("taking away one of five: %s: %s", phase, msg)
	}
}

func TestPeopleApproveAndVeto(t *testing.T) {
	undo := map[string]string{ParamFromTemplateHash: "bbb"}
	annotated := func(key, who string) *v1alpha1.ActionRequest {
		r := request(ActionRolloutUndo, undo)
		r.Annotations = map[string]string{key: who}
		r.CreationTimestamp = metav1.NewTime(t0.Add(-time.Minute))
		return r
	}
	cases := map[string]struct {
		level v1alpha1.AutonomyLevel
		req   *v1alpha1.ActionRequest
		want  v1alpha1.ActionPhase
		why   string
	}{
		"approved":                 {v1alpha1.AutonomyApprove, annotated(AnnotationApprovedBy, "alice"), v1alpha1.ActionSucceeded, "revision 2 to revision 1"},
		"approved by Sentinel":     {v1alpha1.AutonomyApprove, annotated(AnnotationApprovedBy, "system:serviceaccount:sentinel:sentinel"), v1alpha1.ActionRejected, "its own"},
		"vetoed":                   {v1alpha1.AutonomyAutonomous, annotated(AnnotationVetoedBy, "bob"), v1alpha1.ActionRejected, "vetoed by bob"},
		"within the veto window":   {v1alpha1.AutonomyActUnlessVetoed, annotated("other", "x"), v1alpha1.ActionPending, "unless someone vetoes"},
		"vetoed within the window": {v1alpha1.AutonomyActUnlessVetoed, annotated(AnnotationVetoedBy, "bob"), v1alpha1.ActionRejected, "vetoed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, _, _ := newActor(t, 3, sentinelPolicy(tc.level, ActionRolloutUndo))
			a.Self = []string{"system:serviceaccount:sentinel:sentinel", "system:serviceaccount:sentinel-actor:sentinel-actor"}
			phase, msg := a.Handle(ctx, tc.req)
			if phase != tc.want || !strings.Contains(msg, tc.why) {
				t.Fatalf("%s: %s; want %s mentioning %q", phase, msg, tc.want, tc.why)
			}
			if name == "approved" && tc.req.Status.ApprovedBy != "alice" {
				t.Fatalf("approvedBy = %q", tc.req.Status.ApprovedBy)
			}
		})
	}
	a, _, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyActUnlessVetoed, ActionRolloutUndo))
	late := annotated("other", "x")
	late.CreationTimestamp = metav1.NewTime(t0.Add(-VetoWindow - time.Second))
	if phase, msg := a.Handle(ctx, late); phase != v1alpha1.ActionSucceeded {
		t.Fatalf("after the veto window: %s: %s", phase, msg)
	}
}

// A workload Argo CD or Flux keeps in sync would have a plain rollback undone
// at the next sync; the actor leaves it to people.
func TestReconciledWorkloadsAreLeftToPeople(t *testing.T) {
	for owner, meta := range map[string]metav1.ObjectMeta{
		"Argo CD": {Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:shop/web"}},
		"Flux":    {Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop"}},
	} {
		a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo, ActionScale))
		dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
		dep.Labels, dep.Annotations = meta.Labels, meta.Annotations
		if _, err := client.AppsV1().Deployments("shop").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		for _, req := range []*v1alpha1.ActionRequest{
			request(ActionRolloutUndo, map[string]string{ParamFromTemplateHash: "bbb"}),
			request(ActionScale, map[string]string{ParamReplicas: "4", ParamFromReplicas: "3"}),
		} {
			if phase, msg := a.Handle(ctx, req); phase != v1alpha1.ActionFailed || !strings.Contains(msg, "kept in sync by "+owner) {
				t.Fatalf("%s on a workload %s keeps in sync: %s: %s", req.Spec.Action, owner, phase, msg)
			}
		}
	}
}

// Through Argo CD, a rollback first pauses the Application's automated sync,
// keeping what it was, so the rollback holds.
func TestAnArgoCDRollbackPausesTheApplicationFirst(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionArgoCDRollback))
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	dep.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "shop-app:apps/Deployment:shop/web"}
	if _, err := client.AppsV1().Deployments("shop").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	phase, msg := a.Handle(ctx, request(ActionArgoCDRollback, map[string]string{ParamFromTemplateHash: "bbb"}))
	if phase != v1alpha1.ActionSucceeded || !strings.Contains(msg, "paused Argo CD Application argocd/shop-app") ||
		!strings.Contains(msg, "revision 2 to revision 1") {
		t.Fatalf("%s: %s", phase, msg)
	}
	app, err := a.Dynamic.Resource(ApplicationsResource).Namespace("argocd").Get(ctx, "shop-app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructuredMap(app.Object, "spec", "syncPolicy", "automated"); found {
		t.Fatal("the Application still syncs on its own")
	}
	if was := app.GetAnnotations()[AnnotationPausedSync]; !strings.Contains(was, `"selfHeal":true`) {
		t.Fatalf("paused-sync annotation = %q, want the automated policy it had", was)
	}
	got, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "shop/web:1" {
		t.Fatalf("image = %s, want the rollback", img)
	}

	plain, _, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionArgoCDRollback))
	if phase, msg := plain.Handle(ctx, request(ActionArgoCDRollback, map[string]string{ParamFromTemplateHash: "bbb"})); phase != v1alpha1.ActionFailed ||
		!strings.Contains(msg, "not kept in sync by Argo CD") {
		t.Fatalf("on a plain workload: %s: %s", phase, msg)
	}
}

// An ApplicationSet rewrites the Applications it generates; unless it is
// told to leave their sync policy alone, it would undo the pause.
func TestAnApplicationSetMustLeaveThePauseAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		ignore []any
		want   v1alpha1.ActionPhase
	}{
		"rewrites the sync policy": {nil, v1alpha1.ActionFailed},
		"ignores the sync policy":  {[]any{map[string]any{"jsonPointers": []any{"/spec/syncPolicy"}}}, v1alpha1.ActionSucceeded},
	} {
		t.Run(name, func(t *testing.T) {
			a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionArgoCDRollback))
			dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
			dep.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "shop-app:apps/Deployment:shop/web"}
			if _, err := client.AppsV1().Deployments("shop").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			apps := a.Dynamic.Resource(ApplicationsResource).Namespace("argocd")
			app, _ := apps.Get(ctx, "shop-app", metav1.GetOptions{})
			app.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet", Name: "shops", UID: "set"}})
			if _, err := apps.Update(ctx, app, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			set := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
				"metadata": map[string]any{"name": "shops", "namespace": "argocd"}, "spec": map[string]any{}}}
			if tc.ignore != nil {
				set.Object["spec"] = map[string]any{"ignoreApplicationDifferences": tc.ignore}
			}
			if _, err := a.Dynamic.Resource(ApplicationSetsResource).Namespace("argocd").Create(ctx, set, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			phase, msg := a.Handle(ctx, request(ActionArgoCDRollback, map[string]string{ParamFromTemplateHash: "bbb"}))
			if phase != tc.want {
				t.Fatalf("%s: %s", phase, msg)
			}
		})
	}
}

// fluxShop labels web as applied by the Flux Kustomization flux-system/shop
// and creates that Kustomization, its spec.suspend set by setBy if any.
func fluxShop(t *testing.T, a *Actor, client *fake.Clientset, setBy string) {
	t.Helper()
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	dep.Labels = map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	if _, err := client.AppsV1().Deployments("shop").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	k := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "shop", "namespace": "flux-system"},
		"spec":     map[string]any{"path": "./shop", "prune": true}}}
	if setBy != "" {
		k.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: setBy, Operation: metav1.ManagedFieldsOperationApply,
			FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:spec":{"f:path":{},"f:suspend":{}}}`)}})
	}
	if _, err := a.Dynamic.Resource(KustomizationsResource).Namespace("flux-system").Create(ctx, k, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestAFluxRollbackSuspendsTheKustomizationFirst(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionFluxRollback))
	fluxShop(t, a, client, "")
	phase, msg := a.Handle(ctx, request(ActionFluxRollback, map[string]string{ParamFromTemplateHash: "bbb"}))
	if phase != v1alpha1.ActionSucceeded || !strings.Contains(msg, "suspended the Flux Kustomization flux-system/shop") ||
		!strings.Contains(msg, "sentinel resume flux flux-system/shop") {
		t.Fatalf("%s: %s", phase, msg)
	}
	k, _ := a.Dynamic.Resource(KustomizationsResource).Namespace("flux-system").Get(ctx, "shop", metav1.GetOptions{})
	if suspended, _, _ := unstructuredBool(k.Object, "spec", "suspend"); !suspended || k.GetAnnotations()[AnnotationPausedSync] == "" {
		t.Fatalf("Kustomization = %v", k.Object)
	}
	got, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "shop/web:1" {
		t.Fatalf("image = %s, want the rollback", img)
	}
}

// A parent Kustomization that sets the child's spec.suspend would resume
// it; the actor leaves that to people.
func TestAParentThatSetsSuspendIsLeftToPeople(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionFluxRollback))
	fluxShop(t, a, client, "kustomize-controller")
	if phase, msg := a.Handle(ctx, request(ActionFluxRollback, map[string]string{ParamFromTemplateHash: "bbb"})); phase != v1alpha1.ActionFailed ||
		!strings.Contains(msg, "kustomize-controller sets") {
		t.Fatalf("%s: %s", phase, msg)
	}
}

func TestRollbackActionFollowsTheOwner(t *testing.T) {
	for want, meta := range map[string]metav1.ObjectMeta{
		ActionRolloutUndo:    {},
		ActionArgoCDRollback: {Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "app:apps/Deployment:shop/web"}},
		ActionFluxRollback: {Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop",
			"kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
	} {
		if got := RollbackAction(meta); got != want {
			t.Errorf("RollbackAction(%+v) = %s, want %s", meta, got, want)
		}
	}
	helm := metav1.ObjectMeta{Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "shop", "helm.toolkit.fluxcd.io/namespace": "flux-system"}}
	if got := RollbackAction(helm); got != ActionFluxRollback {
		t.Errorf("a HelmRelease's workload: %s, want %s", got, ActionFluxRollback)
	}
}

// helmShop labels web as rendered by the Flux HelmRelease flux-system/shop
// and creates that HelmRelease, remediating failed upgrades when retries is
// not zero.
func helmShop(t *testing.T, a *Actor, client *fake.Clientset, retries int64) {
	t.Helper()
	dep, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	dep.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "shop", "helm.toolkit.fluxcd.io/namespace": "flux-system"}
	if _, err := client.AppsV1().Deployments("shop").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "web"}}, "values": map[string]any{"tag": "2-broken"}}
	if retries != 0 {
		spec["upgrade"] = map[string]any{"remediation": map[string]any{"retries": retries}}
	}
	hr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "shop", "namespace": "flux-system"}, "spec": spec}}
	if _, err := a.Dynamic.Resource(HelmReleasesResource).Namespace("flux-system").Create(ctx, hr, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Through a Flux HelmRelease, a rollback suspends the release first, unless
// helm-controller rolls failed upgrades back itself.
func TestAFluxRollbackSuspendsTheHelmReleaseFirst(t *testing.T) {
	a, client, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionFluxRollback))
	helmShop(t, a, client, 0)
	phase, msg := a.Handle(ctx, request(ActionFluxRollback, map[string]string{ParamFromTemplateHash: "bbb"}))
	if phase != v1alpha1.ActionSucceeded || !strings.Contains(msg, "suspended the Flux HelmRelease flux-system/shop") ||
		!strings.Contains(msg, "sentinel resume helmrelease flux-system/shop") {
		t.Fatalf("%s: %s", phase, msg)
	}
	hr, _ := a.Dynamic.Resource(HelmReleasesResource).Namespace("flux-system").Get(ctx, "shop", metav1.GetOptions{})
	if suspended, _, _ := unstructuredBool(hr.Object, "spec", "suspend"); !suspended || hr.GetAnnotations()[AnnotationPausedSync] == "" {
		t.Fatalf("HelmRelease = %v", hr.Object)
	}
	if got, _ := client.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{}); got.Spec.Template.Spec.Containers[0].Image != "shop/web:1" {
		t.Fatal("web was not rolled back")
	}

	a, client, _ = newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionFluxRollback))
	helmShop(t, a, client, 3)
	if phase, msg := a.Handle(ctx, request(ActionFluxRollback, map[string]string{ParamFromTemplateHash: "bbb"})); phase != v1alpha1.ActionFailed ||
		!strings.Contains(msg, "helm-controller rolls the HelmRelease flux-system/shop back itself") {
		t.Fatalf("with remediation: %s: %s", phase, msg)
	}
}

func TestArgoApplicationFromTrackingID(t *testing.T) {
	for id, want := range map[string][2]string{
		"shop-app:apps/Deployment:shop/web":        {"argocd", "shop-app"},
		"team-a_shop-app:apps/Deployment:shop/web": {"team-a", "shop-app"},
	} {
		ns, name, ok := ArgoApplication(metav1.ObjectMeta{Annotations: map[string]string{"argocd.argoproj.io/tracking-id": id}}, "argocd")
		if !ok || ns != want[0] || name != want[1] {
			t.Errorf("%q: %s/%s, want %s/%s", id, ns, name, want[0], want[1])
		}
	}
}

// The strictest policy in a namespace wins.
func TestTheStrictestPolicyWins(t *testing.T) {
	a, _, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo), func() *v1alpha1.SentinelPolicy {
		p := sentinelPolicy(v1alpha1.AutonomyRecommend, ActionRolloutUndo)
		p.Name = "stricter"
		return p
	}())
	if phase, msg := a.Handle(ctx, request(ActionRolloutUndo, map[string]string{ParamFromTemplateHash: "bbb"})); phase != v1alpha1.ActionRejected {
		t.Fatalf("%s: %s", phase, msg)
	}
}

// At lab-verified the Lab stands in for a person, but only with a passing
// verdict on exactly this rollback; anything less waits for a person.
func TestTheLabMayStandInForAnApprover(t *testing.T) {
	undo := map[string]string{ParamFromTemplateHash: "bbb"}
	verdict := func(ns, incident, revision string, phase v1alpha1.ExperimentPhase) *unstructured.Unstructured {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&v1alpha1.Experiment{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Experiment"},
			ObjectMeta: metav1.ObjectMeta{Name: "verify", Namespace: ns},
			Spec: v1alpha1.ExperimentSpec{IncidentID: incident, Purpose: "verify-fix", LabNamespace: ns, Revision: revision,
				Workload: &v1alpha1.TargetRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "shop", Name: "web"}},
			Status: v1alpha1.ExperimentStatus{Phase: phase, Message: "the Lab's message"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &unstructured.Unstructured{Object: u}
	}
	cases := map[string]struct {
		exp        *unstructured.Unstructured
		ref, who   string
		want       v1alpha1.ActionPhase
		why, actor string
	}{
		"verified":                  {verdict("lab-0", "inc-1", "web-bbb", "Passed"), "lab-0/verify", "", v1alpha1.ActionSucceeded, "revision 2 to revision 1", "the Lab, lab-0/verify"},
		"still verifying":           {verdict("lab-0", "inc-1", "web-bbb", "Running"), "lab-0/verify", "", v1alpha1.ActionPending, "the Lab is still verifying it", ""},
		"not verified":              {verdict("lab-0", "inc-1", "web-bbb", "NotSure"), "lab-0/verify", "", v1alpha1.ActionPending, "the Lab did not verify it (NotSure", ""},
		"not verified but approved": {verdict("lab-0", "inc-1", "web-bbb", "Failed"), "lab-0/verify", "alice", v1alpha1.ActionSucceeded, "revision 2 to revision 1", "alice"},
		"another revision":          {verdict("lab-0", "inc-1", "web-aaa", "Passed"), "lab-0/verify", "", v1alpha1.ActionPending, "another revision", ""},
		"another incident":          {verdict("lab-0", "inc-2", "web-bbb", "Passed"), "lab-0/verify", "", v1alpha1.ActionPending, "about something else", ""},
		"outside the Lab":           {verdict("shop", "inc-1", "web-bbb", "Passed"), "shop/verify", "", v1alpha1.ActionPending, "no Experiment of the Lab's", ""},
		"never asked":               {verdict("lab-0", "inc-1", "web-bbb", "Passed"), "", "", v1alpha1.ActionPending, "the Lab was not asked", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, _, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyLabVerified, ActionRolloutUndo))
			a.LabNamespaces = []string{"lab-0"}
			if _, err := a.Dynamic.Resource(ExperimentsResource).Namespace(tc.exp.GetNamespace()).Create(ctx, tc.exp, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			req := request(ActionRolloutUndo, undo)
			req.Annotations = map[string]string{AnnotationExperiment: tc.ref}
			if tc.who != "" {
				req.Annotations[AnnotationApprovedBy] = tc.who
			}
			phase, msg := a.Handle(ctx, req)
			if phase != tc.want || !strings.Contains(msg, tc.why) || req.Status.ApprovedBy != tc.actor {
				t.Fatalf("%s: %s (approved by %q); want %s mentioning %q, approved by %q", phase, msg, req.Status.ApprovedBy, tc.want, tc.why, tc.actor)
			}
		})
	}
}

// A request the controller marks as needing approval, because its track
// record has not earned the policy's level, waits for a person; the mark
// never loosens a stricter policy.
func TestARequestHeldForItsTrackRecordWaitsForAPerson(t *testing.T) {
	undo := map[string]string{ParamFromTemplateHash: "bbb"}
	held := func(who string) *v1alpha1.ActionRequest {
		r := request(ActionRolloutUndo, undo)
		r.Annotations = map[string]string{AnnotationNeedsApproval: "its track record has not earned autonomous yet"}
		if who != "" {
			r.Annotations[AnnotationApprovedBy] = who
		}
		return r
	}
	a, _, _ := newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyAutonomous, ActionRolloutUndo))
	if phase, msg := a.Handle(ctx, held("")); phase != v1alpha1.ActionPending || !strings.Contains(msg, "has not earned autonomous yet") {
		t.Fatalf("%s: %s; want it waiting for a person, saying why", phase, msg)
	}
	if phase, msg := a.Handle(ctx, held("alice")); phase != v1alpha1.ActionSucceeded {
		t.Fatalf("approved: %s: %s", phase, msg)
	}
	a, _, _ = newActor(t, 3, sentinelPolicy(v1alpha1.AutonomyRecommend, ActionRolloutUndo))
	if phase, msg := a.Handle(ctx, held("alice")); phase != v1alpha1.ActionRejected {
		t.Fatalf("under recommend: %s: %s; the mark may not loosen a stricter policy", phase, msg)
	}
}

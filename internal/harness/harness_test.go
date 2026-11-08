package harness

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRepositoryScenariosAreValid(t *testing.T) {
	paths, err := filepath.Glob("../../test/scenarios/*/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no scenario files found: %v", err)
	}
	ids := map[string]string{}
	for _, p := range paths {
		s, err := LoadScenario(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if prev, dup := ids[s.ID]; dup {
			t.Errorf("scenario ID %s is used by both %s and %s", s.ID, prev, p)
		}
		ids[s.ID] = p
	}
}

func TestValidateRejectsIncompleteScenarios(t *testing.T) {
	gt := GroundTruth{FaultClass: "x", Mitigation: "none"}
	target := Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "ns", Name: "web"}
	bad := map[string]Scenario{
		"no id":           {App: "a", Fault: Fault{Kind: FaultNone}, GroundTruth: gt},
		"no ground truth": {ID: "x", App: "a", Fault: Fault{Kind: FaultNone}},
		"patch no body":   {ID: "x", App: "a", Fault: Fault{Kind: FaultPatch, Target: target}, GroundTruth: gt},
		"patch bad type":  {ID: "x", App: "a", Fault: Fault{Kind: FaultPatch, Target: target, Patch: &Patch{Type: "yaml", Content: "{}"}}, GroundTruth: gt},
		"no selector":     {ID: "x", App: "a", Fault: Fault{Kind: FaultDeletePods, Target: target}, GroundTruth: gt},
		"control expects": {ID: "x", App: "a", Fault: Fault{Kind: FaultNone}, GroundTruth: gt, Manifest: Manifestation{Condition: ConditionCrashLoop, Namespace: "ns", Selector: "a=b"}},
		"unknown kind":    {ID: "x", App: "a", Fault: Fault{Kind: "chaos"}, GroundTruth: gt},
	}
	for name, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}
}

func TestMatchesConditions(t *testing.T) {
	waiting := func(reason string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "c", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
		}}}}
	}
	oom := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "c", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
	}}}}
	notReady := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
	backingOff := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "c", RestartCount: 1, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}}}}
	firstExit := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "c", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}}}}
	cases := []struct {
		c    Condition
		pod  *corev1.Pod
		want bool
	}{
		{ConditionImagePullError, waiting("ImagePullBackOff"), true},
		{ConditionImagePullError, waiting("CrashLoopBackOff"), false},
		{ConditionCrashLoop, waiting("CrashLoopBackOff"), true},
		{ConditionCrashLoop, backingOff, true},
		{ConditionCrashLoop, firstExit, false},
		{ConditionOOMKilled, oom, true},
		{ConditionOOMKilled, waiting("CrashLoopBackOff"), false},
		{ConditionNotReady, notReady, true},
		{ConditionNotReady, waiting("ImagePullBackOff"), false},
	}
	for _, tc := range cases {
		if got, _ := matches(tc.c, tc.pod); got != tc.want {
			t.Errorf("matches(%s) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func newDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop"},
		"spec":     map[string]any{"replicas": int64(1), "paused": false},
	}}
}

func TestInjectPatchThenRevertRestoresTheSpec(t *testing.T) {
	ctx := context.Background()
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	mapper.Add(gvk, meta.RESTScopeNamespace)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newDeployment())
	in := &Injector{Client: fake.NewClientset(), Dynamic: dyn, Mapper: mapper}

	s := &Scenario{ID: "pause-web", App: "shop",
		Fault: Fault{Kind: FaultPatch, Target: Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "shop", Name: "web"},
			Patch: &Patch{Type: "merge", Content: `{"spec":{"paused":true}}`}},
		GroundTruth: GroundTruth{FaultClass: "deploy.paused", Mitigation: "rollout.resume"},
	}
	run, err := in.Inject(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	res := dyn.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace("shop")
	got, _ := res.Get(ctx, "web", metav1.GetOptions{})
	if paused, _, _ := unstructured.NestedBool(got.Object, "spec", "paused"); !paused {
		t.Fatal("the patch was not applied")
	}
	if run.InjectedAt.IsZero() || run.GroundTruth.FaultClass != "deploy.paused" || len(run.Before) == 0 {
		t.Fatalf("run not recorded: %+v", run)
	}
	if err := in.Revert(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, _ = res.Get(ctx, "web", metav1.GetOptions{})
	if paused, _, _ := unstructured.NestedBool(got.Object, "spec", "paused"); paused || run.RevertedAt == nil {
		t.Fatal("Revert did not restore the saved spec")
	}
}

func TestInjectDeletePodsAndControl(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", Labels: map[string]string{"app": "web"}}}
	client := fake.NewClientset(pod)
	in := &Injector{Client: client, Now: func() time.Time { return time.Unix(100, 0) }}

	run, err := in.Inject(ctx, &Scenario{ID: "kill", App: "shop",
		Fault:       Fault{Kind: FaultDeletePods, Target: Target{Namespace: "shop"}, Selector: "app=web"},
		GroundTruth: GroundTruth{FaultClass: "infra.pod-kill", Mitigation: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.DeletedPods) != 1 {
		t.Fatalf("deleted %v", run.DeletedPods)
	}
	if left, _ := client.CoreV1().Pods("shop").List(ctx, metav1.ListOptions{}); len(left.Items) != 0 {
		t.Fatal("pod was not deleted")
	}

	control, err := in.Inject(ctx, &Scenario{ID: "control", App: "shop", Fault: Fault{Kind: FaultNone},
		GroundTruth: GroundTruth{FaultClass: "none", Mitigation: "none"}})
	if err != nil || control.InjectedAt.Unix() != 100 {
		t.Fatalf("control run = %+v, %v", control, err)
	}
}

package watcher

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

var (
	web = model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	t0  = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
)

func pod(statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: statuses},
	}
}

func TestTerminationFactsCountOnlyNewRestarts(t *testing.T) {
	oom := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
	old := pod(corev1.ContainerStatus{Name: "web", RestartCount: 2})
	cur := pod(corev1.ContainerStatus{Name: "web", RestartCount: 4, LastTerminationState: oom})
	facts := TerminationFacts(old, cur, web, "abc", t0)
	if len(facts) != 2 || facts[0].Kind != FactOOMKilled || facts[0].Revision != "abc" {
		t.Fatalf("facts = %+v", facts)
	}
	if again := TerminationFacts(cur, cur, web, "abc", t0); len(again) != 0 {
		t.Fatalf("a resync produced %d facts", len(again))
	}
	if first := TerminationFacts(nil, cur, web, "abc", t0); len(first) != 0 {
		t.Fatal("restarts before Sentinel saw the pod were counted")
	}
	plain := pod(corev1.ContainerStatus{Name: "web", RestartCount: 3, LastTerminationState: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}})
	if f := TerminationFacts(old, plain, web, "", t0); len(f) != 1 || f[0].Kind != FactRestarted {
		t.Fatalf("plain restart facts = %+v", f)
	}
}

func TestStateFacts(t *testing.T) {
	waiting := func(reason string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "web", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
	}
	cases := map[string]FactKind{
		"ImagePullBackOff":           FactImagePullError,
		"ErrImagePull":               FactImagePullError,
		"CrashLoopBackOff":           FactCrashLoop,
		"CreateContainerConfigError": FactConfigError,
	}
	for reason, want := range cases {
		facts := StateFacts(pod(waiting(reason)), web, "", t0)
		if len(facts) != 1 || facts[0].Kind != want {
			t.Errorf("%s: facts = %+v, want one %s", reason, facts, want)
		}
	}
	if f := StateFacts(pod(waiting("ContainerCreating")), web, "", t0); len(f) != 0 {
		t.Errorf("a starting container produced facts: %+v", f)
	}
	deleting := pod(waiting("CrashLoopBackOff"))
	deleting.DeletionTimestamp = &metav1.Time{Time: t0}
	if f := StateFacts(deleting, web, "", t0); len(f) != 0 {
		t.Error("a terminating pod produced facts")
	}
}

func TestNotReadyCountsOnlyAfterTheProbesGracePeriod(t *testing.T) {
	p := pod(corev1.ContainerStatus{Name: "web", State: corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.Time{Time: t0}}}})
	p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{InitialDelaySeconds: 5, PeriodSeconds: 2, FailureThreshold: 3}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	if f := StateFacts(p, web, "", t0.Add(10*time.Second)); len(f) != 0 {
		t.Fatalf("not ready within its 11s grace period, yet counted: %+v", f)
	}
	if f := StateFacts(p, web, "", t0.Add(12*time.Second)); len(f) != 1 || f[0].Kind != FactNotReady {
		t.Fatalf("not ready past its grace period: %+v", f)
	}
	p.Status.Conditions[0].Status = corev1.ConditionTrue
	if f := StateFacts(p, web, "", t0.Add(time.Hour)); len(f) != 0 {
		t.Fatal("a ready pod produced facts")
	}
}

func TestEventFactsCountOnlyNewOccurrences(t *testing.T) {
	ev := &corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-1"}, Type: corev1.EventTypeWarning, Reason: "Unhealthy", Count: 5}
	if f := EventFacts(3, ev, web, "", t0); len(f) != 2 || f[0].Kind != FactProbeFailed {
		t.Fatalf("facts = %+v", f)
	}
	ev.Reason = "Pulled"
	if f := EventFacts(0, ev, web, "", t0); len(f) != 0 {
		t.Fatal("an unrelated event produced facts")
	}
}

func TestTemplateFields(t *testing.T) {
	base := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "web", Image: "web:1",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
		}}},
	}
	changed := *base.DeepCopy()
	changed.Spec.Containers[0].Image = "web:2"
	changed.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("6Mi")
	changed.Annotations = map[string]string{restartAnnotationKey: "now"}
	got := TemplateFields(&base, &changed)
	want := []string{"image:web", "resources:web", "restart"}
	if len(got) != len(want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fields = %v, want %v", got, want)
		}
	}
	if f := TemplateFields(&base, base.DeepCopy()); len(f) != 0 {
		t.Fatalf("equal templates gave %v", f)
	}
}

func TestDeploymentChangesIncludeScaleAndActor(t *testing.T) {
	old := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))}}
	cur := old.DeepCopy()
	cur.Spec.Replicas = ptr.To(int32(3))
	cur.Annotations = map[string]string{annotationManager: "kubectl"}
	changes := DeploymentChanges(old, cur, t0)
	if len(changes) != 1 || changes[0].Kind != ChangeScale || changes[0].Actor != "kubectl" || changes[0].Fields[0] != "replicas:1->3" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestTransformHidesConfigMapValuesAndKeepsTheWriter(t *testing.T) {
	when := metav1.NewTime(t0)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop", ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "helm", Time: &when}, {Manager: "kubectl-edit", Time: &metav1.Time{Time: t0.Add(time.Minute)}},
		}},
		Data: map[string]string{"password": "hunter2", "mode": "fast"},
	}
	out, err := Transform(cm)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*corev1.ConfigMap)
	if got.Data != nil || got.ManagedFields != nil {
		t.Fatal("values or managedFields survived the transform")
	}
	if got.Annotations[annotationManager] != "kubectl-edit" {
		t.Fatalf("last writer = %q, want kubectl-edit", got.Annotations[annotationManager])
	}
	next := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"}, Data: map[string]string{"password": "hunter3", "mode": "fast"}}
	_, _ = Transform(next)
	changes := ConfigMapChanges(got, next, t0)
	if len(changes) != 1 || len(changes[0].Fields) != 1 || changes[0].Fields[0] != "key:password" {
		t.Fatalf("changes = %+v", changes)
	}
}

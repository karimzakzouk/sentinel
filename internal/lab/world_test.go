package lab

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func owned(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: ptr.To(true)}}
}

func image(img string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: img}}}}
}

// The Lab copies a failing revision, not whatever the workload runs now,
// and only a revision that belongs to it.
func TestTheWorldHoldsTheFailingRevision(t *testing.T) {
	failing := image("web:bad")
	failing.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = "abc"
	stsRev := []byte(`{"spec":{"template":{"metadata":{"labels":{"app":"db"}},"spec":{"containers":[{"name":"db","image":"db:bad"}]}},"$patch":"replace"}}`)
	client := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: image("web:good")}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop", OwnerReferences: owned("Deployment", "web")},
			Spec: appsv1.ReplicaSetSpec{Template: failing}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "other-xyz", Namespace: "shop", OwnerReferences: owned("Deployment", "other")}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: appsv1.StatefulSetSpec{Template: image("db:good")}},
		&appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "db-1", Namespace: "shop", OwnerReferences: owned("StatefulSet", "db")},
			Data: runtime.RawExtension{Raw: stsRev}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-abc-1", Namespace: "shop"}, Spec: image("web:bad").Spec,
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "web", ImageID: "web@sha256:feed"}}}},
		// kind loads images under docker.io/library/import-…; that ImageID
		// must not pin the named registry image to another repository.
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-kind", Namespace: "shop"},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "10.96.200.200:5000/pyshop:bad"}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "web",
				ImageID: "docker-pullable://docker.io/library/import-2026-11-10@sha256:kindfeed"}}}},
	)
	web := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	w, err := ReadWorld(ctx, client, "shop", web, "web-abc")
	if err != nil {
		t.Fatal(err)
	}
	got := w.Workloads[web].Template
	if got.Spec.Containers[0].Image != "web:bad" || got.Labels[appsv1.DefaultDeploymentUniqueLabelKey] != "" {
		t.Fatalf("copied %s with labels %v; want the failing revision without its hash", got.Spec.Containers[0].Image, got.Labels)
	}
	if w.Digests["web:bad"] != "web@sha256:feed" {
		t.Fatalf("digests %v", w.Digests)
	}
	if _, ok := w.Digests["10.96.200.200:5000/pyshop:bad"]; ok {
		t.Fatalf("kind import ImageID pinned the named image: %v", w.Digests)
	}
	db := model.ObjectRef{Group: "apps", Kind: "StatefulSet", Namespace: "shop", Name: "db"}
	w, err = ReadWorld(ctx, client, "shop", db, "db-1")
	if err != nil {
		t.Fatal(err)
	}
	if img := w.Workloads[db].Template.Spec.Containers[0].Image; img != "db:bad" {
		t.Fatalf("copied %s; want the StatefulSet's failing revision", img)
	}
	if _, err := ReadWorld(ctx, client, "shop", web, "other-xyz"); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("got %v; want a revision of another workload refused", err)
	}
}

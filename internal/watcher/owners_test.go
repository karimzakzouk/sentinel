package watcher

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// A pod of a ReplicaSet owned by an Argo Rollouts Rollout is attributed to
// the Rollout, so the Brain can abort progressive delivery rather than
// treating the ReplicaSet as the workload.
func TestResolveAttributesRolloutOwnedPods(t *testing.T) {
	controller := true
	rsIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := rsIdx.Add(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-bbb", Namespace: "shop",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "web", UID: "ro-uid", Controller: &controller}}}}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-bbb-pod", Namespace: "shop",
		Labels:          map[string]string{"pod-template-hash": "bbb"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-bbb", UID: "rs-uid", Controller: &controller}}}}
	owners := Owners{ReplicaSets: appslisters.NewReplicaSetLister(rsIdx)}
	got, rev := owners.Resolve(pod)
	want := model.ObjectRef{Group: "argoproj.io", Kind: "Rollout", Namespace: "shop", Name: "web"}
	if got != want || rev != "bbb" {
		t.Fatalf("Resolve = %v %q; want %v %q", got, rev, want, "bbb")
	}
}

// A pod of a Deployment owned by a Flagger Canary is attributed to the
// Canary, so the Brain can abort progressive delivery rather than fighting
// the primary Deployment.
func TestResolveAttributesFlaggerCanaryOwnedPods(t *testing.T) {
	controller := true
	rsIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	depIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := rsIdx.Add(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-primary-bbb", Namespace: "shop",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web-primary", UID: "dep-uid", Controller: &controller}}}}); err != nil {
		t.Fatal(err)
	}
	if err := depIdx.Add(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web-primary", Namespace: "shop",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "flagger.app/v1beta1", Kind: "Canary", Name: "web", UID: "canary-uid", Controller: &controller}}}}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-primary-bbb-pod", Namespace: "shop",
		Labels:          map[string]string{"pod-template-hash": "bbb"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-primary-bbb", UID: "rs-uid", Controller: &controller}}}}
	owners := Owners{ReplicaSets: appslisters.NewReplicaSetLister(rsIdx), Deployments: appslisters.NewDeploymentLister(depIdx)}
	got, rev := owners.Resolve(pod)
	want := model.ObjectRef{Group: "flagger.app", Kind: "Canary", Namespace: "shop", Name: "web"}
	if got != want || rev != "bbb" {
		t.Fatalf("Resolve = %v %q; want %v %q", got, rev, want, "bbb")
	}
}

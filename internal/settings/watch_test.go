package settings

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSyncWatchLabelsAndBindings(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-config", Namespace: "sentinel"},
			Data: map[string]string{"config.yaml": "namespaces: []\nledger:\n  path: /l.db\n"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-actor-config", Namespace: "sentinel-actor"},
			Data: map[string]string{"config.yaml": "namespaces: []\nenabled: true\n"}},
	)
	targs := DefaultTargets("sentinel")
	if err := SyncWatch(ctx, client, targs, "sentinel", []string{"shop", "payments"}, true); err != nil {
		t.Fatal(err)
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, "shop", metav1.GetOptions{})
	if err != nil || ns.Labels[WatchedLabel] != "true" {
		t.Fatalf("shop label: %+v %v", ns.Labels, err)
	}
	if _, err := client.RbacV1().RoleBindings("shop").Get(ctx, "sentinel-watch", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RbacV1().RoleBindings("shop").Get(ctx, "sentinel-act", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := SyncWatch(ctx, client, targs, "sentinel", []string{"shop"}, true); err != nil {
		t.Fatal(err)
	}
	ns, _ = client.CoreV1().Namespaces().Get(ctx, "payments", metav1.GetOptions{})
	if ns.Labels[WatchedLabel] == "true" {
		t.Fatal("payments should be unlabeled")
	}
	if err := SyncWatch(ctx, client, targs, "sentinel", []string{"kube-system"}, false); err == nil {
		t.Fatal("expected refuse system namespace")
	}
}

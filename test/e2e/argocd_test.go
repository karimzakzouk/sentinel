//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const gitopsNS = "e2e-gitops"

var applications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// A bad image delivered through Argo CD, with automated sync and self-heal:
// a plain rollback would be undone at the next sync, so Sentinel pauses the
// Application's automated sync, rolls the Deployment back and verifies it,
// and Argo CD reports the Application out of sync instead of reverting.
// hack/e2e-up.sh installs Argo CD when E2E_ARGOCD is set.
func TestArgoCDRollbackPausesTheApplication(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1"); err != nil {
		t.Skip("Argo CD is not installed: run hack/e2e-up.sh with E2E_ARGOCD=1")
	}
	admin := dynamicAs(t, "")
	apps := admin.Resource(applications).Namespace("argocd")
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "e2e-guestbook", "namespace": "argocd"},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{"repoURL": "https://github.com/argoproj/argocd-example-apps", "targetRevision": "master",
				"path": "kustomize-guestbook"},
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": gitopsNS},
			"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
		},
	}}
	if _, err := apps.Create(ctx, app, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = apps.Delete(ctx, "e2e-guestbook", metav1.DeleteOptions{})
	})
	healthy := func(want string) func() (bool, error) {
		return func() (bool, error) {
			u, err := apps.Get(ctx, "e2e-guestbook", metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			sync, _, _ := unstructured.NestedString(u.Object, "status", "sync", "status")
			health, _, _ := unstructured.NestedString(u.Object, "status", "health", "status")
			return sync+"/"+health == want, nil
		}
	}
	eventually(t, 5*time.Minute, "the guestbook synced and healthy", healthy("Synced/Healthy"))
	before, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "kustomize-guestbook-ui", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image := before.Spec.Template.Spec.Containers[0].Image

	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-argocd", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "argocd.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-argocd", metav1.DeleteOptions{})
	})

	injected := time.Now()
	bad := `{"spec":{"source":{"kustomize":{"images":["gcr.io/google-samples/gb-frontend:does-not-exist"]}}}}`
	if _, err := apps.Patch(ctx, "e2e-guestbook", types.MergePatchType, []byte(bad), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLogEntry(t, client, injected, 3*time.Minute, "a request to roll back through Argo CD", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "argocd.rollback"
	})
	eventually(t, 3*time.Minute, "the Application paused and the guestbook back on its image", func() (bool, error) {
		u, err := apps.Get(ctx, "e2e-guestbook", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		_, automated, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated")
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "kustomize-guestbook-ui", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return !automated && u.GetAnnotations()["sentinel.karimzakzouk.github.io/paused-sync"] != "" &&
			d.Spec.Template.Spec.Containers[0].Image == image && d.Status.AvailableReplicas == *d.Spec.Replicas, nil
	})
	eventually(t, time.Minute, "Argo CD reporting the drift instead of undoing it", healthy("OutOfSync/Healthy"))
	verified := waitForLogEntry(t, client, injected, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
		return e["msg"] == "action verified" || e["msg"] == "action unverified"
	})
	if verified["msg"] != "action verified" {
		t.Fatalf("the rollback was not verified: %v", verified)
	}
}

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

var (
	gitRepositories = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	kustomizations  = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
)

// A bad image delivered through a Flux Kustomization: a plain rollback
// would be re-applied at the next reconcile, so Sentinel suspends the
// Kustomization, rolls the Deployment back and verifies it. hack/e2e-up.sh
// installs Flux when E2E_FLUX is set.
func TestFluxRollbackSuspendsTheKustomization(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("kustomize.toolkit.fluxcd.io/v1"); err != nil {
		t.Skip("Flux is not installed: run hack/e2e-up.sh with E2E_FLUX=1")
	}
	admin := dynamicAs(t, "")
	repos, ks := admin.Resource(gitRepositories).Namespace("flux-system"), admin.Resource(kustomizations).Namespace("flux-system")
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "e2e-podinfo", "namespace": "flux-system"},
		"spec": map[string]any{"url": "https://github.com/stefanprodan/podinfo", "interval": "10m",
			"ref": map[string]any{"branch": "master"}},
	}}
	k := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "e2e-podinfo", "namespace": "flux-system"},
		"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "e2e-podinfo"},
			"path": "./kustomize", "targetNamespace": gitopsNS, "prune": true, "interval": "1m"},
	}}
	if _, err := repos.Create(ctx, repo, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Delete(ctx, "e2e-podinfo", metav1.DeleteOptions{}) })
	if _, err := ks.Create(ctx, k, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ks.Delete(ctx, "e2e-podinfo", metav1.DeleteOptions{}) })
	var image string
	eventually(t, 5*time.Minute, "podinfo applied and available", func() (bool, error) {
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "podinfo", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		image = d.Spec.Template.Spec.Containers[0].Image
		return d.Status.AvailableReplicas > 0 && d.Status.AvailableReplicas == d.Status.Replicas, nil
	})

	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-flux", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "flux.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-flux", metav1.DeleteOptions{})
	})

	injected := time.Now()
	bad := `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + injected.Format(time.RFC3339Nano) + `"}},` +
		`"spec":{"images":[{"name":"ghcr.io/stefanprodan/podinfo","newTag":"does-not-exist"}]}}`
	if _, err := ks.Patch(ctx, "e2e-podinfo", types.MergePatchType, []byte(bad), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLogEntry(t, client, injected, 4*time.Minute, "a request to roll back through Flux", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "flux.rollback"
	})
	eventually(t, 3*time.Minute, "the Kustomization suspended and podinfo back on its image", func() (bool, error) {
		u, err := ks.Get(ctx, "e2e-podinfo", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "podinfo", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return suspended && u.GetAnnotations()["sentinel.karimzakzouk.github.io/paused-sync"] != "" &&
			d.Spec.Template.Spec.Containers[0].Image == image && d.Status.AvailableReplicas == d.Status.Replicas, nil
	})
	verified := waitForLogEntry(t, client, injected, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
		return e["msg"] == "action verified" || e["msg"] == "action unverified"
	})
	if verified["msg"] != "action verified" {
		t.Fatalf("the rollback was not verified: %v", verified)
	}
}

var (
	helmRepositories = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}
	helmReleases     = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
)

// A bad image delivered through a Flux HelmRelease that does not remediate
// failed upgrades itself: Sentinel suspends the HelmRelease, so
// helm-controller does not re-apply the bad values, rolls the Deployment
// back and verifies it.
func TestFluxRollbackSuspendsTheHelmRelease(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("helm.toolkit.fluxcd.io/v2"); err != nil {
		t.Skip("Flux's helm-controller is not installed: run hack/e2e-up.sh with E2E_FLUX=1")
	}
	admin := dynamicAs(t, "")
	repos, releases := admin.Resource(helmRepositories).Namespace("flux-system"), admin.Resource(helmReleases).Namespace("flux-system")
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmRepository",
		"metadata": map[string]any{"name": "e2e-podinfo-charts", "namespace": "flux-system"},
		"spec":     map[string]any{"url": "https://stefanprodan.github.io/podinfo", "interval": "10m"},
	}}
	release := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "e2e-helm-podinfo", "namespace": "flux-system"},
		"spec": map[string]any{"interval": "1m", "releaseName": "e2e-helm-podinfo", "targetNamespace": gitopsNS,
			"chart": map[string]any{"spec": map[string]any{"chart": "podinfo",
				"sourceRef": map[string]any{"kind": "HelmRepository", "name": "e2e-podinfo-charts"}}},
			// Helm marks an upgrade done once applied, so the bad pods show at once.
			"upgrade": map[string]any{"disableWait": true},
			"values":  map[string]any{"replicaCount": 1}},
	}}
	if _, err := repos.Create(ctx, repo, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Delete(ctx, "e2e-podinfo-charts", metav1.DeleteOptions{}) })
	if _, err := releases.Create(ctx, release, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = releases.Delete(ctx, "e2e-helm-podinfo", metav1.DeleteOptions{}) })
	var image string
	eventually(t, 5*time.Minute, "podinfo installed by Helm and available", func() (bool, error) {
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "e2e-helm-podinfo", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		image = d.Spec.Template.Spec.Containers[0].Image
		return d.Status.AvailableReplicas > 0 && d.Status.AvailableReplicas == d.Status.Replicas, nil
	})

	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-flux-helm", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "flux.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-flux-helm", metav1.DeleteOptions{})
	})

	injected := time.Now()
	bad := `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + injected.Format(time.RFC3339Nano) + `"}},` +
		`"spec":{"values":{"replicaCount":1,"image":{"tag":"does-not-exist"}}}}`
	if _, err := releases.Patch(ctx, "e2e-helm-podinfo", types.MergePatchType, []byte(bad), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLogEntry(t, client, injected, 4*time.Minute, "a request to roll back through Flux", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "flux.rollback" && e["target"] == "Deployment.apps/"+gitopsNS+"/e2e-helm-podinfo"
	})
	eventually(t, 3*time.Minute, "the HelmRelease suspended and podinfo back on its image", func() (bool, error) {
		u, err := releases.Get(ctx, "e2e-helm-podinfo", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "e2e-helm-podinfo", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return suspended && u.GetAnnotations()["sentinel.karimzakzouk.github.io/paused-sync"] != "" &&
			d.Spec.Template.Spec.Containers[0].Image == image && d.Status.AvailableReplicas == d.Status.Replicas, nil
	})
	verified := waitForLogEntry(t, client, injected, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
		return e["msg"] == "action verified" || e["msg"] == "action unverified"
	})
	if verified["msg"] != "action verified" {
		t.Fatalf("the rollback was not verified: %v", verified)
	}
}

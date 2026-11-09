package actor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Owner adapters act through the GitOps controller that keeps a workload in
// sync with Git: they pause it, so a plain rollback holds, and leave it
// paused with a note on how to resume. A person resumes it once Git is
// fixed (phase 4's Fixer opens that pull request).

// ActionArgoCDRollback pauses the Argo CD Application's automated sync,
// then rolls the Deployment back as rollout.undo does.
const ActionArgoCDRollback = "argocd.rollback"

// AnnotationPausedSync records on the owner what Sentinel paused, so a
// person or Sentinel can resume it as it was.
const AnnotationPausedSync = "sentinel.karimzakzouk.github.io/paused-sync"

// ApplicationsResource is Argo CD's Application.
var ApplicationsResource = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// ActionFluxRollback suspends the Flux Kustomization that applies a
// Deployment, then rolls the Deployment back as rollout.undo does.
const ActionFluxRollback = "flux.rollback"

// KustomizationsResource is Flux's Kustomization.
var KustomizationsResource = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}

// HelmReleasesResource is Flux's HelmRelease.
var HelmReleasesResource = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}

// RollbackAction names the request for rolling a workload back: through its
// owner when one keeps it in sync, plain otherwise. A policy allows each
// separately, so pausing a GitOps owner is a choice of its own.
func RollbackAction(meta metav1.ObjectMeta) string {
	switch {
	case reconciler(meta) == "Argo CD":
		return ActionArgoCDRollback
	case fluxKustomization(meta) != "", fluxHelmRelease(meta) != "":
		return ActionFluxRollback
	}
	return ActionRolloutUndo
}

// fluxHelmRelease returns namespace/name of the Flux HelmRelease that
// renders a workload, from the labels helm-controller sets.
func fluxHelmRelease(meta metav1.ObjectMeta) string {
	name, ns := meta.Labels["helm.toolkit.fluxcd.io/name"], meta.Labels["helm.toolkit.fluxcd.io/namespace"]
	if name == "" || ns == "" {
		return ""
	}
	return ns + "/" + name
}

// fluxKustomization returns namespace/name of the Flux Kustomization that
// applies a workload, from the labels kustomize-controller sets.
func fluxKustomization(meta metav1.ObjectMeta) string {
	name, ns := meta.Labels["kustomize.toolkit.fluxcd.io/name"], meta.Labels["kustomize.toolkit.fluxcd.io/namespace"]
	if name == "" || ns == "" {
		return ""
	}
	return ns + "/" + name
}

// pauseFlux suspends the Flux object that applies a workload, a
// Kustomization or a HelmRelease, and notes it. When another manager sets
// its spec.suspend, a parent Kustomization say, it would resume it at its
// next reconcile, so the actor refuses. So it does when helm-controller rolls
// a failed upgrade back itself: two rollbacks would race.
func (a *Actor) pauseFlux(ctx context.Context, meta metav1.ObjectMeta, dryRun bool) (string, error) {
	kind, resource, ref := "Kustomization", KustomizationsResource, fluxKustomization(meta)
	if ref == "" {
		kind, resource, ref = "HelmRelease", HelmReleasesResource, fluxHelmRelease(meta)
	}
	ns, name, ok := strings.Cut(ref, "/")
	if !ok {
		return "", fmt.Errorf("%s has no Flux labels", meta.Name)
	}
	ks := a.Dynamic.Resource(resource).Namespace(ns)
	k, err := ks.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("the Flux %s %s/%s: %w", kind, ns, name, err)
	}
	for _, f := range k.GetManagedFields() {
		if f.Manager != FieldManager && f.FieldsV1 != nil && strings.Contains(f.FieldsV1.GetRawString(), `"f:suspend"`) {
			return "", fmt.Errorf("%s sets the Flux %s %s/%s's spec.suspend and would resume it; Sentinel leaves it to people",
				f.Manager, kind, ns, name)
		}
	}
	if retries, _, _ := unstructured.NestedInt64(k.Object, "spec", "upgrade", "remediation", "retries"); kind == "HelmRelease" && retries != 0 {
		return "", fmt.Errorf("helm-controller rolls the HelmRelease %s/%s back itself after a failed upgrade "+
			"(spec.upgrade.remediation); Sentinel leaves it to helm-controller", ns, name)
	}
	if suspended, _, _ := unstructuredBool(k.Object, "spec", "suspend"); suspended {
		return fmt.Sprintf("the Flux %s %s/%s was already suspended", kind, ns, name), nil
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{AnnotationPausedSync: "suspended"}},
		"spec":     map[string]any{"suspend": true},
	})
	if err != nil {
		return "", err
	}
	opts := metav1.PatchOptions{FieldManager: FieldManager}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if _, err := ks.Patch(ctx, name, types.MergePatchType, patch, opts); err != nil {
		return "", fmt.Errorf("suspend the Flux %s %s/%s: %w", kind, ns, name, err)
	}
	command := "flux"
	if kind == "HelmRelease" {
		command = "helmrelease"
	}
	return fmt.Sprintf("suspended the Flux %s %s/%s; once Git is fixed, resume it with "+
		"`sentinel resume %s %s/%s`", kind, ns, name, command, ns, name), nil
}

func unstructuredBool(obj map[string]any, path ...string) (bool, bool, error) {
	parent, found, err := unstructuredMap(obj, path[:len(path)-1]...)
	if !found || err != nil {
		return false, found, err
	}
	b, ok := parent[path[len(path)-1]].(bool)
	return b, ok, nil
}

// argoApplication returns the namespace and name of the Argo CD Application
// that tracks a workload, from its tracking annotation
// ("app:group/Kind:namespace/name", with "namespace_app" when Applications
// live outside Argo CD's own namespace).
func argoApplication(meta metav1.ObjectMeta, argoNamespace string) (string, string, bool) {
	id := meta.Annotations["argocd.argoproj.io/tracking-id"]
	app, _, ok := strings.Cut(id, ":")
	if !ok || app == "" {
		return "", "", false
	}
	if ns, name, found := strings.Cut(app, "_"); found {
		return ns, name, true
	}
	return argoNamespace, app, true
}

// pauseArgoCD removes the Application's automated sync and records it in an
// annotation. An Application that does not sync on its own is left as is.
func (a *Actor) pauseArgoCD(ctx context.Context, meta metav1.ObjectMeta, dryRun bool) (string, error) {
	ns, name, ok := argoApplication(meta, a.ArgoCDNamespace)
	if !ok {
		return "", fmt.Errorf("%s has no Argo CD tracking annotation", meta.Name)
	}
	apps := a.Dynamic.Resource(ApplicationsResource).Namespace(ns)
	app, err := apps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("the Argo CD Application %s/%s: %w", ns, name, err)
	}
	if set, ok := applicationSet(app.GetOwnerReferences()); ok {
		if err := a.applicationSetKeepsPause(ctx, ns, set); err != nil {
			return "", err
		}
	}
	automated, found, _ := unstructuredMap(app.Object, "spec", "syncPolicy", "automated")
	if !found {
		return fmt.Sprintf("Argo CD Application %s/%s does not sync on its own", ns, name), nil
	}
	was, err := json.Marshal(automated)
	if err != nil {
		return "", err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{AnnotationPausedSync: string(was)}},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": nil}},
	})
	if err != nil {
		return "", err
	}
	opts := metav1.PatchOptions{FieldManager: FieldManager}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if _, err := apps.Patch(ctx, name, types.MergePatchType, patch, opts); err != nil {
		return "", fmt.Errorf("pause Argo CD Application %s/%s: %w", ns, name, err)
	}
	return fmt.Sprintf("paused Argo CD Application %s/%s's automated sync; once Git is fixed, resume it with "+
		"`sentinel resume argocd %s/%s`", ns, name, ns, name), nil
}

// ApplicationSetsResource is Argo CD's ApplicationSet.
var ApplicationSetsResource = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

func applicationSet(owners []metav1.OwnerReference) (string, bool) {
	for _, o := range owners {
		if o.Kind == "ApplicationSet" && strings.HasPrefix(o.APIVersion, "argoproj.io/") {
			return o.Name, true
		}
	}
	return "", false
}

// applicationSetKeepsPause checks that the ApplicationSet generating an
// Application leaves its sync policy alone; otherwise it would write the
// automated sync back and the rollback would be undone.
func (a *Actor) applicationSetKeepsPause(ctx context.Context, ns, name string) error {
	set, err := a.Dynamic.Resource(ApplicationSetsResource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("the ApplicationSet %s/%s that generates the Application: %w", ns, name, err)
	}
	ignored, _, _ := unstructuredSlice(set.Object, "spec", "ignoreApplicationDifferences")
	for _, rule := range ignored {
		r, _ := rule.(map[string]any)
		pointers, _ := r["jsonPointers"].([]any)
		for _, p := range pointers {
			if s, _ := p.(string); s == "/spec/syncPolicy" || s == "/spec/syncPolicy/automated" {
				return nil
			}
		}
	}
	return fmt.Errorf("the ApplicationSet %s/%s generates the Application and would restore its automated sync; "+
		"Sentinel leaves it to people unless the ApplicationSet ignores differences in /spec/syncPolicy", ns, name)
}

func unstructuredSlice(obj map[string]any, path ...string) ([]any, bool, error) {
	parent, found, err := unstructuredMap(obj, path[:len(path)-1]...)
	if !found || err != nil {
		return nil, found, err
	}
	s, ok := parent[path[len(path)-1]].([]any)
	return s, ok, nil
}

func unstructuredMap(obj map[string]any, path ...string) (map[string]any, bool, error) {
	cur := obj
	for i, p := range path {
		next, ok := cur[p]
		if !ok || next == nil {
			return nil, false, nil
		}
		m, ok := next.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("%s is not an object", strings.Join(path[:i+1], "."))
		}
		cur = m
	}
	return cur, true, nil
}

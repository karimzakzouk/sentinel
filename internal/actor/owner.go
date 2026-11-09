package actor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// RollbackAction names the request for rolling a workload back: through its
// owner when one keeps it in sync, plain otherwise. A policy allows each
// separately, so pausing a GitOps owner is a choice of its own.
func RollbackAction(meta metav1.ObjectMeta) string {
	if reconciler(meta) == "Argo CD" {
		return ActionArgoCDRollback
	}
	return ActionRolloutUndo
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
	return fmt.Sprintf("paused Argo CD Application %s/%s's automated sync (resume it once Git is fixed)", ns, name), nil
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

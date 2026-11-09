// Package resume resumes the GitOps owners Sentinel paused before a
// rollback, as a person does once Git is fixed: an Argo CD Application's
// automated sync, or a Flux Kustomization or HelmRelease.
package resume

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/karimzakzouk/sentinel/internal/actor"
)

// FieldManager names a person's resume in managedFields.
const FieldManager = "sentinel-cli"

// ArgoCD restores an Application's automated sync as Sentinel's note
// recorded it. An ApplicationSet drops annotations it does not set, the note
// among them, so an Application one generates is resumed without a note to
// the automated sync its ApplicationSet's template sets. It says what it
// did.
func ArgoCD(ctx context.Context, dyn dynamic.Interface, ns, name string) (string, error) {
	apps := dyn.Resource(actor.ApplicationsResource).Namespace(ns)
	app, err := apps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	var automated map[string]any
	how := "as it was"
	if was := app.GetAnnotations()[actor.AnnotationPausedSync]; was != "" {
		if err := json.Unmarshal([]byte(was), &automated); err != nil {
			return "", fmt.Errorf("the note of what Sentinel paused is unreadable: %w", err)
		}
	} else if set, ok := actor.ApplicationSetOf(app.GetOwnerReferences()); ok {
		s, err := dyn.Resource(actor.ApplicationSetsResource).Namespace(ns).Get(ctx, set, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("the ApplicationSet %s/%s that generates the Application: %w", ns, set, err)
		}
		m, found, err := unstructured.NestedMap(s.Object, "spec", "template", "spec", "syncPolicy", "automated")
		if err != nil || !found {
			return "", fmt.Errorf("the ApplicationSet %s/%s's template sets no automated sync Sentinel can restore; resume it in Argo CD", ns, set)
		}
		automated, how = m, "as the ApplicationSet "+ns+"/"+set+" sets it"
	} else {
		return "", fmt.Errorf("%s/%s carries no note of a pause by Sentinel", ns, name)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{actor.AnnotationPausedSync: nil}},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": automated}},
	})
	if err != nil {
		return "", err
	}
	if _, err := apps.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManager}); err != nil {
		return "", err
	}
	shown, err := json.Marshal(automated)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("resumed %s/%s's automated sync %s: %s", ns, name, how, shown), nil
}

// Flux resumes the Flux Kustomization or HelmRelease Sentinel suspended.
func Flux(ctx context.Context, dyn dynamic.Interface, resource schema.GroupVersionResource, kind, ns, name string) (string, error) {
	ks := dyn.Resource(resource).Namespace(ns)
	k, err := ks.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if k.GetAnnotations()[actor.AnnotationPausedSync] == "" {
		return "", fmt.Errorf("%s/%s carries no note of a pause by Sentinel", ns, name)
	}
	patch := []byte(`{"metadata":{"annotations":{"` + actor.AnnotationPausedSync + `":null}},"spec":{"suspend":false}}`)
	if _, err := ks.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManager}); err != nil {
		return "", err
	}
	return fmt.Sprintf("resumed the Flux %s %s/%s", kind, ns, name), nil
}

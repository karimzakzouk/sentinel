package cli

import (
	"bytes"
	"context"
	"io"
	"maps"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/karimzakzouk/sentinel/internal/actor"
)

// Sentinel's note says how an Application synced before the pause. One an
// ApplicationSet generates loses the note and resumes as the ApplicationSet's
// template says; any other Application without a note is left alone.
func TestResumeArgoCD(t *testing.T) {
	ctx := context.Background()
	app := func(name, note, set string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": name, "namespace": "argocd"}, "spec": map[string]any{"syncPolicy": map[string]any{}}}}
		if note != "" {
			u.SetAnnotations(map[string]string{actor.AnnotationPausedSync: note})
		}
		if set != "" {
			u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationSet", Name: set, UID: "set"}})
		}
		return u
	}
	set := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
		"metadata": map[string]any{"name": "shops", "namespace": "argocd"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"syncPolicy": map[string]any{"automated": map[string]any{"prune": true}}}}}}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		app("noted", `{"selfHeal":true}`, ""), app("generated", "", "shops"), app("other", "", ""), set)

	for name, want := range map[string]struct {
		said      string
		automated map[string]any
	}{
		"noted":     {`as it was: {"selfHeal":true}`, map[string]any{"selfHeal": true}},
		"generated": {`as the ApplicationSet argocd/shops sets it: {"prune":true}`, map[string]any{"prune": true}},
	} {
		var out bytes.Buffer
		if err := resumeArgoCD(ctx, &out, dyn, "argocd", name); err != nil || !strings.Contains(out.String(), want.said) {
			t.Fatalf("%s: %q, %v", name, out.String(), err)
		}
		u, err := dyn.Resource(actor.ApplicationsResource).Namespace("argocd").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		automated, _, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated")
		if !maps.Equal(automated, want.automated) || u.GetAnnotations()[actor.AnnotationPausedSync] != "" {
			t.Fatalf("%s: automated %v, annotations %v", name, automated, u.GetAnnotations())
		}
	}
	if err := resumeArgoCD(ctx, io.Discard, dyn, "argocd", "other"); err == nil || !strings.Contains(err.Error(), "carries no note") {
		t.Fatalf("an Application Sentinel did not pause was resumed: %v", err)
	}
}

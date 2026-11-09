package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// newResumeCmd resumes what Sentinel paused before a rollback, as it was,
// once Git is fixed: an Argo CD Application's automated sync, or a Flux
// Kustomization or HelmRelease. It runs as the caller.
func newResumeCmd(kf *kube.Flags) *cobra.Command {
	return &cobra.Command{
		Use:   "resume argocd|flux|helmrelease NAMESPACE/NAME",
		Short: "Resume the GitOps sync Sentinel paused, once Git is fixed",
		Long: "Before rolling back a workload Argo CD or Flux keeps in sync, Sentinel pauses the Application's automated sync " +
			"or suspends the Kustomization or HelmRelease, so the rollback holds. Fix the change in Git first: resuming re-applies " +
			"whatever Git says.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, name, ok := strings.Cut(args[1], "/")
			if !ok || ns == "" || name == "" {
				return fmt.Errorf("name it as NAMESPACE/NAME")
			}
			if args[0] != "argocd" && args[0] != "flux" && args[0] != "helmrelease" {
				return fmt.Errorf("resume argocd, flux or helmrelease, not %q", args[0])
			}
			rc, err := kf.RESTConfig(version.UserAgent("cli"))
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
			}
			switch args[0] {
			case "flux":
				return resumeFlux(cmd.Context(), cmd.OutOrStdout(), dyn, actor.KustomizationsResource, "Kustomization", ns, name)
			case "helmrelease":
				return resumeFlux(cmd.Context(), cmd.OutOrStdout(), dyn, actor.HelmReleasesResource, "HelmRelease", ns, name)
			}
			return resumeArgoCD(cmd.Context(), cmd.OutOrStdout(), dyn, ns, name)
		},
	}
}

// resumeArgoCD restores an Application's automated sync as Sentinel's note
// recorded it. An ApplicationSet drops annotations it does not set, the note
// among them, so an Application one generates is resumed without a note to
// the automated sync its ApplicationSet's template sets.
func resumeArgoCD(ctx context.Context, out io.Writer, dyn dynamic.Interface, ns, name string) error {
	apps := dyn.Resource(actor.ApplicationsResource).Namespace(ns)
	app, err := apps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var automated map[string]any
	how := "as it was"
	if was := app.GetAnnotations()[actor.AnnotationPausedSync]; was != "" {
		if err := json.Unmarshal([]byte(was), &automated); err != nil {
			return fmt.Errorf("the note of what Sentinel paused is unreadable: %w", err)
		}
	} else if set, ok := actor.ApplicationSetOf(app.GetOwnerReferences()); ok {
		s, err := dyn.Resource(actor.ApplicationSetsResource).Namespace(ns).Get(ctx, set, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("the ApplicationSet %s/%s that generates the Application: %w", ns, set, err)
		}
		m, found, err := unstructured.NestedMap(s.Object, "spec", "template", "spec", "syncPolicy", "automated")
		if err != nil || !found {
			return fmt.Errorf("the ApplicationSet %s/%s's template sets no automated sync Sentinel can restore; resume it in Argo CD", ns, set)
		}
		automated, how = m, "as the ApplicationSet "+ns+"/"+set+" sets it"
	} else {
		return fmt.Errorf("%s/%s carries no note of a pause by Sentinel", ns, name)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{actor.AnnotationPausedSync: nil}},
		"spec":     map[string]any{"syncPolicy": map[string]any{"automated": automated}},
	})
	if err != nil {
		return err
	}
	if _, err := apps.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sentinel-cli"}); err != nil {
		return err
	}
	shown, err := json.Marshal(automated)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "resumed %s/%s's automated sync %s: %s\n", ns, name, how, shown)
	return err
}

func resumeFlux(ctx context.Context, out io.Writer, dyn dynamic.Interface, resource schema.GroupVersionResource, kind, ns, name string) error {
	ks := dyn.Resource(resource).Namespace(ns)
	k, err := ks.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if k.GetAnnotations()[actor.AnnotationPausedSync] == "" {
		return fmt.Errorf("%s/%s carries no note of a pause by Sentinel", ns, name)
	}
	patch := []byte(`{"metadata":{"annotations":{"` + actor.AnnotationPausedSync + `":null}},"spec":{"suspend":false}}`)
	if _, err := ks.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sentinel-cli"}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "resumed the Flux %s %s/%s\n", kind, ns, name)
	return err
}

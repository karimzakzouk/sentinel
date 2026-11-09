package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				return resumeFlux(cmd, dyn, actor.KustomizationsResource, "Kustomization", ns, name)
			case "helmrelease":
				return resumeFlux(cmd, dyn, actor.HelmReleasesResource, "HelmRelease", ns, name)
			}
			apps := dyn.Resource(actor.ApplicationsResource).Namespace(ns)
			app, err := apps.Get(cmd.Context(), name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			was := app.GetAnnotations()[actor.AnnotationPausedSync]
			if was == "" {
				return fmt.Errorf("%s carries no note of a pause by Sentinel", args[1])
			}
			var automated map[string]any
			if err := json.Unmarshal([]byte(was), &automated); err != nil {
				return fmt.Errorf("the note of what Sentinel paused is unreadable: %w", err)
			}
			patch, err := json.Marshal(map[string]any{
				"metadata": map[string]any{"annotations": map[string]any{actor.AnnotationPausedSync: nil}},
				"spec":     map[string]any{"syncPolicy": map[string]any{"automated": automated}},
			})
			if err != nil {
				return err
			}
			if _, err := apps.Patch(cmd.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sentinel-cli"}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "resumed %s's automated sync as it was: %s\n", args[1], was)
			return err
		},
	}
}

func resumeFlux(cmd *cobra.Command, dyn dynamic.Interface, resource schema.GroupVersionResource, kind, ns, name string) error {
	ks := dyn.Resource(resource).Namespace(ns)
	k, err := ks.Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if k.GetAnnotations()[actor.AnnotationPausedSync] == "" {
		return fmt.Errorf("%s/%s carries no note of a pause by Sentinel", ns, name)
	}
	patch := []byte(`{"metadata":{"annotations":{"` + actor.AnnotationPausedSync + `":null}},"spec":{"suspend":false}}`)
	if _, err := ks.Patch(cmd.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "sentinel-cli"}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "resumed the Flux %s %s/%s\n", kind, ns, name)
	return err
}

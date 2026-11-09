package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// newResumeCmd restores the automated sync Sentinel paused on an Argo CD
// Application, as it was, once Git is fixed. It runs as the caller.
func newResumeCmd(kf *kube.Flags) *cobra.Command {
	return &cobra.Command{
		Use:   "resume argocd NAMESPACE/APPLICATION",
		Short: "Resume the Argo CD sync Sentinel paused, once Git is fixed",
		Long: "Sentinel pauses an Argo CD Application's automated sync before rolling back a workload it keeps in sync, " +
			"so the rollback holds. Fix the change in Git first: resuming re-applies whatever Git says.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "argocd" {
				return fmt.Errorf("only Argo CD Applications can be resumed so far")
			}
			ns, name, ok := strings.Cut(args[1], "/")
			if !ok || ns == "" || name == "" {
				return fmt.Errorf("name the Application as NAMESPACE/APPLICATION")
			}
			rc, err := kf.RESTConfig(version.UserAgent("cli"))
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
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

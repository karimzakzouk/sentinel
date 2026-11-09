package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"

	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/resume"
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
			var said string
			switch args[0] {
			case "flux":
				said, err = resume.Flux(cmd.Context(), dyn, actor.KustomizationsResource, "Kustomization", ns, name)
			case "helmrelease":
				said, err = resume.Flux(cmd.Context(), dyn, actor.HelmReleasesResource, "HelmRelease", ns, name)
			default:
				said, err = resume.ArgoCD(cmd.Context(), dyn, ns, name)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), said)
			return err
		},
	}
}

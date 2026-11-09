package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// newDecideCmd returns the approve or veto command: it sets the request's
// annotation to the caller's own identity, as the fence requires.
func newDecideCmd(kf *kube.Flags, use, annotation, short, did string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " NAMESPACE/REQUEST",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, name, ok := strings.Cut(args[0], "/")
			if !ok || ns == "" || name == "" {
				return fmt.Errorf("name the request as NAMESPACE/REQUEST, as kubectl get actionrequests -A shows it")
			}
			rc, err := kf.RESTConfig(version.UserAgent("cli"))
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(rc)
			if err != nil {
				return err
			}
			review, err := client.AuthenticationV1().SelfSubjectReviews().Create(cmd.Context(), &authenticationv1.SelfSubjectReview{},
				metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("could not tell who you are: %w", err)
			}
			who := review.Status.UserInfo.Username
			patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{annotation: who}}})
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
			}
			if _, err := dyn.Resource(actor.RequestsResource).Namespace(ns).Patch(cmd.Context(), name, types.MergePatchType, patch,
				metav1.PatchOptions{FieldManager: "sentinel-cli"}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s as %s\n", did, args[0], who)
			return err
		},
	}
}

func newApproveCmd(kf *kube.Flags) *cobra.Command {
	return newDecideCmd(kf, "approve", actor.AnnotationApprovedBy,
		"Approve an action Sentinel asked for, as yourself", "approved")
}

func newVetoCmd(kf *kube.Flags) *cobra.Command {
	return newDecideCmd(kf, "veto", actor.AnnotationVetoedBy,
		"Veto an action Sentinel asked for, as yourself", "vetoed")
}

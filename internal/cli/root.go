// Package cli implements the sentinel command.
package cli

import (
	"encoding/json"
	"io"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/kube"
)

// NewRoot builds the sentinel command tree.
func NewRoot() *cobra.Command {
	var kf kube.Flags
	root := &cobra.Command{
		Use:           "sentinel",
		Short:         "Sentinel: a self-hosted autonomous SRE for Kubernetes",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&kf.Kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
	pf.StringVar(&kf.Context, "context", "", "kubeconfig context to use")
	pf.StringVar(&kf.As, "as", "", "user to impersonate, for example system:serviceaccount:sentinel:sentinel")
	pf.StringSliceVar(&kf.AsGroups, "as-group", nil, "group to impersonate; repeatable")

	root.AddCommand(newVersionCmd(), newCapabilitiesCmd(&kf), newLedgerCmd(), newCheckProofCmd(), newIncidentsCmd(), newReportCmd(), newRunCmd(&kf), newActorCmd(&kf), newApproveCmd(&kf), newVetoCmd(&kf), newResumeCmd(&kf), newLabCmd(&kf), newFixerCmd(&kf))
	return root
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

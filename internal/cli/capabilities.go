package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

func newCapabilitiesCmd(kf *kube.Flags) *cobra.Command {
	var (
		opts   capability.Options
		active bool
		settle time.Duration
		output string
	)
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Report what Sentinel can see and do on this cluster",
		Long: `Report what Sentinel can see and do on this cluster, and why.

Permissions are checked for the identity the command runs as. To check
Sentinel's own service account from an admin kubeconfig, impersonate it:

  sentinel capabilities --namespaces shop --as system:serviceaccount:sentinel:sentinel

--active proves NetworkPolicy enforcement with real traffic: it starts two
short-lived pods and a deny policy in --probe-namespace, then deletes them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("unknown output format %q", output)
			}
			if active && opts.ProbeNamespace == "" {
				return errors.New("--active needs --probe-namespace")
			}
			client, err := kf.Clientset(version.UserAgent("cli"))
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if active {
				c := capability.NetworkPolicyProbe{Client: client, Namespace: opts.ProbeNamespace, Settle: settle}.Run(ctx)
				opts.NetworkPolicy = &c
			}
			rep, err := (&capability.Detector{Client: client, Options: opts}).Run(ctx)
			if err != nil {
				return err
			}
			if output == "json" {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			return rep.WriteText(cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&opts.Namespaces, "namespaces", nil, "workload namespaces Sentinel may watch (required)")
	f.StringVar(&opts.ProbeNamespace, "probe-namespace", "", "namespace for dry-run and active probes")
	f.StringVar(&opts.ActorUser, "actor", "", "identity that executes actions, checked with SubjectAccessReview")
	f.BoolVar(&active, "active", false, "prove NetworkPolicy enforcement with live traffic")
	f.DurationVar(&settle, "settle", time.Minute, "how long a deny policy may take to start blocking traffic")
	f.StringVarP(&output, "output", "o", "text", "output format: text or json")
	_ = cmd.MarkFlagRequired("namespaces")
	return cmd
}

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/version"
)

func newVersionCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			if output == "json" {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "sentinel %s (%s, %s)\n", info.Version, shortRev(info.Revision, info.Modified), info.GoVersion)
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	return cmd
}

func shortRev(rev string, modified bool) string {
	if rev == "" {
		return "unknown revision"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified {
		rev += "-dirty"
	}
	return rev
}

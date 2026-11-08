package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/ledger"
)

func newLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ledger", Short: "Inspect a Sentinel ledger file"}
	var path string
	cmd.PersistentFlags().StringVar(&path, "path", "", "path to the ledger database (required)")
	_ = cmd.MarkPersistentFlagRequired("path")

	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check every event against the hash chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := ledger.Open(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			n, err := l.Verify(cmd.Context())
			if err != nil {
				return fmt.Errorf("verified %d event(s), then: %w", n, err)
			}
			seq, hash, err := l.Head(cmd.Context())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok: %d event(s) verified; head %d %s\n", n, seq, hash)
			return err
		},
	}

	var after int64
	export := &cobra.Command{
		Use:   "export",
		Short: "Print events as CloudEvents JSON, one per line",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := ledger.Open(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			enc := json.NewEncoder(cmd.OutOrStdout())
			for {
				events, err := l.Events(cmd.Context(), after, 1000)
				if err != nil {
					return err
				}
				if len(events) == 0 {
					return nil
				}
				for _, e := range events {
					if err := enc.Encode(e.CloudEvent()); err != nil {
						return err
					}
					after = e.Seq
				}
			}
		},
	}
	export.Flags().Int64Var(&after, "after", 0, "export only events after this sequence number")

	cmd.AddCommand(verify, export)
	return cmd
}

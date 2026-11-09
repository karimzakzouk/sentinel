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

	var size int64
	root := &cobra.Command{
		Use:   "root",
		Short: "Print the Merkle root of the events, to publish where Sentinel cannot rewrite it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := ledger.Open(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			n, r, err := l.Root(cmd.Context(), size)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%d %s\n", n, r)
			return err
		},
	}
	root.Flags().Int64Var(&size, "size", 0, "the root of the first this many events; every event when 0")

	var seq, from int64
	prove := &cobra.Command{
		Use:   "prove",
		Short: "Print, as JSON, the proof that an event is in the tree of a root, or that an earlier root begins it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := ledger.Open(cmd.Context(), path)
			if err != nil {
				return err
			}
			defer func() { _ = l.Close() }()
			var proof any
			switch {
			case seq > 0 && from == 0:
				proof, err = l.Prove(cmd.Context(), seq, size)
			case from > 0 && seq == 0:
				proof, err = l.Consistency(cmd.Context(), from, size)
			default:
				return fmt.Errorf("give --event for an event's proof or --from for an earlier root's")
			}
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(proof)
		},
	}
	prove.Flags().Int64Var(&seq, "event", 0, "the sequence number of the event to prove")
	prove.Flags().Int64Var(&from, "from", 0, "the size of the earlier tree to prove the current one begins with")
	prove.Flags().Int64Var(&size, "size", 0, "the tree of the first this many events; every event when 0")

	cmd.AddCommand(verify, export, root, prove)
	return cmd
}

// newCheckProofCmd checks a proof without the Ledger, against a root
// someone published.
func newCheckProofCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "check-proof",
		Short: "Check a proof that sentinel ledger prove printed, read from standard input, against a published root",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var raw map[string]json.RawMessage
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&raw); err != nil {
				return fmt.Errorf("read the proof: %w", err)
			}
			data, _ := json.Marshal(raw)
			if _, ok := raw["eventHash"]; ok {
				var p ledger.InclusionProof
				if err := json.Unmarshal(data, &p); err != nil {
					return err
				}
				if root != "" && p.Root != root {
					return fmt.Errorf("the proof leads to %s, not the published %s", p.Root, root)
				}
				if err := p.Check(); err != nil {
					return err
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "ok: event %d (%s) is in the tree of %d events with root %s\n", p.Seq, p.EventHash, p.Size, p.Root)
				return err
			}
			var p ledger.ConsistencyProof
			if err := json.Unmarshal(data, &p); err != nil {
				return err
			}
			if root != "" && p.OldRoot != root {
				return fmt.Errorf("the proof begins with %s, not the published %s", p.OldRoot, root)
			}
			if err := p.Check(); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "ok: the tree of %d events with root %s begins the tree of %d with root %s\n",
				p.From, p.OldRoot, p.To, p.NewRoot)
			return err
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "the root published earlier, which an event's proof must lead to or the later tree must begin with")
	return cmd
}

package cli

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
)

// defaultLedger is where the chart keeps the Ledger, so that
// "kubectl exec deploy/sentinel -- /sentinel incidents" needs no flags.
const defaultLedger = "/var/lib/sentinel/ledger.db"

func loadIncidents(cmd *cobra.Command, path string, since time.Duration) ([]model.Incident, error) {
	l, err := ledger.Open(cmd.Context(), path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = l.Close() }()
	all, err := ledger.List[model.Incident](cmd.Context(), l, model.KindIncident, "")
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-since)
	var out []model.Incident
	for _, inc := range all {
		if inc.MergedInto == "" && (since == 0 || inc.OpenedAt.After(cutoff)) {
			out = append(out, inc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if oi, oj := isOpen(out[i]), isOpen(out[j]); oi != oj {
			return oi
		}
		return out[i].OpenedAt.After(out[j].OpenedAt)
	})
	return out, nil
}

func isOpen(inc model.Incident) bool {
	return inc.State != model.IncidentResolved && inc.State != model.IncidentClosed
}

func workloadName(o model.ObjectRef) string {
	if o.Namespace == "" {
		return o.Name
	}
	return o.Namespace + "/" + o.Name
}

func newIncidentsCmd() *cobra.Command {
	var path string
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "incidents",
		Short: "List incidents and what Sentinel concluded about each",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			incidents, err := loadIncidents(cmd, path, since)
			if err != nil {
				return err
			}
			return writeIncidents(cmd.OutOrStdout(), incidents)
		},
	}
	cmd.Flags().StringVar(&path, "path", defaultLedger, "path to the ledger database")
	cmd.Flags().DurationVar(&since, "since", 0, "only incidents opened within this long, such as 24h")
	return cmd
}

func writeIncidents(w io.Writer, incidents []model.Incident) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OPENED\tSTATE\tWORKLOAD\tCLASS\tLIKELY CAUSE\tWOULD DO")
	for _, inc := range incidents {
		class, cause, decision := "-", "-", "-"
		if a := inc.Assessment; a != nil {
			class = fmt.Sprintf("%s (%.0f%%)", a.Class, 100*a.ClassP)
			decision = a.Decision
			switch {
			case a.Culprit != nil:
				cause = fmt.Sprintf("%s change to %s (%.0f%%)", a.Culprit.Kind, workloadName(a.Culprit.Object), 100*a.Culprit.P)
			case len(a.RootCauses) > 0:
				cause = fmt.Sprintf("%s (%.0f%%)", workloadName(a.RootCauses[0].Object), 100*a.RootCauses[0].P)
			}
		}
		where := "-"
		if len(inc.Objects) > 0 {
			where = workloadName(inc.Objects[0])
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", inc.OpenedAt.Local().Format("Jan 02 15:04"), inc.State, where, class, cause, decision)
	}
	return tw.Flush()
}

func newReportCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "report", Short: "Summaries of what Sentinel did and would have done"}
	var path string
	var since time.Duration
	shadow := &cobra.Command{
		Use:   "shadow",
		Short: "What Sentinel would have done in observe mode",
		Long: `Summarizes, for the incidents opened in the period, the actions Sentinel
would have taken had it been allowed to act: how many rollbacks, replica
restores and escalations, for which fault classes, and how sure it was.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			incidents, err := loadIncidents(cmd, path, since)
			if err != nil {
				return err
			}
			return writeShadowReport(cmd.OutOrStdout(), incidents, since)
		},
	}
	shadow.Flags().StringVar(&path, "path", defaultLedger, "path to the ledger database")
	shadow.Flags().DurationVar(&since, "since", 7*24*time.Hour, "the period to report on")
	cmd.AddCommand(shadow)
	return cmd
}

func writeShadowReport(w io.Writer, incidents []model.Incident, since time.Duration) error {
	byDecision := map[string][]model.Incident{}
	classes := map[string]int{}
	for _, inc := range incidents {
		decision := "unassessed"
		if a := inc.Assessment; a != nil {
			decision = a.Decision
			classes[a.Class]++
		}
		byDecision[decision] = append(byDecision[decision], inc)
	}
	_, _ = fmt.Fprintf(w, "In the last %s Sentinel opened %d incident(s). In observe mode it acted on none.\n\n", since, len(incidents))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "IT WOULD HAVE\tINCIDENTS\tMEDIAN CONFIDENCE IN THE CAUSE")
	for _, d := range []string{"rollout.undo", "scale.restore", "escalate", "none", "unassessed"} {
		incs := byDecision[d]
		if len(incs) == 0 {
			continue
		}
		var ps []float64
		for _, inc := range incs {
			if a := inc.Assessment; a != nil && a.Culprit != nil {
				ps = append(ps, a.Culprit.P)
			}
		}
		confidence := "-"
		if len(ps) > 0 {
			sort.Float64s(ps)
			confidence = fmt.Sprintf("%.0f%%", 100*ps[len(ps)/2])
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", shadowAction(d), len(incs), confidence)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(classes) > 0 {
		_, _ = fmt.Fprintln(w, "\nBy fault class:")
		names := make([]string, 0, len(classes))
		for c := range classes {
			names = append(names, c)
		}
		sort.Slice(names, func(i, j int) bool {
			return classes[names[i]] > classes[names[j]] || classes[names[i]] == classes[names[j]] && names[i] < names[j]
		})
		for _, c := range names {
			_, _ = fmt.Fprintf(w, "  %-26s %d\n", c, classes[c])
		}
	}
	return nil
}

func shadowAction(decision string) string {
	switch decision {
	case "rollout.undo":
		return "rolled back a rollout"
	case "scale.restore":
		return "restored replicas"
	case "escalate":
		return "asked a person"
	case "none":
		return "done nothing"
	}
	return "not decided"
}

// Command harness injects scenario faults into a test cluster and records
// their ground truth. It is an evaluation tool, not part of Sentinel.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"

	"github.com/karimzakzouk/sentinel/internal/harness"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/version"
)

func main() {
	if err := newRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	var kf kube.Flags
	root := &cobra.Command{
		Use:           "harness",
		Short:         "Inject scenario faults and record their ground truth",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&kf.Kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
	root.PersistentFlags().StringVar(&kf.Context, "context", "", "kubeconfig context to use")
	var fo forgeOptions
	root.PersistentFlags().StringVar(&fo.api, "forge-api", os.Getenv("HARNESS_FORGE_API"),
		"for commit faults, the Gitea API's base URL, such as http://127.0.0.1:13000/api/v1")
	root.PersistentFlags().StringVar(&fo.secret, "forge-token-secret", "gitea/gitea-person",
		"for commit faults, NAMESPACE/NAME of the Secret holding the token of the person who owns the repositories")
	root.PersistentFlags().StringVar(&fo.build, "build", os.Getenv("HARNESS_BUILD"),
		"for release faults, the command that builds a commit as the source's pipeline would, such as hack/ci.sh build; "+
			"it is given the repository and the commit and prints the image tag last")

	validate := &cobra.Command{
		Use:   "validate SCENARIO...",
		Short: "Check scenario files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, p := range args {
				if _, err := harness.LoadScenario(p); err != nil {
					return err
				}
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%d scenario(s) valid\n", len(args))
			return err
		},
	}

	var scenarioPath, outDir string
	var revert bool
	inject := &cobra.Command{
		Use:   "inject",
		Short: "Inject one scenario and write its ground truth to --out",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := harness.LoadScenario(scenarioPath)
			if err != nil {
				return err
			}
			in, err := newInjector(cmd.Context(), kf, fo)
			if err != nil {
				return err
			}
			run, err := in.Inject(cmd.Context(), s)
			if err != nil {
				return err
			}
			if revert {
				if err := in.Revert(cmd.Context(), run); err != nil {
					return err
				}
			}
			path, err := writeRun(outDir, run)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: injected %s; %s; ground truth in %s\n", s.ID, run.InjectedAt.Format("15:04:05"), describe(run), path)
			return err
		},
	}
	inject.Flags().StringVar(&scenarioPath, "scenario", "", "scenario file (required)")
	inject.Flags().StringVar(&outDir, "out", "test/results", "directory for ground-truth records")
	inject.Flags().BoolVar(&revert, "revert", false, "revert right after the fault manifests")
	_ = inject.MarkFlagRequired("scenario")

	var runPath string
	revertCmd := &cobra.Command{
		Use:   "revert",
		Short: "Revert a recorded injection",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := os.ReadFile(runPath) //nolint:gosec // the path comes from the operator
			if err != nil {
				return err
			}
			var run harness.Run
			if err := json.Unmarshal(b, &run); err != nil {
				return err
			}
			in, err := newInjector(cmd.Context(), kf, fo)
			if err != nil {
				return err
			}
			if err := in.Revert(cmd.Context(), &run); err != nil {
				return err
			}
			if _, err := writeRun(filepath.Dir(runPath), &run); err != nil {
				return err
			}
			if run.Reverted != "" {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: reverted: %s\n", run.Scenario, run.Reverted)
			}
			return err
		},
	}
	revertCmd.Flags().StringVar(&runPath, "run", "", "ground-truth record written by inject (required)")
	_ = revertCmd.MarkFlagRequired("run")

	version := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "harness", version.Get().Version)
			return err
		},
	}

	var runsDir, ledgerPath, since string
	var grace time.Duration
	var asJSON bool
	score := &cobra.Command{
		Use:   "score",
		Short: "Score Sentinel's incidents against the runs' ground truth",
		Long: `Reads the run records in --runs and a Ledger export (sentinel ledger export),
and reports, per run and overall: detection (recall, and precision over every
incident opened), detection latency, the root cause's rank (AC@1, AC@3,
Avg@5), and whether the fault class, the culprit change and the decision were
right. Incidents during control runs are false alarms.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runs, err := harness.LoadRuns(runsDir)
			if err != nil {
				return err
			}
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				runs = slices.DeleteFunc(runs, func(r *harness.Run) bool { return r.InjectedAt.Before(t) })
			}
			f, err := os.Open(ledgerPath) //nolint:gosec // the path comes from the operator
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			export, err := harness.ReadExport(f)
			if err != nil {
				return err
			}
			rep := harness.Score(runs, export.Incidents, harness.ScoreOptions{Grace: grace, Actions: export.Actions, Fixes: export.Fixes})
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			return rep.WriteMarkdown(cmd.OutOrStdout())
		},
	}
	score.Flags().StringVar(&runsDir, "runs", "test/results", "directory of run records")
	score.Flags().StringVar(&ledgerPath, "ledger", "", "Ledger export to score (required)")
	score.Flags().StringVar(&since, "since", "", "score only runs injected at or after this time (RFC 3339)")
	score.Flags().DurationVar(&grace, "grace", 2*time.Minute, "how long after a revert an incident still counts toward the run")
	score.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	_ = score.MarkFlagRequired("ledger")

	root.AddCommand(validate, inject, revertCmd, score, version)
	return root
}

// forgeOptions say where commit faults go, whose token commits them, and
// what builds a release.
type forgeOptions struct{ api, secret, build string }

func newInjector(ctx context.Context, kf kube.Flags, fo forgeOptions) (*harness.Injector, error) {
	cfg, err := kf.RESTConfig(version.UserAgent("harness"))
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(client.Discovery()))
	in := &harness.Injector{Client: client, Dynamic: dyn, Mapper: mapper}
	if fo.api != "" {
		ns, name, ok := strings.Cut(fo.secret, "/")
		if !ok {
			return nil, fmt.Errorf("--forge-token-secret: name it as NAMESPACE/NAME")
		}
		secret, err := client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("the forge token: %w", err)
		}
		in.Forge = &harness.Forge{API: fo.api, Token: string(secret.Data["token"]), HTTP: &http.Client{Timeout: time.Minute}}
	}
	if args := strings.Fields(fo.build); len(args) > 0 {
		in.Build = func(ctx context.Context, repository, revision string) (string, error) {
			cmd := exec.CommandContext(ctx, args[0], append(args[1:], repository, revision)...) //nolint:gosec // the operator's own build command
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil {
				return "", err
			}
			lines := strings.Fields(string(out))
			if len(lines) == 0 {
				return "", fmt.Errorf("%s printed no image tag", fo.build)
			}
			return lines[len(lines)-1], nil
		}
	}
	return in, nil
}

func writeRun(dir string, run *harness.Run) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, run.Scenario+"-"+run.RunID+".json")
	return path, os.WriteFile(path, append(b, '\n'), 0o600)
}

func describe(run *harness.Run) string {
	switch {
	case run.FaultKind == harness.FaultNone:
		return "control, nothing injected"
	case run.Reverted != "":
		return run.Reverted
	case run.ManifestedAt != nil:
		return fmt.Sprintf("manifested after %s (%s)", run.ManifestedAt.Sub(run.InjectedAt).Round(1e9), run.Observation)
	case run.Observation != "":
		return run.Observation
	default:
		return "injected"
	}
}

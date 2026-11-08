// Command harness injects scenario faults into a test cluster and records
// their ground truth. It is an evaluation tool, not part of Sentinel.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
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
			in, err := newInjector(kf)
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
			in, err := newInjector(kf)
			if err != nil {
				return err
			}
			if err := in.Revert(cmd.Context(), &run); err != nil {
				return err
			}
			_, err = writeRun(filepath.Dir(runPath), &run)
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

	root.AddCommand(validate, inject, revertCmd, version)
	return root
}

func newInjector(kf kube.Flags) (*harness.Injector, error) {
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
	return &harness.Injector{Client: client, Dynamic: dyn, Mapper: mapper}, nil
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
	case run.ManifestedAt != nil:
		return fmt.Sprintf("manifested after %s (%s)", run.ManifestedAt.Sub(run.InjectedAt).Round(1e9), run.Observation)
	case run.Observation != "":
		return run.Observation
	default:
		return "injected"
	}
}

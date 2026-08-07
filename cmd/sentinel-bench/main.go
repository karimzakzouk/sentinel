// Package main is the sentinel-bench CLI — a standalone eval harness that
// runs Sentinel against a set of Kubernetes-failure scenarios and reports
// DBR / VPR / TAR / FMR.
//
// Usage:
//
//	sentinel-bench list
//	sentinel-bench validate
//	sentinel-bench run [--scenario S001] [--timeout 15m]
//	sentinel-bench results <version>
//
// Flags:
//
//	--server URL          Sentinel API URL (default: http://localhost:8000)
//	--token T             Bearer token for /admin/* (or SENTINEL_API_TOKEN env var)
//	--cluster NAME        Use an existing KinD cluster instead of creating one
//	--scenario ID         Run only the named scenario (repeatable)
//	--timeout D           Per-scenario wall-clock budget (default: 15m)
//	--scenarios-dir PATH  Path to scenarios/ (default: ./sentinel-bench/scenarios)
//	--results-dir PATH    Path to results/ (default: ./sentinel-bench/results)
//	--json                Output raw JSON instead of tables
//
// The runner is a standalone binary. It talks to Sentinel via HTTP only and
// shells out to `kind` and `kubectl` for cluster lifecycle. It does NOT
// import internal/* packages except internal/logging and internal/version.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// Version is the sentinel-bench build version. Sourced from
// internal/version (set via -ldflags from the VERSION file at build time).
var Version = version.Version

func main() {
	logging.Init("INFO")

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}
	for _, a := range os.Args[1:] {
		switch a {
		case "--help", "-h", "help":
			printHelp()
			os.Exit(0)
		case "--version", "-V":
			fmt.Printf("sentinel-bench %s\n", Version)
			os.Exit(0)
		}
	}

	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "list":
		handleList(args)
	case "validate":
		handleValidate(args)
	case "run":
		handleRun(args)
	case "results":
		handleResults(args)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

// globalFlags holds the flags shared by every subcommand.
type globalFlags struct {
	server       string
	token        string
	cluster      string
	scenarios    []string
	timeout      time.Duration
	scenariosDir string
	resultsDir   string
	json         bool
}

// parseGlobalFlags harvests the shared flags from args, returning the
// positional remainder. Supports both --flag value and --flag=value.
func parseGlobalFlags(args []string) ([]string, globalFlags) {
	g := globalFlags{
		server:       getenv("SENTINEL_SERVER", "http://localhost:8000"),
		token:        getenv("SENTINEL_API_TOKEN", ""),
		timeout:      15 * time.Minute,
		scenariosDir: "./sentinel-bench/scenarios",
		resultsDir:   "./sentinel-bench/results",
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
			parts := strings.SplitN(a, "=", 2)
			name, value := parts[0], parts[1]
			if setGlobalFlag(&g, name, value) {
				continue
			}
		}
		switch a {
		case "--server", "--token", "--cluster", "--timeout", "--scenarios-dir", "--results-dir":
			if i+1 < len(args) {
				setGlobalFlag(&g, a, args[i+1])
				i++
				continue
			}
		case "--json":
			g.json = true
			continue
		case "--scenario":
			if i+1 < len(args) {
				g.scenarios = append(g.scenarios, args[i+1])
				i++
				continue
			}
		}
		out = append(out, a)
	}
	g.server = strings.TrimRight(g.server, "/")
	return out, g
}

// setGlobalFlag assigns a value to the named flag. Returns true if the
// flag was recognized.
func setGlobalFlag(g *globalFlags, name, value string) bool {
	switch name {
	case "--server":
		g.server = value
	case "--token":
		g.token = value
	case "--cluster":
		g.cluster = value
	case "--timeout":
		if d, err := time.ParseDuration(value); err == nil {
			g.timeout = d
		}
	case "--scenarios-dir":
		g.scenariosDir = value
	case "--results-dir":
		g.resultsDir = value
	case "--json":
		g.json = value == "true" || value == "1" || value == ""
	default:
		return false
	}
	return true
}

// handleList lists every scenario in the manifest.
func handleList(args []string) {
	_, g := parseGlobalFlags(args)
	scenarios, err := LoadAllScenarios(g.scenariosDir)
	if err != nil {
		fail("load scenarios: %v", err)
	}
	if g.json {
		out, _ := json.MarshalIndent(scenarios, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("%-6s %-30s %-12s %-10s %-10s %s\n", "ID", "SLUG", "CATEGORY", "DIFFICULTY", "ORIGIN", "SHA256")
	for _, s := range scenarios {
		fmt.Printf("%-6s %-30s %-12s %-10s %-10s %s\n",
			s.ID, s.Slug, s.Category, s.Difficulty, s.Origin,
			shortHash(s.Manifest.GroundTruthSHA256))
	}
}

// handleValidate verifies the structural integrity of every scenario and
// re-checks the ground-truth SHA256 against the manifest.
func handleValidate(args []string) {
	_, g := parseGlobalFlags(args)
	scenarios, err := LoadAllScenarios(g.scenariosDir)
	if err != nil {
		fail("load scenarios: %v", err)
	}
	var failures int
	for _, s := range scenarios {
		if err := s.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL  %s  %v\n", s.ID, err)
			failures++
			continue
		}
		fmt.Printf("OK    %s  %s\n", s.ID, s.Slug)
	}
	if failures > 0 {
		os.Exit(1)
	}
}

// handleRun executes the scenarios and writes the report.
func handleRun(args []string) {
	positional, g := parseGlobalFlags(args)
	_ = positional
	scenarios, err := LoadAllScenarios(g.scenariosDir)
	if err != nil {
		fail("load scenarios: %v", err)
	}
	scenarios = filterScenarios(scenarios, g.scenarios)
	if len(scenarios) == 0 {
		fail("no scenarios matched")
	}

	client := NewClient(g.server, g.token, g.timeout)
	runner := &Runner{
		Sentinel:     client,
		Provisioner:  kindProvisioner{},
		Applier:      kubectlApplier{},
		DiffFetcher:  githubDiffFetcher{token: g.token},
		ScenariosDir: g.scenariosDir,
		ResultsDir:   g.resultsDir,
		PollInterval: 5 * time.Second,
	}

	opts := RunOptions{
		ScenarioIDs: g.scenarios,
		Timeout:     g.timeout,
		ClusterName: g.cluster,
		KeepCluster: g.cluster != "",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	report, err := runner.Run(ctx, opts)
	if err != nil {
		fail("run: %v", err)
	}
	if g.json {
		out, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(out))
		return
	}
	printReport(report)
}

// handleResults prints a historical results directory. The <version>
// positional arg selects the Sentinel version subdir; pass "latest" or
// omit it to list every version.
func handleResults(args []string) {
	positional, g := parseGlobalFlags(args)
	_ = g
	if len(positional) == 0 {
		versions, err := listResultVersions(g.resultsDir)
		if err != nil {
			fail("list results: %v", err)
		}
		for _, v := range versions {
			fmt.Println(v)
		}
		return
	}
	version := positional[0]
	dir := filepath.Join(g.resultsDir, version)
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail("read %s: %v", dir, err)
	}
	for _, e := range entries {
		jsonPath := filepath.Join(dir, e.Name(), "results.json")
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			continue
		}
		var r Report
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		fmt.Printf("%s  %s  total=%d  DBR=%.0f%% VPR=%.0f%% TAR=%.0f%% FMR=%.0f%%\n",
			version, e.Name(), r.Metrics.Total,
			r.Metrics.DBR*100, r.Metrics.VPR*100, r.Metrics.TAR*100, r.Metrics.FMR*100)
	}
}

// listResultVersions returns the names of the immediate subdirectories
// of resultsDir (one per Sentinel version).
func listResultVersions(resultsDir string) ([]string, error) {
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// printReport writes a human-readable summary of the report to stdout.
func printReport(r *Report) {
	fmt.Printf("sentinel-bench — Sentinel %s\n\n", r.SentinelVersion)
	fmt.Printf("Total: %d   DBR: %.0f%%   VPR: %.0f%%   TAR: %.0f%%   FMR: %.0f%%\n\n",
		r.Metrics.Total, r.Metrics.DBR*100, r.Metrics.VPR*100, r.Metrics.TAR*100, r.Metrics.FMR*100)
	fmt.Printf("%-6s %-30s %-10s %-8s %-6s %s\n", "ID", "SLUG", "DIFF", "STATUS", "SCORE", "PR")
	for _, s := range r.Scenarios {
		pr := "—"
		if s.PRURL != "" {
			pr = s.PRURL
		}
		fmt.Printf("%-6s %-30s %-10s %-8s %5.0f%%  %s\n",
			s.ScenarioID, s.Slug, s.Difficulty, s.Status, s.Score*100, pr)
	}
}

// fail prints an error to stderr and exits 1.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// getenv returns the env var or the default if not set.
func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}

// shortHash returns the first 12 chars of a hex hash, for table display.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// printHelp writes the sentinel-bench usage to stdout.
func printHelp() {
	fmt.Printf(`sentinel-bench %s — eval harness for Sentinel, the Autonomous AI SRE

Usage:
  sentinel-bench <command> [flags]

Commands:
  list                              List all scenarios from MANIFEST.yaml
  validate                          Verify structure + check ground-truth SHA256s
  run [--scenario S001] [--timeout 15m]
                                    Run Sentinel against scenarios
  results [version]                 Show historical results

Flags:
  --server URL                      Sentinel API URL (default: http://localhost:8000)
  --token T                         Bearer token for /admin/* (or SENTINEL_API_TOKEN env var)
  --cluster NAME                    Use an existing KinD cluster (skip create/delete)
  --scenario ID                     Run only the named scenario (repeatable)
  --timeout D                       Per-scenario wall-clock budget (default: 15m)
  --scenarios-dir PATH              Path to scenarios/ (default: ./sentinel-bench/scenarios)
  --results-dir PATH                Path to results/ (default: ./sentinel-bench/results)
  --json                            Output raw JSON instead of tables
  --help, -h                        Show this help
  --version, -V                     Show version

Environment:
  SENTINEL_SERVER                   Default API URL (same as --server)
  SENTINEL_API_TOKEN                Default bearer token (same as --token)

Examples:
  sentinel-bench list
  sentinel-bench validate
  sentinel-bench run --scenario S001 --scenario S002
  sentinel-bench run --cluster my-kind --timeout 30m
  sentinel-bench results
  sentinel-bench results dev

The runner is a standalone binary. It talks to Sentinel via HTTP only and
shells out to `+"`kind`"+` and `+"`kubectl`"+` for cluster lifecycle. It does
NOT run inside Kubernetes.
`, Version)
}

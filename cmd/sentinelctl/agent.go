// Package main — sentinelctl agent + codeintel subcommands.
//
// These subcommands run the agent loop and code-intelligence tools LOCALLY
// (no Sentinel server connection required). They are useful for:
//
//   - Development: test the agent loop against a cloned repo without standing
//     up the full Sentinel stack.
//   - Debugging: dump the symbol index, query callers/callees of a function,
//     see exactly what the agent would see.
//   - Demos: run a single investigation against a known-bad commit and show
//     the trace.
//
// Usage:
//
//	sentinelctl agent investigate <repo-path> [--logs <file>]
//	    [--service <name>] [--namespace <ns>] [--max-turns N]
//	    [--turn-timeout 30s]
//	sentinelctl agent codeintel <repo-path> [--max-files N] [--json]
//	sentinelctl agent symbols <repo-path> [--max-files N]
//	sentinelctl agent callers <repo-path> <name> [--max-files N]
//	sentinelctl agent callees <repo-path> <name> [--max-files N]
//	sentinelctl agent definition <repo-path> <name> [--max-files N]
//
// LLM credentials are read from the same env vars as the Sentinel server:
//
//	SENTINEL_LLM_API_KEY       (required for investigate)
//	SENTINEL_LLM_BASE_URL      (default: provider's public URL)
//	SENTINEL_LLM_PROVIDER      (default: "custom"; see goai_provider.go)
//	SENTINEL_LLM_FAST_MODEL    (default: provider default)
//	SENTINEL_LLM_FRONTIER_MODEL (default: provider default)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/agent"
	"github.com/karimzakzouk/sentinel/internal/codeintel"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// handleAgent dispatches the `sentinelctl agent <subcommand>` tree.
//
// Unlike most sentinelctl subcommands, agent subcommands do NOT use the HTTP
// client — they import the agent + codeintel packages directly and run
// locally. The --server / --token / --timeout global flags are accepted but
// ignored for this subcommand tree.
func (c *Client) handleAgent(args []string) {
	if len(args) == 0 {
		printAgentHelp()
		os.Exit(0)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "investigate", "inv":
		handleAgentInvestigate(rest)
	case "codeintel", "ci":
		c.handleAgentCodeIntel(rest)
	case "symbols", "sym":
		handleAgentSymbols(rest)
	case "callers":
		handleAgentCallers(rest)
	case "callees":
		handleAgentCallees(rest)
	case "definition", "def":
		handleAgentDefinition(rest)
	case "help", "h", "--help", "-h":
		printAgentHelp()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown agent subcommand: %s\n\n", sub)
		printAgentHelp()
		os.Exit(1)
	}
}

// handleAgentInvestigate runs the agent loop against a pre-cloned repo.
//
// The user supplies the repo path (already on disk) and optionally a file
// containing pod logs. The command constructs an LLM router from env vars,
// builds a fresh agent.Investigator, and runs the loop. The result is
// printed as indented JSON to stdout so it can be piped into jq or
// redirected to a file.
//
// This is the fastest way to test a code change to the agent loop end-to-end
// without standing up K8s + GitHub + Slack.
func handleAgentInvestigate(args []string) {
	fs := flag.NewFlagSet("agent investigate", flag.ExitOnError)
	logsFile := fs.String("logs", "", "path to a file containing pod logs (default: read from stdin)")
	service := fs.String("service", "demo-app", "service name for the agent's system prompt")
	namespace := fs.String("namespace", "default", "namespace for the agent's system prompt")
	maxTurns := fs.Int("max-turns", agent.DefaultMaxTurns, "maximum agent loop turns")
	turnTimeout := fs.Duration("turn-timeout", agent.DefaultTurnTimeout, "per-turn LLM timeout")
	compactThreshold := fs.Int("compact-threshold", agent.DefaultCompactThreshold, "conversation length that triggers compaction")
	logLevel := fs.String("log-level", "info", "log level (debug, info, warn, error)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent investigate <repo-path> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Runs the agent loop locally against a pre-cloned repo. No Sentinel server needed.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Environment:")
		fmt.Fprintln(os.Stderr, "  SENTINEL_LLM_API_KEY        required (LLM API key)")
		fmt.Fprintln(os.Stderr, "  SENTINEL_LLM_BASE_URL       LLM API base URL")
		fmt.Fprintln(os.Stderr, "  SENTINEL_LLM_PROVIDER       provider type (default: custom)")
		fmt.Fprintln(os.Stderr, "  SENTINEL_LLM_FAST_MODEL     fast model name")
		fmt.Fprintln(os.Stderr, "  SENTINEL_LLM_FRONTIER_MODEL frontier model name")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	if _, err := os.Stat(repoPath); err != nil {
		fail(fmt.Errorf("repo path: %w", err))
	}

	logging.Init(*logLevel)

	// Read pod logs from --logs file or stdin.
	var podLogs string
	if *logsFile != "" {
		data, err := os.ReadFile(*logsFile)
		if err != nil {
			fail(fmt.Errorf("read --logs file: %w", err))
		}
		podLogs = string(data)
	} else {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			// Stdin is piped.
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fail(fmt.Errorf("read stdin: %w", err))
			}
			podLogs = string(data)
		}
	}

	// Construct the LLM router from env vars.
	apiKey := os.Getenv("SENTINEL_LLM_API_KEY")
	if apiKey == "" {
		fail(fmt.Errorf("SENTINEL_LLM_API_KEY is required (set it to your LLM provider API key)"))
	}
	baseURL := getenv("SENTINEL_LLM_BASE_URL", "")
	provider := getenv("SENTINEL_LLM_PROVIDER", "custom")
	fastModel := getenv("SENTINEL_LLM_FAST_MODEL", "")
	frontierModel := getenv("SENTINEL_LLM_FRONTIER_MODEL", "")
	router := llm.NewWithProvider(provider, baseURL, apiKey, fastModel, frontierModel)

	// Build a minimal AgentState with the pod logs attached.
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			ClusterID: uuid.New(),
			Service:   *service,
			Namespace: *namespace,
			Summary:   fmt.Sprintf("Manual investigation via sentinelctl agent investigate (repo: %s)", filepath.Base(repoPath)),
			Anomalies: []schemas.Anomaly{
				{
					AnomalyID:   uuid.New(),
					SignalID:    uuid.New(),
					Service:     *service,
					Namespace:   *namespace,
					Detector:    "manual",
					Explanation: podLogs,
					Annotations: map[string]string{
						"sentinel.io/repo":   filepath.Base(repoPath),
						"sentinel.io/branch": "main",
					},
				},
			},
		},
	}

	cfg := agent.AgentConfig{
		MaxTurns:         *maxTurns,
		TurnTimeout:      *turnTimeout,
		CompactThreshold: *compactThreshold,
	}
	inv := agent.NewInvestigator(router, repoPath, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*maxTurns)*(*turnTimeout)+30*time.Second)
	defer cancel()

	fmt.Fprintln(os.Stderr, "starting agent loop...",
		"repo", repoPath,
		"service", *service,
		"max_turns", *maxTurns,
		"logs_bytes", len(podLogs))

	if err := inv.Run(ctx, state); err != nil {
		fail(fmt.Errorf("agent loop failed: %w", err))
	}

	result := struct {
		RootCause   *schemas.RootCause     `json:"root_cause,omitempty"`
		Hypotheses  []schemas.Hypothesis   `json:"hypotheses"`
		SourceFiles []string               `json:"source_files"`
		Status      schemas.IncidentStatus `json:"status"`
		Trace       []map[string]any       `json:"trace"`
	}{
		RootCause:   state.RootCause,
		Hypotheses:  state.Hypotheses,
		SourceFiles: state.SourceFiles,
		Status:      state.Status,
		Trace:       state.AgentTrace,
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail(fmt.Errorf("marshal result: %w", err))
	}
	fmt.Println(string(out))
}

// handleAgentCodeIntel builds the code-intel index for a repo and dumps it
// as JSON (symbols + call graph). Useful for inspecting what the agent would
// see when investigating that repo.
//
// The --json global flag toggles between a human-readable summary (default)
// and raw JSON output.
func (c *Client) handleAgentCodeIntel(args []string) {
	fs := flag.NewFlagSet("agent codeintel", flag.ExitOnError)
	maxFiles := fs.Int("max-files", 500, "maximum number of source files to parse")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent codeintel <repo-path> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Builds the code intelligence index (symbols + call graph) and prints it.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "  --json                  Output raw JSON (global flag)")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	if _, err := os.Stat(repoPath); err != nil {
		fail(fmt.Errorf("repo path: %w", err))
	}

	idx, err := codeintel.BuildIndex(repoPath, *maxFiles)
	if err != nil {
		fail(fmt.Errorf("build index: %w", err))
	}

	if c.jsonOut {
		out, _ := json.MarshalIndent(idx, "", "  ")
		fmt.Println(string(out))
		return
	}

	fmt.Printf("Code Intelligence Index for %s\n", repoPath)
	fmt.Printf("  Files parsed: %d\n", len(idx.CallGraph.Files))
	fmt.Printf("  Symbols:      %d\n", len(idx.Symbols))
	fmt.Printf("  Unique names: %d\n", len(idx.CallGraph.Definitions))
	fmt.Printf("  Call sites:   %d\n", countCallSites(idx))
	fmt.Println()
	fmt.Print(idx.FormatSymbolList())
}

// handleAgentSymbols prints just the formatted symbol list (no call graph).
// Equivalent to `agent codeintel` without the call-graph section — useful
// when piping into less / grep.
func handleAgentSymbols(args []string) {
	fs := flag.NewFlagSet("agent symbols", flag.ExitOnError)
	maxFiles := fs.Int("max-files", 500, "maximum number of source files to parse")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent symbols <repo-path> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Prints the list of symbols (functions, methods, classes, types) found in the repo.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	idx, err := codeintel.BuildIndex(repoPath, *maxFiles)
	if err != nil {
		fail(fmt.Errorf("build index: %w", err))
	}
	fmt.Print(idx.FormatSymbolList())
}

// handleAgentCallers prints all call sites of the named symbol.
func handleAgentCallers(args []string) {
	fs := flag.NewFlagSet("agent callers", flag.ExitOnError)
	maxFiles := fs.Int("max-files", 500, "maximum number of source files to parse")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent callers <repo-path> <name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Prints all call sites of the named function/method/class in the repo.")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	name := fs.Arg(1)
	idx, err := codeintel.BuildIndex(repoPath, *maxFiles)
	if err != nil {
		fail(fmt.Errorf("build index: %w", err))
	}
	fmt.Print(idx.FormatCallers(name))
}

// handleAgentCallees prints all functions called by the named symbol.
func handleAgentCallees(args []string) {
	fs := flag.NewFlagSet("agent callees", flag.ExitOnError)
	maxFiles := fs.Int("max-files", 500, "maximum number of source files to parse")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent callees <repo-path> <name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Prints all functions called by the named function in the repo.")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	name := fs.Arg(1)
	idx, err := codeintel.BuildIndex(repoPath, *maxFiles)
	if err != nil {
		fail(fmt.Errorf("build index: %w", err))
	}
	callees := idx.FindCallees(name)
	if len(callees) == 0 {
		fmt.Printf("No callees found for %q\n", name)
		return
	}
	fmt.Printf("%d callee(s) of %q:\n", len(callees), name)
	for _, c := range callees {
		fmt.Printf("  %s:%d  calls %s\n", c.CallFile, c.CallLine, c.Callee)
	}
}

// handleAgentDefinition prints the definition site(s) of the named symbol.
func handleAgentDefinition(args []string) {
	fs := flag.NewFlagSet("agent definition", flag.ExitOnError)
	maxFiles := fs.Int("max-files", 500, "maximum number of source files to parse")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl agent definition <repo-path> <name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Prints the definition site(s) of the named symbol (function, method, class, type).")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}
	repoPath := fs.Arg(0)
	name := fs.Arg(1)
	idx, err := codeintel.BuildIndex(repoPath, *maxFiles)
	if err != nil {
		fail(fmt.Errorf("build index: %w", err))
	}
	defs := idx.FindDefinition(name)
	if len(defs) == 0 {
		fmt.Printf("No definition found for %q\n", name)
		return
	}
	fmt.Printf("%d definition(s) of %q:\n", len(defs), name)
	for _, d := range defs {
		switch d.Kind {
		case codeintel.KindMethod:
			fmt.Printf("  %s:%d  method %s(%s) on %s\n", d.File, d.Line, d.Name, d.Params, d.Receiver)
		case codeintel.KindFunction:
			fmt.Printf("  %s:%d  func %s(%s)\n", d.File, d.Line, d.Name, d.Params)
		case codeintel.KindClass:
			fmt.Printf("  %s:%d  class %s\n", d.File, d.Line, d.Name)
		case codeintel.KindType:
			fmt.Printf("  %s:%d  type %s\n", d.File, d.Line, d.Name)
		default:
			fmt.Printf("  %s:%d  %s %s\n", d.File, d.Line, d.Kind, d.Name)
		}
	}
}

// countCallSites returns the total number of call edges in the index.
func countCallSites(idx *codeintel.Index) int {
	n := 0
	for _, edges := range idx.CallGraph.Callers {
		n += len(edges)
	}
	return n
}

// printAgentHelp writes the `sentinelctl agent` subcommand help.
func printAgentHelp() {
	fmt.Println(`sentinelctl agent — run the agent loop + code intelligence tools locally

Usage:
  sentinelctl agent investigate <repo-path> [--logs <file>]
      Run the N-turn agent loop against a pre-cloned repo. Pod logs are read
      from --logs (or stdin if omitted). LLM credentials come from the same
      env vars as the Sentinel server (SENTINEL_LLM_API_KEY etc.).

  sentinelctl agent codeintel <repo-path> [--max-files N] [--json]
      Build the code intelligence index (symbols + call graph) and print it.

  sentinelctl agent symbols <repo-path> [--max-files N]
      Print just the symbol list (functions, methods, classes, types).

  sentinelctl agent callers <repo-path> <name> [--max-files N]
      Print all call sites of the named symbol.

  sentinelctl agent callees <repo-path> <name> [--max-files N]
      Print all functions called by the named symbol.

  sentinelctl agent definition <repo-path> <name> [--max-files N]
      Print the definition site(s) of the named symbol.

These subcommands run LOCALLY — they do not connect to a Sentinel server.
The --server / --token / --timeout global flags are accepted but ignored.

Examples:
  sentinelctl agent symbols ./my-repo
  sentinelctl agent callers ./my-repo HandleRequest
  sentinelctl agent investigate ./my-repo --logs ./crash.log --service api
  kubectl logs my-pod | sentinelctl agent investigate ./my-repo --service api`)
}

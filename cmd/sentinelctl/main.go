// Package main is the sentinelctl CLI — a thin HTTP client for Sentinel's API.
//
// Usage:
//
//	sentinelctl incidents list [--status X] [--service Y] [--last 7d]
//	sentinelctl incidents get <id>
//	sentinelctl trigger <service> --namespace sim [--reason "..."]
//	sentinelctl pause [--duration 60m] [--reason "..."]
//	sentinelctl resume
//	sentinelctl stats
//	sentinelctl config view
//	sentinelctl config set <json-file-or-inline>
//	sentinelctl metrics
//	sentinelctl app setup
//	sentinelctl app status
//
// Flags:
//
//	--server URL    Sentinel API URL (default: http://localhost:8000)
//	--token T       Bearer token for admin endpoints (or SENTINEL_API_TOKEN env var)
//	--json          Output raw JSON instead of tables
//	--timeout D     HTTP timeout (default: 30s)
//
// Global flags work anywhere in the args (kubectl-style). Meta flags
// (--help, -h, --version, -V) are recognized before any command dispatch so
// `sentinelctl trigger --help` prints help instead of triggering an
// investigation with service="--help".
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/version"
)

// Version is the sentinelctl build version. Sourced from internal/version
// (set via -ldflags from the VERSION file at build time).
var Version = version.Version

func main() {
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
			fmt.Printf("sentinelctl %s\n", Version)
			os.Exit(0)
		}
	}

	server := getenv("SENTINEL_SERVER", "http://localhost:8000")
	token := getenv("SENTINEL_API_TOKEN", "")
	jsonOut := false
	timeout := 30 * time.Second

	cmd, rawArgs := findSubcommand(os.Args[1:])
	if cmd == "" {
		printHelp()
		os.Exit(0)
	}

	filtered := make([]string, 0, len(rawArgs))
	subcmdRemoved := false
	for _, a := range rawArgs {
		if !subcmdRemoved && a == cmd {
			subcmdRemoved = true
			continue
		}
		filtered = append(filtered, a)
	}
	positional, server, token, jsonOut, timeout := extractGlobalFlags(filtered, server, token, jsonOut, timeout)
	server = strings.TrimRight(server, "/")

	c := NewClient(server, token, jsonOut, timeout)

	switch cmd {
	case "incidents", "incident", "i":
		c.handleIncidents(positional)
	case "trigger", "t":
		c.handleTrigger(positional)
	case "pause":
		c.handlePause(positional)
	case "resume":
		c.handleResume()
	case "stats", "s":
		c.handleStats()
	case "config", "cfg":
		c.handleConfig(positional)
	case "metrics", "m":
		c.handleMetrics()
	case "app":
		c.handleApp(positional)
	case "agent":
		c.handleAgent(positional)
	case "version", "v":
		fmt.Printf("sentinelctl %s\n", Version)
	case "help", "h", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

// findSubcommand scans args for the first non-flag token, skipping the
// values of recognized global flags (--server, --token, --timeout take a
// value; --json does not). Returns the subcommand name and the full arg
// slice so global flags can be harvested separately by extractGlobalFlags.
func findSubcommand(args []string) (cmd string, allArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1], args
			}
			return "", args
		}
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
			continue
		}
		switch a {
		case "--server", "--token", "--timeout":
			i++
			continue
		case "--json":
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args
	}
	return "", args
}

// extractGlobalFlags scans args for recognized global flags and removes them,
// returning the positional remainder. Supports both --flag value and
// --flag=value forms. Unknown flags are left in place for the subcommand
// handler to interpret.
func extractGlobalFlags(args []string, server, token string, jsonOut bool, timeout time.Duration) ([]string, string, string, bool, time.Duration) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
			parts := strings.SplitN(a, "=", 2)
			name, value := parts[0], parts[1]
			switch name {
			case "--server":
				server = value
				continue
			case "--token":
				token = value
				continue
			case "--timeout":
				if d, err := time.ParseDuration(value); err == nil {
					timeout = d
				}
				continue
			case "--json":
				jsonOut = value == "true" || value == "1" || value == ""
				continue
			}
		}
		switch a {
		case "--server":
			if i+1 < len(args) {
				server = args[i+1]
				i++
				continue
			}
		case "--token":
			if i+1 < len(args) {
				token = args[i+1]
				i++
				continue
			}
		case "--timeout":
			if i+1 < len(args) {
				if d, err := time.ParseDuration(args[i+1]); err == nil {
					timeout = d
				}
				i++
				continue
			}
		case "--json":
			jsonOut = true
			continue
		}
		out = append(out, a)
	}
	return out, server, token, jsonOut, timeout
}

// printHelp writes the sentinelctl usage to stdout.
func printHelp() {
	fmt.Printf(`sentinelctl %s — CLI for Sentinel, the Autonomous AI SRE

Usage:
  sentinelctl <command> [flags]

Commands:
  incidents list          List recent incidents (--status, --service, --last, --limit)
  incidents get <id>      Show incident details
  trigger <service>       Manually trigger an investigation
  pause [--duration N]    Pause autonomous mode (default: 30 min)
  resume                  Resume autonomous mode
  stats                   Show incident statistics
  config view             Show current configuration
  config set <json|file>  Update runtime configuration
  metrics                 Show Prometheus metrics
  app setup               Create the GitHub App (manifest flow)
  app set-install <id>    Set the GitHub App install ID
  app status              Show whether the GitHub App is configured
  app remove              Remove the GitHub App config (fall back to PAT)
  agent investigate       Run the agent loop locally against a pre-cloned repo
  agent codeintel         Build + dump the code intelligence index for a repo
  agent symbols           List symbols (functions, classes, types) in a repo
  agent callers           Find call sites of a symbol in a repo
  agent callees           Find functions called by a symbol in a repo
  agent definition        Find definition site(s) of a symbol in a repo
  version                 Show version
  help                    Show this help

Flags:
  --server URL            Sentinel API URL (default: http://localhost:8000)
  --token T               Bearer token for admin endpoints
  --json                  Output raw JSON instead of tables
  --timeout D             HTTP timeout (default: 30s)
  --namespace NS          Namespace (for trigger command)
  --help, -h              Show this help (works anywhere in args)
  --version, -V           Show version (works anywhere in args)

Environment:
  SENTINEL_SERVER         Default API URL (same as --server)
  SENTINEL_API_TOKEN      Default bearer token (same as --token)

Examples:
  sentinelctl incidents list
  sentinelctl incidents list --status investigating --last 7d
  sentinelctl incidents get 91cce5a5
  sentinelctl trigger demo-app --namespace sim --reason "manual check"
  sentinelctl pause --duration 60
  sentinelctl stats
  sentinelctl config view --json
  sentinelctl config set '{"llm_fast_model":"glm-4-flash"}'
  sentinelctl config set ./my-settings.json
  sentinelctl app setup

Auth:
  When the Sentinel server has SENTINEL_API_TOKEN set, all admin commands
  (trigger, pause, resume, config set, app setup, app set-install, app remove)
  require a matching bearer token. Pass it via --token or the
  SENTINEL_API_TOKEN env var. Read-only commands (incidents, stats,
  config view, metrics, app status) work without a token.
`, Version)
}

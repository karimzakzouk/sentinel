// Package main is the sentinelctl CLI entry point.
//
// Phase 1 placeholder. Full CLI commands (incidents, trigger, pause, resume,
// stats, config, metrics, app) are built in Phase 11.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/karimzakzouk/sentinel/internal/version"
)

var Version = version.Version

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("sentinelctl", Version)
		return
	}

	if flag.NArg() == 0 {
		usage()
		os.Exit(1)
	}

	// Phase 1 placeholder
	fmt.Fprintf(os.Stderr, "sentinelctl: command %q not yet implemented (phase 1)\n", flag.Arg(0))
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, `sentinelctl — Sentinel CLI

Usage:
  sentinelctl <command> [flags]

Commands:
  incidents list       List incidents (--status, --service, --last)
  incidents get <id>   Show a single incident
  trigger <service>    Manually trigger an investigation
  pause                Pause autonomous mode (--duration)
  resume               Resume autonomous mode
  stats                Show incident statistics
  config view          Show current config
  config set <json>    Update runtime config
  metrics              Show Prometheus metrics
  app setup            GitHub App manifest flow
  app status           Show GitHub App status

Flags:
  --server URL         Sentinel API URL (default: http://localhost:8000)
  --token T            Admin API bearer token
  --json               Raw JSON output
  --version            Print version and exit

Phase 1 placeholder — full implementation arrives in Phase 11.`)
}

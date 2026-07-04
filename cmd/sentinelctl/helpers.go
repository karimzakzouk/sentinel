// Package main — small helpers for sentinelctl.
package main

import (
	"fmt"
	"os"

	"github.com/karimzakzouk/sentinel/internal/version"
)

func str(m map[string]any, key string) string {
        if m == nil {
                return ""
        }
        v, ok := m[key]
        if !ok {
                return ""
        }
        switch val := v.(type) {
        case string:
                return val
        case float64:
                return fmt.Sprintf("%.0f", val)
        default:
                return fmt.Sprintf("%v", val)
        }
}

func getenv(key, def string) string {
        if v := os.Getenv(key); v != "" {
                return v
        }
        return def
}

func fail(err error) {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
}

func printHelp() {
        fmt.Printf(`sentinelctl %s — CLI for Sentinel, the Autonomous AI SRE

Usage:
  sentinelctl <command> [flags]

Commands:
  incidents list          List recent incidents
  incidents get <id>      Show incident details
  trigger <service>       Manually trigger an investigation
  pause [--duration N]    Pause autonomous mode (default: 30 min)
  resume                  Resume autonomous mode
  stats                   Show incident statistics
  config view             Show current configuration
  config set <json|file>  Update runtime configuration (v3.8.4)
  metrics                 Show Prometheus metrics
  version                 Show version
  help                    Show this help

Flags:
  --server URL            Sentinel API URL (default: http://localhost:8000)
  --token T               Bearer token for admin endpoints (v3.8.4)
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
  sentinelctl incidents get 91cce5a5
  sentinelctl trigger demo-app --namespace sim
  sentinelctl pause --duration 60
  sentinelctl stats
  sentinelctl config view --json
  sentinelctl config set '{"llm_fast_model":"glm-4-flash"}' --token "$SENTINEL_API_TOKEN"
  sentinelctl config set ./my-settings.json

Auth (v3.8.4):
  When the Sentinel server has SENTINEL_API_TOKEN set, all admin commands
  (trigger, pause, resume, config set) require a matching bearer token.
  Pass it via --token or the SENTINEL_API_TOKEN env var. Read-only
  commands (incidents, stats, config view, metrics) work without a token.
`, version.Version)
}

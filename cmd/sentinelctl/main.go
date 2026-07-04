// Package main is the sentinelctl CLI — a thin HTTP client for Sentinel's API.
// Usage:
//      sentinelctl incidents list [--status X] [--service Y] [--last 7d]
//      sentinelctl incidents get <id>
//      sentinelctl trigger <service> --namespace sim
//      sentinelctl pause [--duration 60m]
//      sentinelctl resume
//      sentinelctl stats
//      sentinelctl config view
//      sentinelctl config set <json-file-or-inline>
//      sentinelctl metrics
// Flags:
//      --server URL    Sentinel API URL (default: http://localhost:8000)
//      --token T       Bearer token for admin endpoints (or SENTINEL_API_TOKEN env var)
//      --json          Output raw JSON instead of tables
//      --timeout D     HTTP timeout (default: 30s)
// The server now 401s /admin/* and POST /settings when SENTINEL_API_TOKEN
// is set; without --token, those commands fail with a clear error message.
// Also added HTTP timeouts (was none — a hung Sentinel pod hung the CLI
// forever), fixed flag parsing so global flags work anywhere (was
// broken when flags came after positional args, due to Go's flag package
// stopping at the first non-flag arg), added `config set` to drive the
// now-real Settings page, and wired the version through internal/version
// so it stays in sync with the VERSION file.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/version"
)

func main() {
        if len(os.Args) < 2 {
                printHelp()
                os.Exit(0)
        }

        // doing anything else. These are "meta flags" that should work no matter
        // where they appear — `sentinelctl trigger --help` should print help,
        // not trigger an investigation with service="--help" (which is what
        // happened before this fix — dangerous).

        for _, a := range os.Args[1:] {
                switch a {
                case "--help", "-h", "help":
                        printHelp()
                        os.Exit(0)
                case "--version", "-V":
                        fmt.Printf("sentinelctl %s\n", version.Version)
                        os.Exit(0)
                }
        }

        // Global flags with defaults from env vars.

        server := getenv("SENTINEL_SERVER", "http://localhost:8000")
        token := getenv("SENTINEL_API_TOKEN", "")
        jsonOut := false
        timeout := 30 * time.Second

        // values so `sentinelctl --server URL trigger demo-app` finds `trigger`,
        // not `URL`. Then harvest global flags from everywhere (before and
        // after the subcommand) so users get kubectl-style flag-anywhere UX.

        cmd, rawArgs := findSubcommand(os.Args[1:])
        if cmd == "" {
                printHelp()
                os.Exit(0)
        }

        // Remove the subcommand token (first occurrence) from the args so the
        // subcommand handler doesn't see it as a positional arg.

        filtered := make([]string, 0, len(rawArgs))
        subcmdRemoved := false
        for _, a := range rawArgs {
                if !subcmdRemoved && a == cmd {
                        subcmdRemoved = true
                        continue
                }
                filtered = append(filtered, a)
        }
        // Extract global flags from the remaining args. Returns the positional
        // args + subcommand-specific flags.

        positional, server, token, jsonOut, timeout := extractGlobalFlags(filtered, server, token, jsonOut, timeout)

        // double-slash URLs like http://host//incidents. Most servers handle
        // it but it's sloppy and some strict proxies reject it.

        server = strings.TrimRight(server, "/")

        c := &client{
                baseURL: server,
                token:   token,
                jsonOut: jsonOut,
                http:    &http.Client{Timeout: timeout},
        }

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
        case "version", "v":
                fmt.Printf("sentinelctl %s\n", version.Version)
        case "help", "h", "--help", "-h":
                printHelp()
        default:
                fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
                printHelp()
                os.Exit(1)
        }
}

// findSubcommand scans args for the first non-flag token, skipping the
// values of recognized global flags (--server, --token, --timeout take a
// value; --json does not). Returns the subcommand name and the full arg
// slice (including flags before the subcommand) so global flags can be
// harvested separately.
// treat `URL` as the subcommand because Go's flag package stops at the
// first non-flag arg. We do our own scan to recognize the value-consuming
// flags and skip their values.

func findSubcommand(args []string) (cmd string, allArgs []string) {
        for i := 0; i < len(args); i++ {
                a := args[i]
                if a == "--" {
                        // Everything after -- is positional. The first one is the subcommand.

                        if i+1 < len(args) {
                                return args[i+1], args
                        }
                        return "", args
                }
                // --flag=value form — single token, skip.

                if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
                        continue
                }
                switch a {
                case "--server", "--token", "--timeout":
                        // Consumes the next arg as a value.

                        i++
                        continue
                case "--json":
                        continue
                }
                if strings.HasPrefix(a, "-") {
                        // Unknown flag — skip (assume no value).

                        continue
                }
                return a, args
        }
        return "", args
}

// extractGlobalFlags scans args for recognized global flags and removes
// them, returning the positional remainder. Supports both --flag value
// and --flag=value forms. Unknown flags are left in place for the
// subcommand handler.

func extractGlobalFlags(args []string, server, token string, jsonOut bool, timeout time.Duration) ([]string, string, string, bool, time.Duration) {
        out := make([]string, 0, len(args))
        for i := 0; i < len(args); i++ {
                a := args[i]

                // --flag=value form

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

                // --flag value form

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

// client wraps the HTTP calls to Sentinel's API.

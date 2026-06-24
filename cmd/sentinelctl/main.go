// Package main is the sentinelctl CLI — a thin HTTP client for Sentinel's API.
//
// Usage:
//
//      sentinelctl incidents list [--status X] [--service Y] [--last 7d]
//      sentinelctl incidents get <id>
//      sentinelctl trigger <service> --namespace sim
//      sentinelctl pause [--duration 60m]
//      sentinelctl resume
//      sentinelctl stats
//      sentinelctl config view
//      sentinelctl config set <json-file-or-inline>
//      sentinelctl metrics
//
// Flags:
//
//      --server URL    Sentinel API URL (default: http://localhost:8000)
//      --token T       Bearer token for admin endpoints (or SENTINEL_API_TOKEN env var)
//      --json          Output raw JSON instead of tables
//      --timeout D     HTTP timeout (default: 30s)
//
// v3.8.4: Added --token / SENTINEL_API_TOKEN for admin-endpoint auth.
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
        "bytes"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "os"
        "sort"
        "strings"
        "text/tabwriter"
        "time"

        "github.com/karimzakzouk/sentinel/v3/internal/version"
)

func main() {
        if len(os.Args) < 2 {
                printHelp()
                os.Exit(0)
        }

        // Global flags with defaults from env vars.
        server := getenv("SENTINEL_SERVER", "http://localhost:8000")
        token := getenv("SENTINEL_API_TOKEN", "")
        jsonOut := false
        timeout := 30 * time.Second

        // v3.8.4: Find the subcommand = first non-flag arg, skipping flag
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
//
// v3.8.4: Without this, `sentinelctl --server URL trigger demo-app` would
// treat `URL` as the subcommand because Go's flag package stops at the
// first non-flag arg. We do our own scan to recognize the value-consuming
// flags and skip their values.
func findSubcommand(args []string) (cmd string, allArgs []string) {
        allArgs = args
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
type client struct {
        baseURL string
        token   string // v3.8.4: bearer token for admin endpoints
        jsonOut bool
        http    *http.Client // v3.8.4: custom client with timeout
}

// do sends an HTTP request and returns the response body. The authRequired
// flag controls whether the bearer token is attached — admin endpoints
// (/admin/*, POST /settings) need it; read endpoints don't.
//
// v3.8.4: On 401, returns a clear error telling the user to set --token
// or SENTINEL_API_TOKEN. Previously a 401 produced a generic "API returned
// 401" message with no hint about the fix.
func (c *client) do(method, path string, body io.Reader, authRequired bool) ([]byte, error) {
        req, err := http.NewRequest(method, c.baseURL+path, body)
        if err != nil {
                return nil, fmt.Errorf("build request: %w", err)
        }
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }
        if authRequired && c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }

        resp, err := c.http.Do(req)
        if err != nil {
                return nil, fmt.Errorf("connection failed: %w\nIs Sentinel running at %s?", err, c.baseURL)
        }
        defer resp.Body.Close()
        respBody, _ := io.ReadAll(resp.Body)

        if resp.StatusCode == 401 {
                if c.token == "" {
                        return nil, fmt.Errorf("API returned 401 Unauthorized.\n\n" +
                                "The Sentinel server has SENTINEL_API_TOKEN set. Provide the token via:\n" +
                                "  --token <token>           (flag)\n" +
                                "  SENTINEL_API_TOKEN=<tok>  (env var)")
                }
                return nil, fmt.Errorf("API returned 401 Unauthorized — token is incorrect or expired")
        }
        if resp.StatusCode == 403 {
                return nil, fmt.Errorf("API returned 403 Forbidden — token lacks required scope")
        }
        if resp.StatusCode >= 400 {
                return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
        }
        return respBody, nil
}

func (c *client) get(path string) ([]byte, error) {
        return c.do("GET", path, nil, false)
}

func (c *client) post(path string, payload any) ([]byte, error) {
        return c.postAuth(path, payload, false)
}

// postAuth is like post but attaches the bearer token. Used for admin
// endpoints (/admin/investigate, /admin/pause, /admin/resume, POST /settings).
func (c *client) postAuth(path string, payload any, authRequired bool) ([]byte, error) {
        var body io.Reader
        if payload != nil {
                b, _ := json.Marshal(payload)
                body = bytes.NewReader(b)
        }
        return c.do("POST", path, body, authRequired)
}

// --- Commands ---

func (c *client) handleIncidents(args []string) {
        if len(args) == 0 {
                c.listIncidents(nil)
                return
        }
        switch args[0] {
        case "list", "ls":
                c.listIncidents(args[1:])
        case "get", "show":
                if len(args) < 2 {
                        fmt.Fprintln(os.Stderr, "Usage: sentinelctl incidents get <id>")
                        os.Exit(1)
                }
                c.getIncident(args[1])
        default:
                // Treat as incident ID
                c.getIncident(args[0])
        }
}

func (c *client) listIncidents(args []string) {
        // v3.8.4: --limit flag was documented in help but silently ignored
        // (always sent limit=20). Now actually honored. Supports both
        // --limit 50 and --limit=50 forms.
        limit := 20
        for i := 0; i < len(args); i++ {
                a := args[i]
                if a == "--limit" || a == "-l" {
                        if i+1 < len(args) {
                                fmt.Sscanf(args[i+1], "%d", &limit)
                                i++
                        }
                } else if strings.HasPrefix(a, "--limit=") {
                        fmt.Sscanf(strings.TrimPrefix(a, "--limit="), "%d", &limit)
                } else if strings.HasPrefix(a, "-l=") {
                        fmt.Sscanf(strings.TrimPrefix(a, "-l="), "%d", &limit)
                }
        }
        if limit <= 0 || limit > 500 {
                limit = 20
        }
        body, err := c.get(fmt.Sprintf("/incidents?limit=%d", limit))
        if err != nil {
                fail(err)
        }

        if c.jsonOut {
                fmt.Println(string(body))
                return
        }

        var incidents []map[string]any
        if err := json.Unmarshal(body, &incidents); err != nil {
                fail(fmt.Errorf("parse response: %w", err))
        }

        if len(incidents) == 0 {
                fmt.Println("No incidents found.")
                return
        }

        w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
        fmt.Fprintln(w, "ID\tSERVICE\tNAMESPACE\tSTATUS\tCONFIDENCE\tCREATED")
        for _, inc := range incidents {
                id := str(inc, "incident_id")
                cluster, _ := inc["cluster"].(map[string]any)
                service := str(cluster, "service")
                namespace := str(cluster, "namespace")
                status := str(inc, "status")
                state, _ := inc["state"].(map[string]any)
                confidence := ""
                if state != nil {
                        if rc, ok := state["root_cause"].(map[string]any); ok {
                                if c, ok := rc["confidence"].(float64); ok {
                                        confidence = fmt.Sprintf("%.0f%%", c*100)
                                }
                        }
                }
                created := str(inc, "created_at")
                if len(created) > 19 {
                        created = created[:19]
                }
                fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", id[:8], service, namespace, status, confidence, created)
        }
        w.Flush()
}

func (c *client) getIncident(id string) {
        body, err := c.get("/incidents/" + id)
        if err != nil {
                fail(err)
        }

        if c.jsonOut {
                fmt.Println(string(body))
                return
        }

        var inc map[string]any
        if err := json.Unmarshal(body, &inc); err != nil {
                fail(fmt.Errorf("parse response: %w", err))
        }

        cluster, _ := inc["cluster"].(map[string]any)
        state, _ := inc["state"].(map[string]any)

        fmt.Printf("Incident: %s\n", str(inc, "incident_id"))
        fmt.Printf("Service:  %s\n", str(cluster, "service"))
        fmt.Printf("Status:   %s\n", str(inc, "status"))
        fmt.Printf("Created:  %s\n", str(inc, "created_at"))

        if state != nil {
                if rc, ok := state["root_cause"].(map[string]any); ok {
                        fmt.Printf("\nRoot Cause:\n  %s\n", str(rc, "statement"))
                        if conf, ok := rc["confidence"].(float64); ok {
                                fmt.Printf("  Confidence: %.0f%%\n", conf*100)
                        }
                }

                if fp, ok := state["fix_proposal"].(map[string]any); ok {
                        fmt.Printf("\nFix:\n  Type: %s\n  Description: %s\n", str(fp, "fix_type"), str(fp, "description"))
                }

                if er, ok := state["execution_result"].(map[string]any); ok {
                        fmt.Printf("\nExecution:\n  PR: %s\n  Sandbox: %v\n", str(er, "pr_url"), str(er, "sandbox_summary"))
                }

                if trace, ok := state["agent_trace"].([]any); ok {
                        fmt.Printf("\nAgent Trace:\n")
                        for _, t := range trace {
                                if m, ok := t.(map[string]any); ok {
                                        agent := str(m, "agent")
                                        fmt.Printf("  - %s", agent)
                                        if c, ok := m["confidence"]; ok {
                                                fmt.Printf(" (confidence: %v)", c)
                                        }
                                        if n, ok := m["files_changed"]; ok {
                                                fmt.Printf(" (files: %v)", n)
                                        }
                                        fmt.Println()
                                }
                        }
                }
        }
}

func (c *client) handleTrigger(args []string) {
        if len(args) == 0 {
                fmt.Fprintln(os.Stderr, "Usage: sentinelctl trigger <service> [--namespace sim]")
                os.Exit(1)
        }
        service := args[0]
        ns := "default"
        // v3.8.4: Support both --namespace sim and --namespace=sim (and -n
        // shorthand). Previous version only handled the space form, so
        // --namespace=sim was silently ignored and the request went to the
        // "default" namespace — confusing.
        for i := 0; i < len(args); i++ {
                a := args[i]
                if a == "--namespace" || a == "-n" {
                        if i+1 < len(args) {
                                ns = args[i+1]
                                i++
                        }
                } else if strings.HasPrefix(a, "--namespace=") {
                        ns = strings.TrimPrefix(a, "--namespace=")
                } else if strings.HasPrefix(a, "-n=") {
                        ns = strings.TrimPrefix(a, "-n=")
                }
        }

        // v3.8.4: /admin/investigate requires auth.
        body, err := c.postAuth("/admin/investigate", map[string]string{
                "service":   service,
                "namespace": ns,
                "reason":    "manual trigger via CLI",
        }, true)
        if err != nil {
                fail(err)
        }

        var resp map[string]any
        json.Unmarshal(body, &resp)
        fmt.Printf("Investigation triggered for %s/%s\n", service, ns)
        fmt.Printf("Cluster ID: %s\n", str(resp, "cluster_id"))
}

func (c *client) handlePause(args []string) {
        duration := 30
        // v3.8.4: Support both --duration 60 and --duration=60 (and -d
        // shorthand). Previous version only handled the space form, so
        // --duration=60 was silently ignored and pause always used 30 min.
        for i := 0; i < len(args); i++ {
                a := args[i]
                if a == "--duration" || a == "-d" {
                        if i+1 < len(args) {
                                fmt.Sscanf(args[i+1], "%d", &duration)
                                i++
                        }
                } else if strings.HasPrefix(a, "--duration=") {
                        fmt.Sscanf(strings.TrimPrefix(a, "--duration="), "%d", &duration)
                } else if strings.HasPrefix(a, "-d=") {
                        fmt.Sscanf(strings.TrimPrefix(a, "-d="), "%d", &duration)
                }
        }

        // v3.8.4: /admin/pause requires auth.
        _, err := c.postAuth("/admin/pause", map[string]any{
                "duration_min": duration,
                "reason":       "paused via CLI",
        }, true)
        if err != nil {
                fail(err)
        }
        fmt.Printf("Autonomous mode paused for %d minutes.\n", duration)
}

func (c *client) handleResume() {
        // v3.8.4: /admin/resume requires auth.
        _, err := c.postAuth("/admin/resume", nil, true)
        if err != nil {
                fail(err)
        }
        fmt.Println("Autonomous mode resumed.")
}

func (c *client) handleStats() {
        body, err := c.get("/metrics-summary")
        if err != nil {
                fail(err)
        }

        if c.jsonOut {
                fmt.Println(string(body))
                return
        }

        var stats map[string]int
        json.Unmarshal(body, &stats)

        fmt.Println("Sentinel Stats")
        fmt.Println("──────────────────")
        fmt.Printf("Active incidents:    %d\n", stats["active"])
        fmt.Printf("Resolved (24h):      %d\n", stats["resolved_24h"])
        fmt.Printf("Failed:              %d\n", stats["failed"])
        fmt.Printf("Total:               %d\n", stats["total"])
}

// handleConfig implements `sentinelctl config view` and `sentinelctl config set`.
//
// v3.8.4: Added `config set` so the CLI can drive the now-real Settings
// page. The server's POST /settings endpoint accepts a JSON body matching
// the RuntimeConfig struct in internal/runtimecfg/runtimecfg.go. The
// apply callback in main.go propagates the changes into the live LLM
// router immediately — no restart needed.
//
// Usage:
//
//      sentinelctl config view                                      # GET /settings (no auth)
//      sentinelctl config set '{"llm_fast_model":"glm-4-flash"}'    # inline JSON (auth)
//      sentinelctl config set ./my-settings.json                    # from file (auth)
func (c *client) handleConfig(args []string) {
        if len(args) == 0 {
                c.viewConfig()
                return
        }
        switch args[0] {
        case "view", "show":
                c.viewConfig()
        case "set", "save":
                if len(args) < 2 {
                        fmt.Fprintln(os.Stderr, "Usage: sentinelctl config set <json-string-or-file-path>")
                        fmt.Fprintln(os.Stderr, "Examples:")
                        fmt.Fprintln(os.Stderr, `  sentinelctl config set '{"llm_fast_model":"glm-4-flash"}'`)
                        fmt.Fprintln(os.Stderr, "  sentinelctl config set ./my-settings.json")
                        os.Exit(1)
                }
                c.setConfig(args[1])
        default:
                fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
                os.Exit(1)
        }
}

func (c *client) viewConfig() {
        body, err := c.get("/settings")
        if err != nil {
                fail(err)
        }

        if c.jsonOut {
                fmt.Println(string(body))
                return
        }

        var settings map[string]any
        json.Unmarshal(body, &settings)

        keys := make([]string, 0, len(settings))
        for k := range settings {
                keys = append(keys, k)
        }
        sort.Strings(keys)

        fmt.Println("Sentinel Configuration")
        fmt.Println("───────────────────────────")
        for _, k := range keys {
                fmt.Printf("%-25s %v\n", k, settings[k])
        }
}

// setConfig reads JSON from a literal string or a file path and POSTs it
// to /settings. v3.8.4.
func (c *client) setConfig(arg string) {
        var jsonBytes []byte
        if strings.HasPrefix(arg, "{") || strings.HasPrefix(arg, "[") {
                jsonBytes = []byte(arg)
        } else {
                b, err := os.ReadFile(arg)
                if err != nil {
                        fail(fmt.Errorf("read config file %s: %w", arg, err))
                }
                jsonBytes = b
        }

        // Validate it parses as JSON before sending.
        var probe any
        if err := json.Unmarshal(jsonBytes, &probe); err != nil {
                fail(fmt.Errorf("invalid JSON: %w", err))
        }

        // v3.8.4: POST /settings requires auth.
        body, err := c.postAuth("/settings", json.RawMessage(jsonBytes), true)
        if err != nil {
                fail(err)
        }
        fmt.Println("Settings saved.")
        if c.jsonOut {
                fmt.Println(string(body))
        } else {
                var resp map[string]string
                if json.Unmarshal(body, &resp) == nil {
                        if msg, ok := resp["message"]; ok && msg != "" {
                                fmt.Printf("Server: %s\n", msg)
                        }
                }
        }
}

func (c *client) handleMetrics() {
        body, err := c.get("/metrics")
        if err != nil {
                fail(err)
        }
        fmt.Print(string(body))
}

// --- Helpers ---

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

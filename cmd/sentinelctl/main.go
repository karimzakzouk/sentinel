// Package main is the sentinelctl CLI — a thin HTTP client for Sentinel's API.
//
// Usage:
//   sentinelctl incidents list [--status X] [--service Y] [--last 7d]
//   sentinelctl incidents get <id>
//   sentinelctl trigger <service> --namespace sim
//   sentinelctl pause [--duration 60m]
//   sentinelctl resume
//   sentinelctl stats
//   sentinelctl config view
//   sentinelctl metrics
//
// Flags:
//   --server URL    Sentinel API URL (default: http://localhost:8000)
//   --json          Output raw JSON instead of tables
//   --limit N       Number of results (default: 20)
package main

import (
        "bytes"
        "encoding/json"
        "flag"
        "fmt"
        "io"
        "net/http"
        "os"
        "sort"
        "text/tabwriter"
        "time"
)

const version = "v3.6.0"

func main() {
        if len(os.Args) < 2 {
                printHelp()
                os.Exit(0)
        }

        // Global flags
        server := flag.String("server", getenv("SENTINEL_SERVER", "http://localhost:8000"), "Sentinel API URL")
        jsonOut := flag.Bool("json", false, "Output raw JSON")

        // Parse subcommand
        cmd := os.Args[1]
        args := os.Args[2:]

        // Re-parse global flags from the remaining args
        fs := flag.NewFlagSet("sentinelctl", flag.ExitOnError)
        fs.StringVar(server, "server", *server, "Sentinel API URL")
        fs.BoolVar(jsonOut, "json", false, "Output raw JSON")
        _ = fs.Parse(args)
        args = fs.Args()

        c := &client{baseURL: *server, jsonOut: *jsonOut}

        switch cmd {
        case "incidents", "incident", "i":
                c.handleIncidents(args)
        case "trigger", "t":
                c.handleTrigger(args)
        case "pause":
                c.handlePause(args)
        case "resume":
                c.handleResume()
        case "stats", "s":
                c.handleStats()
        case "config", "cfg":
                c.handleConfig()
        case "metrics", "m":
                c.handleMetrics()
        case "version", "v":
                fmt.Printf("sentinelctl %s\n", version)
        case "help", "h", "--help", "-h":
                printHelp()
        default:
                fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
                printHelp()
                os.Exit(1)
        }
}

// client wraps the HTTP calls to Sentinel's API.
type client struct {
        baseURL string
        jsonOut bool
}

func (c *client) get(path string) ([]byte, error) {
        resp, err := http.Get(c.baseURL + path)
        if err != nil {
                return nil, fmt.Errorf("connection failed: %w\nIs Sentinel running at %s?", err, c.baseURL)
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        if resp.StatusCode >= 400 {
                return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
        }
        return body, nil
}

func (c *client) post(path string, payload any) ([]byte, error) {
        var body io.Reader
        if payload != nil {
                b, _ := json.Marshal(payload)
                body = bytes.NewReader(b)
        }
        resp, err := http.Post(c.baseURL+path, "application/json", body)
        if err != nil {
                return nil, fmt.Errorf("connection failed: %w", err)
        }
        defer resp.Body.Close()
        respBody, _ := io.ReadAll(resp.Body)
        if resp.StatusCode >= 400 {
                return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
        }
        return respBody, nil
}

// --- Commands ---

func (c *client) handleIncidents(args []string) {
        if len(args) == 0 {
                c.listIncidents()
                return
        }
        switch args[0] {
        case "list", "ls":
                c.listIncidents()
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

func (c *client) listIncidents() {
        limit := 20
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

        // Pretty-print the incident
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

                // Agent trace
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
        for i, a := range args {
                if a == "--namespace" || a == "-n" {
                        if i+1 < len(args) {
                                ns = args[i+1]
                        }
                }
        }

        body, err := c.post("/admin/investigate", map[string]string{
                "service":   service,
                "namespace": ns,
                "reason":    "manual trigger via CLI",
        })
        if err != nil {
                fail(err)
        }

        var resp map[string]any
        json.Unmarshal(body, &resp)
        fmt.Printf("Investigation triggered for %s/%s\n", service, ns)
        fmt.Printf("Cluster ID: %s\n", str(resp, "cluster_id"))
}

func (c *client) handlePause(args []string) {
        duration := 30 // minutes
        for i, a := range args {
                if a == "--duration" || a == "-d" {
                        if i+1 < len(args) {
                                fmt.Sscanf(args[i+1], "%d", &duration)
                        }
                }
        }

        _, err := c.post("/admin/pause", map[string]any{
                "duration_min": duration,
                "reason":       "paused via CLI",
        })
        if err != nil {
                fail(err)
        }
        fmt.Printf("Autonomous mode paused for %d minutes.\n", duration)
}

func (c *client) handleResume() {
        _, err := c.post("/admin/resume", nil)
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

func (c *client) handleConfig() {
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

        // Sort keys for consistent output
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
  incidents list     List recent incidents
  incidents get <id> Show incident details
  trigger <service>  Manually trigger an investigation
  pause [--duration N]  Pause autonomous mode (default: 30 min)
  resume             Resume autonomous mode
  stats              Show incident statistics
  config view        Show current configuration
  metrics            Show Prometheus metrics
  version            Show version
  help               Show this help

Flags:
  --server URL       Sentinel API URL (default: http://localhost:8000)
  --json             Output raw JSON instead of tables
  --namespace NS     Namespace (for trigger command)

Environment:
  SENTINEL_SERVER    Default API URL (same as --server)

Examples:
  sentinelctl incidents list
  sentinelctl incidents get 91cce5a5
  sentinelctl trigger demo-app --namespace sim
  sentinelctl pause --duration 60
  sentinelctl stats
  sentinelctl config view --json
`, version)
}

// Unused but kept for potential future use
var _ = time.Now

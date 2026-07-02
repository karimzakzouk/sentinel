// Package main — incident list/get commands for sentinelctl.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)


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


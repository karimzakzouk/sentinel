// Package main — incident commands for sentinelctl: list, get, trigger,
// pause, resume, stats.
//
// The command handlers (handleIncidents, handleTrigger, …) parse flags and
// delegate to the underlying I/O methods (ListIncidents, GetIncident, …),
// which take a context and an io.Writer and return an error. The split
// keeps the I/O methods testable: tests pass a httptest.Server URL and a
// bytes.Buffer, then assert on the buffer + the returned error.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// IncidentFilters captures the optional --status/--service/--last/--limit
// filters accepted by `sentinelctl incidents list`.
type IncidentFilters struct {
	Status  string
	Service string
	Last    time.Duration
	Limit   int
}

// ParseIncidentFilters scans args for --status, --service, --last, --limit
// and their =-shorthand forms. Unknown flags are ignored. Limit defaults to
// 50 when no --limit flag is present.
func ParseIncidentFilters(args []string) IncidentFilters {
	f := IncidentFilters{Limit: 50}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--status" && i+1 < len(args):
			f.Status = args[i+1]
			i++
		case strings.HasPrefix(a, "--status="):
			f.Status = strings.TrimPrefix(a, "--status=")
		case a == "--service" && i+1 < len(args):
			f.Service = args[i+1]
			i++
		case strings.HasPrefix(a, "--service="):
			f.Service = strings.TrimPrefix(a, "--service=")
		case a == "--last" && i+1 < len(args):
			if d, err := parseLastDuration(args[i+1]); err == nil {
				f.Last = d
			}
			i++
		case strings.HasPrefix(a, "--last="):
			if d, err := parseLastDuration(strings.TrimPrefix(a, "--last=")); err == nil {
				f.Last = d
			}
		case a == "--limit" && i+1 < len(args):
			fmt.Sscanf(args[i+1], "%d", &f.Limit)
			i++
		case strings.HasPrefix(a, "--limit="):
			fmt.Sscanf(strings.TrimPrefix(a, "--limit="), "%d", &f.Limit)
		}
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	// When a time filter is set, fetch a larger window so the client-side
	// filter has enough rows to work with.
	if f.Last > 0 && f.Limit < 500 {
		f.Limit = 500
	}
	return f
}

// handleIncidents dispatches `sentinelctl incidents <list|get>`.
func (c *Client) handleIncidents(args []string) {
	ctx := context.Background()
	if len(args) == 0 {
		c.failHandler(c.ListIncidents(ctx, os.Stdout, IncidentFilters{Limit: 50}))
		return
	}
	switch args[0] {
	case "list", "ls":
		c.failHandler(c.ListIncidents(ctx, os.Stdout, ParseIncidentFilters(args[1:])))
	case "get", "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: sentinelctl incidents get <id>")
			os.Exit(1)
		}
		c.failHandler(c.GetIncident(ctx, os.Stdout, args[1]))
	default:
		// Treat the bare token as an incident ID prefix.
		c.failHandler(c.GetIncident(ctx, os.Stdout, args[0]))
	}
}

// ListIncidents fetches /incidents?limit=N and writes a table (or raw JSON
// when c.jsonOut is true) to w. Client-side --status / --service / --last
// filters are applied to the table view; --json bypasses filtering and
// returns the raw server response.
func (c *Client) ListIncidents(ctx context.Context, w io.Writer, filters IncidentFilters) error {
	body, err := c.Get(ctx, fmt.Sprintf("/incidents?limit=%d", filters.Limit))
	if err != nil {
		return err
	}
	if c.jsonOut {
		fmt.Fprintln(w, string(body))
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	incidents := filterIncidents(raw, filters)
	if len(incidents) == 0 {
		fmt.Fprintln(w, "No incidents found.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSERVICE\tNAMESPACE\tSTATUS\tCONFIDENCE\tCREATED")
	for _, inc := range incidents {
		id := str(inc, "incident_id")
		cluster, _ := inc["cluster"].(map[string]any)
		service := str(cluster, "service")
		namespace := str(cluster, "namespace")
		status := str(inc, "status")
		confidence := confidenceFromState(inc)
		created := formatTime(str(inc, "created_at"))
		if id == "" {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(id, 8), service, namespace, status, confidence, created)
	}
	return tw.Flush()
}

// shortID returns the first n characters of id, or id unchanged when shorter.
// Used for the ID column in `incidents list` so the full UUID isn't printed.
func shortID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// filterIncidents applies the optional --status, --service, --last filters
// to a slice of incident JSON maps. Returns the input unchanged when no
// filter is set.
func filterIncidents(incidents []map[string]any, f IncidentFilters) []map[string]any {
	if f.Status == "" && f.Service == "" && f.Last <= 0 {
		return incidents
	}
	var cutoff time.Time
	if f.Last > 0 {
		cutoff = time.Now().Add(-f.Last)
	}
	out := make([]map[string]any, 0, len(incidents))
	for _, inc := range incidents {
		if f.Status != "" && str(inc, "status") != f.Status {
			continue
		}
		cluster, _ := inc["cluster"].(map[string]any)
		if f.Service != "" && str(cluster, "service") != f.Service {
			continue
		}
		if f.Last > 0 {
			if t, err := time.Parse(time.RFC3339, str(inc, "created_at")); err == nil && t.Before(cutoff) {
				continue
			}
		}
		out = append(out, inc)
	}
	return out
}

// confidenceFromState extracts a "NN%" string from state.root_cause.confidence
// when present. Returns "" when the state or root cause is absent.
func confidenceFromState(inc map[string]any) string {
	state, _ := inc["state"].(map[string]any)
	if state == nil {
		return ""
	}
	rc, _ := state["root_cause"].(map[string]any)
	if rc == nil {
		return ""
	}
	if c, ok := rc["confidence"].(float64); ok {
		return fmt.Sprintf("%.0f%%", c*100)
	}
	return ""
}

// GetIncident fetches /incidents/{id} and writes the detail view to w.
func (c *Client) GetIncident(ctx context.Context, w io.Writer, id string) error {
	body, err := c.Get(ctx, "/incidents/"+id)
	if err != nil {
		return err
	}
	if c.jsonOut {
		fmt.Fprintln(w, string(body))
		return nil
	}
	var inc map[string]any
	if err := json.Unmarshal(body, &inc); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	writeIncidentDetail(w, inc)
	return nil
}

// writeIncidentDetail renders a single incident as a key/value block.
func writeIncidentDetail(w io.Writer, inc map[string]any) {
	cluster, _ := inc["cluster"].(map[string]any)
	state, _ := inc["state"].(map[string]any)

	fmt.Fprintf(w, "Incident: %s\n", str(inc, "incident_id"))
	fmt.Fprintf(w, "Service:  %s\n", str(cluster, "service"))
	fmt.Fprintf(w, "Status:   %s\n", str(inc, "status"))
	fmt.Fprintf(w, "Created:  %s\n", formatTime(str(inc, "created_at")))

	if state == nil {
		return
	}
	if rc, ok := state["root_cause"].(map[string]any); ok {
		fmt.Fprintf(w, "\nRoot Cause:\n  %s\n", str(rc, "statement"))
		if conf, ok := rc["confidence"].(float64); ok {
			fmt.Fprintf(w, "  Confidence: %.0f%%\n", conf*100)
		}
	}
	if fp, ok := state["fix_proposal"].(map[string]any); ok {
		fmt.Fprintf(w, "\nFix:\n  Type: %s\n  Description: %s\n", str(fp, "fix_type"), str(fp, "description"))
	}
	if er, ok := state["execution_result"].(map[string]any); ok {
		fmt.Fprintf(w, "\nExecution:\n  PR: %s\n  Sandbox: %s\n", str(er, "pr_url"), str(er, "sandbox_summary"))
	}
	if trace, ok := state["agent_trace"].([]any); ok && len(trace) > 0 {
		fmt.Fprintln(w, "\nAgent Trace:")
		for _, t := range trace {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  - %s", str(m, "agent"))
			if c, ok := m["confidence"]; ok {
				fmt.Fprintf(w, " (confidence: %v)", c)
			}
			if n, ok := m["files_changed"]; ok {
				fmt.Fprintf(w, " (files: %v)", n)
			}
			fmt.Fprintln(w)
		}
	}
}

// handleTrigger parses --namespace/--reason and triggers an investigation.
func (c *Client) handleTrigger(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl trigger <service> [--namespace sim] [--reason \"...\"]")
		os.Exit(1)
	}
	service := args[0]
	if strings.HasPrefix(service, "-") {
		fmt.Fprintf(os.Stderr, "error: service name must not start with '-' (got %q)\n", service)
		fmt.Fprintln(os.Stderr, "Did you mean to pass a flag before the service name?")
		os.Exit(1)
	}
	ns := "default"
	reason := "manual trigger via CLI"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--namespace" || a == "-n") && i+1 < len(args):
			ns = args[i+1]
			i++
		case strings.HasPrefix(a, "--namespace="):
			ns = strings.TrimPrefix(a, "--namespace=")
		case strings.HasPrefix(a, "-n="):
			ns = strings.TrimPrefix(a, "-n=")
		case (a == "--reason") && i+1 < len(args):
			reason = args[i+1]
			i++
		case strings.HasPrefix(a, "--reason="):
			reason = strings.TrimPrefix(a, "--reason=")
		}
	}
	c.failHandler(c.Trigger(context.Background(), os.Stdout, service, ns, reason))
}

// Trigger POSTs /admin/investigate and writes the new cluster ID to w.
func (c *Client) Trigger(ctx context.Context, w io.Writer, service, namespace, reason string) error {
	if service == "" {
		return fmt.Errorf("service is required")
	}
	if namespace == "" {
		namespace = "default"
	}
	if reason == "" {
		reason = "manual trigger via CLI"
	}
	body, err := c.PostJSON(ctx, "/admin/investigate", map[string]string{
		"service":   service,
		"namespace": namespace,
		"reason":    reason,
	}, true)
	if err != nil {
		return err
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	fmt.Fprintf(w, "Investigation triggered for %s/%s\n", service, namespace)
	fmt.Fprintf(w, "Cluster ID: %s\n", str(resp, "cluster_id"))
	return nil
}

// handlePause parses --duration (minutes) and --reason and pauses autonomous mode.
func (c *Client) handlePause(args []string) {
	duration := 30
	reason := "paused via CLI"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--duration" || a == "-d") && i+1 < len(args):
			fmt.Sscanf(args[i+1], "%d", &duration)
			i++
		case strings.HasPrefix(a, "--duration="):
			fmt.Sscanf(strings.TrimPrefix(a, "--duration="), "%d", &duration)
		case strings.HasPrefix(a, "-d="):
			fmt.Sscanf(strings.TrimPrefix(a, "-d="), "%d", &duration)
		case a == "--reason" && i+1 < len(args):
			reason = args[i+1]
			i++
		case strings.HasPrefix(a, "--reason="):
			reason = strings.TrimPrefix(a, "--reason=")
		}
	}
	c.failHandler(c.Pause(context.Background(), os.Stdout, duration, reason))
}

// Pause POSTs /admin/pause and writes a confirmation to w.
func (c *Client) Pause(ctx context.Context, w io.Writer, durationMin int, reason string) error {
	if durationMin <= 0 {
		durationMin = 30
	}
	if reason == "" {
		reason = "paused via CLI"
	}
	if _, err := c.PostJSON(ctx, "/admin/pause", map[string]any{
		"duration_min": durationMin,
		"reason":       reason,
	}, true); err != nil {
		return err
	}
	fmt.Fprintf(w, "Autonomous mode paused for %d minutes.\n", durationMin)
	return nil
}

// handleResume resumes autonomous mode.
func (c *Client) handleResume() {
	c.failHandler(c.Resume(context.Background(), os.Stdout))
}

// Resume POSTs /admin/resume and writes a confirmation to w.
func (c *Client) Resume(ctx context.Context, w io.Writer) error {
	if _, err := c.PostJSON(ctx, "/admin/resume", nil, true); err != nil {
		return err
	}
	fmt.Fprintln(w, "Autonomous mode resumed.")
	return nil
}

// handleStats fetches /metrics-summary and writes a small stats block.
func (c *Client) handleStats() {
	c.failHandler(c.Stats(context.Background(), os.Stdout))
}

// Stats fetches /metrics-summary and writes the active/resolved/failed/total
// counts to w.
func (c *Client) Stats(ctx context.Context, w io.Writer) error {
	body, err := c.Get(ctx, "/metrics-summary")
	if err != nil {
		return err
	}
	if c.jsonOut {
		fmt.Fprintln(w, string(body))
		return nil
	}
	var stats map[string]int
	if err := json.Unmarshal(body, &stats); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	fmt.Fprintln(w, "Sentinel Stats")
	fmt.Fprintln(w, "──────────────────")
	fmt.Fprintf(w, "Active incidents:    %d\n", stats["active"])
	fmt.Fprintf(w, "Resolved (24h):      %d\n", stats["resolved_24h"])
	fmt.Fprintf(w, "Failed:              %d\n", stats["failed"])
	fmt.Fprintf(w, "Total:               %d\n", stats["total"])
	return nil
}

// failHandler is the single error→exit boundary used by the command
// dispatchers. It exists as a method so tests can swap it out (the default
// calls fail, which prints to stderr and exits 1).
func (c *Client) failHandler(err error) {
	if err == nil {
		return
	}
	fail(err)
}

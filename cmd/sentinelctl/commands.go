// Package main — trigger/pause/resume/stats/config/metrics commands.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func (c *client) handleTrigger(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl trigger <service> [--namespace sim]")
		os.Exit(1)
	}
	service := args[0]

	// — the meta-flag check in main() should catch --help/-h before we get here,
	// but if a future flag is added that isn't in the meta-flag list, we don't
	// want to accidentally trigger an investigation with service="--whatever".

	if strings.HasPrefix(service, "-") {
		fmt.Fprintf(os.Stderr, "Error: service name must not start with '-' (got %q)\n", service)
		fmt.Fprintln(os.Stderr, "Did you mean to pass a flag before the service name?")
		os.Exit(1)
	}
	ns := "default"

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
		} else if after, ok := strings.CutPrefix(a, "--namespace="); ok {
			ns = after
		} else if after, ok := strings.CutPrefix(a, "-n="); ok {
			ns = after
		}
	}

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

	// shorthand). Previous version only handled the space form, so
	// --duration=60 was silently ignored and pause always used 30 min.

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--duration" || a == "-d" {
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &duration)
				i++
			}
		} else if after, ok := strings.CutPrefix(a, "--duration="); ok {
			fmt.Sscanf(after, "%d", &duration)
		} else if after, ok := strings.CutPrefix(a, "-d="); ok {
			fmt.Sscanf(after, "%d", &duration)
		}
	}

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
// page. The server's POST /settings endpoint accepts a JSON body matching
// the RuntimeConfig struct in internal/runtimecfg/runtimecfg.go. The
// apply callback in main.go propagates the changes into the live LLM
// router immediately — no restart needed.
// Usage:
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

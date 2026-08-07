// Package main — `sentinelctl config view|set` commands.
//
// `config view` GETs /settings and prints the runtime config as a sorted
// key/value table. `config set` POSTs either an inline JSON string or the
// contents of a JSON file to /settings. The server's POST /settings endpoint
// accepts a JSON body matching internal/config.RuntimeSettings; the apply
// callback propagates the changes into the live LLM router immediately (no
// restart needed).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// handleConfig dispatches `sentinelctl config <view|set>`.
func (c *Client) handleConfig(args []string) {
	ctx := context.Background()
	if len(args) == 0 {
		c.failHandler(c.ViewConfig(ctx, os.Stdout))
		return
	}
	switch args[0] {
	case "view", "show":
		c.failHandler(c.ViewConfig(ctx, os.Stdout))
	case "set", "save":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: sentinelctl config set <json-string-or-file-path>")
			fmt.Fprintln(os.Stderr, "Examples:")
			fmt.Fprintln(os.Stderr, `  sentinelctl config set '{"llm_fast_model":"glm-4-flash"}'`)
			fmt.Fprintln(os.Stderr, "  sentinelctl config set ./my-settings.json")
			os.Exit(1)
		}
		c.failHandler(c.SetConfig(ctx, os.Stdout, args[1]))
	default:
		fmt.Fprintf(os.Stderr, "error: unknown config subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// ViewConfig GETs /settings and writes a sorted key/value table to w.
// When c.jsonOut is true the raw JSON is written instead.
func (c *Client) ViewConfig(ctx context.Context, w io.Writer) error {
	body, err := c.Get(ctx, "/settings")
	if err != nil {
		return err
	}
	if c.jsonOut {
		fmt.Fprintln(w, string(body))
		return nil
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "Sentinel Configuration")
	fmt.Fprintln(w, "───────────────────────────")
	for _, k := range keys {
		fmt.Fprintf(w, "%-30s %v\n", k, settings[k])
	}
	return nil
}

// SetConfig reads JSON from a literal string or a file path and POSTs it to
// /settings. The argument is treated as inline JSON when it starts with "{"
// or "[", otherwise as a path. The JSON is validated locally before sending
// so a syntax error produces a clear "invalid JSON" message rather than a
// 500 from the server.
func (c *Client) SetConfig(ctx context.Context, w io.Writer, arg string) error {
	var jsonBytes []byte
	if strings.HasPrefix(arg, "{") || strings.HasPrefix(arg, "[") {
		jsonBytes = []byte(arg)
	} else {
		b, err := os.ReadFile(arg)
		if err != nil {
			return fmt.Errorf("read config file %s: %w", arg, err)
		}
		jsonBytes = b
	}
	var probe any
	if err := json.Unmarshal(jsonBytes, &probe); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	body, err := c.PostJSON(ctx, "/settings", json.RawMessage(jsonBytes), true)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Settings saved.")
	if c.jsonOut {
		fmt.Fprintln(w, string(body))
		return nil
	}
	var resp map[string]string
	if json.Unmarshal(body, &resp) == nil {
		if msg, ok := resp["message"]; ok && msg != "" {
			fmt.Fprintf(w, "Server: %s\n", msg)
		}
	}
	return nil
}

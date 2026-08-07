// Package main — `sentinelctl metrics` command.
//
// Fetches /metrics from the Sentinel server and writes the Prometheus
// exposition-format text to stdout. The server returns text/plain (version
// 0.0.4). The CLI passes the body through untouched so the output is
// byte-identical to what Prometheus scrapes — this matters because some
// operators pipe `sentinelctl metrics` directly into promtool for
// validation, and any reformatting would break the parser.
//
// When --json is set the body is still written verbatim. Prometheus
// exposition is not JSON, but the flag is honored for symmetry with the
// other commands so a script can pipe every subcommand's output through
// the same code path.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// handleMetrics fetches /metrics and writes the Prometheus exposition to stdout.
func (c *Client) handleMetrics() {
	c.failHandler(c.Metrics(context.Background(), os.Stdout))
}

// Metrics GETs /metrics and writes the body to w. The body is the Prometheus
// exposition format; sentinelctl does not parse or transform it.
func (c *Client) Metrics(ctx context.Context, w io.Writer) error {
	body, err := c.Get(ctx, "/metrics")
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// formatMetricsBlock is retained as a small alignment helper for callers
// that want a more readable view of the exposition format. It pads metric
// lines (those not starting with '#' and not blank) so the value column
// starts at a fixed offset. Comment lines (# HELP, # TYPE) and blank lines
// are passed through unchanged. The helper is intentionally minimal — it
// does not parse labels or timestamps, only aligns the first whitespace
// separator.
func formatMetricsBlock(in string) string {
	var b strings.Builder
	for _, line := range strings.Split(in, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		// Split into name+labels and value. The first space after the
		// metric identifier separates them; preserve everything after.
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		name := line[:idx]
		rest := line[idx+1:]
		fmt.Fprintf(&b, "%-50s %s\n", name, rest)
	}
	return b.String()
}

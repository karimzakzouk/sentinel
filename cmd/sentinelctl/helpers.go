// Package main — small pure helpers for sentinelctl.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// str extracts a string value from a JSON-decoded map. Returns "" when the
// key is missing or the value is nil. Float64 (the default JSON number type)
// is rendered without a fractional part so an ID like 42 prints as "42" not
// "42.000000".
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// getenv returns os.Getenv(key) when set and non-empty, otherwise def.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// fail prints err to stderr in the project's "error: ..." style and exits 1.
// Used only by the top-level command dispatch in main.go; library-style
// helpers return errors so they can be tested.
func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// formatTime renders an RFC3339 timestamp as "2006-01-02 15:04:05" (UTC).
// Returns the input unchanged when it cannot be parsed, so a malformed
// timestamp from the server still surfaces in the output instead of being
// silently dropped.
func formatTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// formatDuration renders a duration in the largest whole units that fit
// (e.g. 90m → "1h30m", 65s → "1m5s", 500ms → "500ms"). Durations of zero
// or less return "0s". Sub-second precision is preserved for fractional
// durations; whole-second durations are split into h/m/s components with
// zero components omitted (so 2h renders as "2h", not "2h0m0s").
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return d.String()
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	if b.Len() == 0 {
		return "0s"
	}
	return b.String()
}

// truncate caps s to n runes, appending "…" when truncation occurred.
// n ≤ 0 returns s unchanged.
func truncate(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// parseLastDuration parses a "--last" duration flag value. Supports the
// suffixes "d" (days), "h" (hours), "m" (minutes), and bare Go duration
// strings (e.g. "2h30m"). Returns an error for unparseable input.
func parseLastDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("parse days: %w", err)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return d, nil
}

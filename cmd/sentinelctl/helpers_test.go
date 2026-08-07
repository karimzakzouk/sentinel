package main

import (
	"testing"
	"time"
)

// TestFormatTime verifies RFC3339 input is rendered as "2006-01-02 15:04:05"
// in UTC, and unparseable input is returned unchanged.
func TestFormatTime(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2025-01-02T15:04:05Z", "2025-01-02 15:04:05"},
		{"2025-01-02T15:04:05+02:00", "2025-01-02 13:04:05"},
		{"", ""},
		{"not-a-time", "not-a-time"},
	}
	for _, tc := range cases {
		if got := formatTime(tc.in); got != tc.want {
			t.Errorf("formatTime(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatDuration verifies durations are rendered in the largest whole
// unit that fits, with sub-second precision stripped.
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{500 * time.Millisecond, "500ms"},
		{65 * time.Second, "1m5s"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{24*time.Hour + 30*time.Minute, "24h30m"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTruncate verifies truncate caps the rune count and appends "…".
func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "he…"},
		{"hello", 1, "…"},
		{"hello", 0, "hello"},
		{"héllo", 3, "hé…"}, // rune-aware, not byte-aware
		{"", 5, ""},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// TestStr verifies str extracts strings, numbers, and bools from a JSON-decoded
// map and returns "" for missing keys or nil values.
func TestStr(t *testing.T) {
	m := map[string]any{
		"name":  "demo",
		"id":    float64(42),
		"on":    true,
		"empty": "",
		"null":  nil,
		"obj":   map[string]any{"a": 1},
		"arr":   []any{1, 2, 3},
	}
	cases := []struct {
		key, want string
	}{
		{"name", "demo"},
		{"id", "42"},
		{"on", "true"},
		{"empty", ""},
		{"null", ""},
		{"missing", ""},
		{"obj", "map[a:1]"},
		{"arr", "[1 2 3]"},
	}
	for _, tc := range cases {
		if got := str(m, tc.key); got != tc.want {
			t.Errorf("str(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
	if got := str(nil, "anything"); got != "" {
		t.Errorf("str(nil, ...) = %q, want empty", got)
	}
}

// TestParseLastDuration verifies the --last flag parser accepts "Nd", "Nh",
// "Nm", and bare Go duration strings.
func TestParseLastDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7d", 7 * 24 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"2h30m", 2*time.Hour + 30*time.Minute, true},
		{"", 0, false},
		{"abc", 0, false},
		{"5x", 0, false},
	}
	for _, tc := range cases {
		got, err := parseLastDuration(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("parseLastDuration(%q) err = %v, want nil", tc.in, err)
				continue
			}
			if got != tc.want {
				t.Errorf("parseLastDuration(%q) = %v, want %v", tc.in, got, tc.want)
			}
		} else {
			if err == nil {
				t.Errorf("parseLastDuration(%q) err = nil, want error", tc.in)
			}
		}
	}
}

// TestGetenv verifies getenv returns the env value when set, the default
// otherwise.
func TestGetenv(t *testing.T) {
	t.Setenv("SENTINEL_TEST_GETENV_SET", "yes")
	if got := getenv("SENTINEL_TEST_GETENV_SET", "no"); got != "yes" {
		t.Errorf("getenv(set) = %q, want yes", got)
	}
	if got := getenv("SENTINEL_TEST_GETENV_UNSET", "default"); got != "default" {
		t.Errorf("getenv(unset) = %q, want default", got)
	}
}

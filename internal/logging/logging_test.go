package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestWithIncidentID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ctx := WithIncidentID(context.Background(), "inc-abc")
	args := withCtx(ctx, nil)
	logger.Info("msg", args...)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if entry["incident_id"] != "inc-abc" {
		t.Errorf("incident_id = %v, want inc-abc", entry["incident_id"])
	}
}

func TestWithComponent(t *testing.T) {
	ctx := WithComponent(context.Background(), "investigator")
	args := withCtx(ctx, nil)

	want := "investigator"
	found := false
	for i, a := range args {
		if a == "component" && i+1 < len(args) && args[i+1] == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected component=%s in args %v", want, args)
	}
}

func TestWithIncidentIDAndComponent(t *testing.T) {
	ctx := WithIncidentID(context.Background(), "inc-1")
	ctx = WithComponent(ctx, "analyst")
	args := withCtx(ctx, nil)

	joined := strings.Join(stringsFromArgs(args), ",")
	if !strings.Contains(joined, "incident_id") || !strings.Contains(joined, "inc-1") {
		t.Errorf("missing incident_id: %v", args)
	}
	if !strings.Contains(joined, "component") || !strings.Contains(joined, "analyst") {
		t.Errorf("missing component: %v", args)
	}
}

func TestNilContext(t *testing.T) {
	if got := withCtx(context.TODO(), nil); got != nil {
		t.Errorf("empty ctx should produce no args, got %v", got)
	}
}

func TestEmptyContext(t *testing.T) {
	if got := withCtx(context.Background(), nil); got != nil {
		t.Errorf("empty ctx should produce no args, got %v", got)
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		key  string
		val  slog.Value
		want string
	}{
		{"api_key", slog.StringValue("sk-abc"), "***REDACTED***"},
		{"APIKEY", slog.StringValue("sk-abc"), "***REDACTED***"},
		{"github_token", slog.StringValue("ghp_x"), "***REDACTED***"},
		{"password", slog.StringValue("hunter2"), "***REDACTED***"},
		{"service", slog.StringValue("api"), "api"},
		{"incident_id", slog.StringValue("inc-1"), "inc-1"},
		{"empty_token", slog.StringValue(""), ""},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got := redactSecrets(nil, slog.Attr{Key: c.key, Value: c.val})
			if got.Value.String() != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.key, got.Value.String(), c.want)
			}
		})
	}
}

func TestInitMultipleCallsNoPanic(t *testing.T) {
	Init("INFO")
	Init("DEBUG")
	Init("WARN")
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"weird":   slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func stringsFromArgs(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.(string)
	}
	return out
}

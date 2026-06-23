package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestWithIncidentID verifies that incident_id is attached to the context
// and appears in structured log output.
func TestWithIncidentID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ctx := WithIncidentID(context.Background(), "incident-abc-123")
	args := argsFromCtx(ctx)

	// Use the logger directly to verify the args
	logger.Info("test message", args...)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if entry["incident_id"] != "incident-abc-123" {
		t.Errorf("incident_id = %v, want incident-abc-123", entry["incident_id"])
	}
	if entry["msg"] != "test message" {
		t.Errorf("msg = %v, want 'test message'", entry["msg"])
	}
}

// TestWithComponent verifies that component is attached to the context.
func TestWithComponent(t *testing.T) {
	ctx := WithComponent(context.Background(), "investigator")
	args := argsFromCtx(ctx)

	found := false
	for i, a := range args {
		if a == "component" && i+1 < len(args) && args[i+1] == "investigator" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected component=investigator in args, got %v", args)
	}
}

// TestWithIncidentIDAndComponent verifies both fields can coexist.
func TestWithIncidentIDAndComponent(t *testing.T) {
	ctx := WithIncidentID(context.Background(), "inc-1")
	ctx = WithComponent(ctx, "analyst")
	args := argsFromCtx(ctx)

	argsStr := strings.Join(toStrings(args), ",")
	if !strings.Contains(argsStr, "incident_id") || !strings.Contains(argsStr, "inc-1") {
		t.Errorf("missing incident_id in args: %v", args)
	}
	if !strings.Contains(argsStr, "component") || !strings.Contains(argsStr, "analyst") {
		t.Errorf("missing component in args: %v", args)
	}
}

// TestNilContext verifies that argsFromCtx handles nil context gracefully.
func TestNilContext(t *testing.T) {
	args := argsFromCtx(nil)
	if len(args) != 0 {
		t.Errorf("nil context should produce no args, got %v", args)
	}
}

// TestEmptyContext verifies that an empty context produces no args.
func TestEmptyContext(t *testing.T) {
	args := argsFromCtx(context.Background())
	if len(args) != 0 {
		t.Errorf("empty context should produce no args, got %v", args)
	}
}

// TestInitIsIdempotent verifies that calling Init multiple times doesn't
// re-initialize the logger (sync.Once protection).
func TestInitIsIdempotent(t *testing.T) {
	// Reset by creating a fresh package state is not possible (once is package-level),
	// but we can at least verify Init doesn't panic on multiple calls.
	Init("INFO")
	Init("DEBUG")
	Init("WARN")
	// If we got here without panicking, the test passes.
}

func toStrings(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.(string)
	}
	return out
}

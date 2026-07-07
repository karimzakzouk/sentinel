package detection

import (
        "testing"
        "time"
)

// TestShouldTriggerCooldown verifies the cooldown logic — the same error type
// for the same service+namespace should not re-trigger within the cooldown window.
// First test for the detection layer (710 lines, previously 0 tests).
func TestShouldTriggerCooldown(t *testing.T) {
        d := New(nil, nil, "")
        // Override cooldown to 100ms for fast testing
        d.cooldown = 100 * time.Millisecond

        // First trigger should succeed
        if !d.shouldTrigger("sim", "api", "KeyError") {
                t.Error("first trigger should succeed")
        }

        // Second trigger immediately should be blocked by cooldown
        if d.shouldTrigger("sim", "api", "KeyError") {
                t.Error("second trigger within cooldown should be blocked")
        }

        // Wait for cooldown to expire
        time.Sleep(150 * time.Millisecond)

        // Third trigger after cooldown should succeed
        if !d.shouldTrigger("sim", "api", "KeyError") {
                t.Error("trigger after cooldown should succeed")
        }
}

// TestShouldTriggerDifferentServices verifies that different services are
// tracked independently — an error for service A doesn't block service B.
func TestShouldTriggerDifferentServices(t *testing.T) {
        d := New(nil, nil, "")

        if !d.shouldTrigger("sim", "api", "KeyError") {
                t.Error("trigger for api should succeed")
        }
        if !d.shouldTrigger("sim", "worker", "KeyError") {
                t.Error("trigger for worker should succeed (different service)")
        }
}

// TestShouldTriggerDifferentNamespaces verifies that different namespaces are
// tracked independently.
func TestShouldTriggerDifferentNamespaces(t *testing.T) {
        d := New(nil, nil, "")

        if !d.shouldTrigger("sim", "api", "KeyError") {
                t.Error("trigger for sim/api should succeed")
        }
        if !d.shouldTrigger("prod", "api", "KeyError") {
                t.Error("trigger for prod/api should succeed (different namespace)")
        }
}

// TestExtractErrorType verifies the error type extraction from log lines.
func TestExtractErrorType(t *testing.T) {
        tests := []struct {
                line    string
                want    string
        }{
                {"KeyError: 'database_url'", "KeyError"},
                {"ValueError: invalid input", "ValueError"},
                {"TypeError: unsupported operand", "TypeError"},
                {"panic: runtime error: nil pointer dereference", "panic"},
                {"  File \"app.py\", line 42", "unknown_error"},
                {"", "unknown_error"},
        }

        for _, tt := range tests {
                t.Run(tt.want, func(t *testing.T) {
                        got := d.extractErrorType(tt.line)
                        if got != tt.want {
                                t.Errorf("extractErrorType(%q) = %q, want %q", tt.line, got, tt.want)
                        }
                })
        }
}

// dummy var to access the method — we need a Detector instance
var d = New(nil, nil, "")

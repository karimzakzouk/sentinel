package executors

import (
	"testing"
)

// TestSandboxRunner_ProbePaths verifies that the v3.4.0 configurable probe
// paths work correctly. Before v3.4.0, these were hardcoded to
// [/, /healthz, /readyz] for Level 2 and [/pay?amount=0, /pay?amount=-1,
// /pay?amount=abc] for Level 3 — which only worked for the demo app.
func TestSandboxRunner_ProbePaths(t *testing.T) {
	t.Run("defaults when not set", func(t *testing.T) {
		s := NewSandbox(nil, "sentinel-sandbox", false)
		if len(s.probePaths) != 3 {
			t.Errorf("expected 3 default probe paths, got %d (%v)", len(s.probePaths), s.probePaths)
		}
		want := []string{"/", "/healthz", "/readyz"}
		for i, p := range want {
			if s.probePaths[i] != p {
				t.Errorf("probePaths[%d] = %q, want %q", i, s.probePaths[i], p)
			}
		}
	})

	t.Run("custom paths via WithProbePaths", func(t *testing.T) {
		s := NewSandbox(nil, "sentinel-sandbox", false).
			WithProbePaths([]string{"/", "/health", "/ready", "/api/v1/status", "/pay?amount=0"})
		if len(s.probePaths) != 5 {
			t.Errorf("expected 5 custom probe paths, got %d (%v)", len(s.probePaths), s.probePaths)
		}
		if s.probePaths[4] != "/pay?amount=0" {
			t.Errorf("probePaths[4] = %q, want /pay?amount=0", s.probePaths[4])
		}
	})

	t.Run("empty paths ignored (falls back to defaults)", func(t *testing.T) {
		defaults := []string{"/", "/healthz", "/readyz"}
		s := NewSandbox(nil, "sentinel-sandbox", false)
		original := s.probePaths
		s.WithProbePaths(nil)
		if len(s.probePaths) != len(original) {
			t.Errorf("nil paths should not change probePaths, got %v (was %v)", s.probePaths, original)
		}
		s.WithProbePaths([]string{})
		if len(s.probePaths) != len(defaults) {
			t.Errorf("empty paths should not change probePaths, got %v", s.probePaths)
		}
	})
}

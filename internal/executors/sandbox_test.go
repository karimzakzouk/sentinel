package executors

import (
	"testing"
	"time"
)

// TestSandboxProbePathsDefaults verifies the default probe paths and the
// WithProbePaths override.
func TestSandboxProbePathsDefaults(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)
	if len(s.probePaths) != 3 {
		t.Fatalf("expected 3 default probe paths, got %d (%v)", len(s.probePaths), s.probePaths)
	}
	want := []string{"/", "/healthz", "/readyz"}
	for i, p := range want {
		if s.probePaths[i] != p {
			t.Errorf("probePaths[%d] = %q, want %q", i, s.probePaths[i], p)
		}
	}
}

func TestSandboxProbePathsWithCustom(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false).
		WithProbePaths([]string{"/", "/health", "/ready", "/api/v1/status", "/pay?amount=0"})
	if len(s.probePaths) != 5 {
		t.Fatalf("expected 5 custom probe paths, got %d (%v)", len(s.probePaths), s.probePaths)
	}
	if s.probePaths[4] != "/pay?amount=0" {
		t.Errorf("probePaths[4] = %q, want /pay?amount=0", s.probePaths[4])
	}
}

func TestSandboxProbePathsEmptyIgnored(t *testing.T) {
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
}

func TestSandboxSustainedReadyOverride(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)
	if s.sustainedWindow != 15*time.Second {
		t.Errorf("default sustainedWindow = %v, want 15s", s.sustainedWindow)
	}
	s.WithSustainedReady(42 * time.Second)
	if s.sustainedWindow != 42*time.Second {
		t.Errorf("after WithSustainedReady: %v, want 42s", s.sustainedWindow)
	}
}

func TestSandboxSentinelNamespaceOverride(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)
	if s.sentinelNamespace != "sentinel" {
		t.Errorf("default sentinelNamespace = %q, want sentinel", s.sentinelNamespace)
	}
	s.WithSentinelNamespace("sre-bot")
	if s.sentinelNamespace != "sre-bot" {
		t.Errorf("after WithSentinelNamespace: %q, want sre-bot", s.sentinelNamespace)
	}
	// Empty value should be ignored.
	s.WithSentinelNamespace("")
	if s.sentinelNamespace != "sre-bot" {
		t.Errorf("after empty WithSentinelNamespace: %q, want sre-bot", s.sentinelNamespace)
	}
}

// TestParsePathMethod verifies the probe-path entry parser handles the
// "METHOD /path" form and defaults to GET.
func TestParsePathMethod(t *testing.T) {
	cases := []struct {
		in       string
		wantM    string
		wantPath string
	}{
		{"/", "GET", "/"},
		{"/healthz", "GET", "/healthz"},
		{"GET /api/v1", "GET", "/api/v1"},
		{"POST /users", "POST", "/users"},
		{"DELETE /users/42", "DELETE", "/users/42"},
		{"PATCH /users/42", "PATCH", "/users/42"},
		{"PUT /users/42", "PUT", "/users/42"},
		{"HEAD /healthz", "HEAD", "/healthz"},
		// Unknown method → keep the whole entry as the path.
		{"BOGUS /foo", "GET", "BOGUS /foo"},
		// Leading space + no recognized method → keep entry as path.
		{" /weird", "GET", " /weird"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			m, p := parsePathMethod(c.in)
			if m != c.wantM {
				t.Errorf("method = %q, want %q", m, c.wantM)
			}
			if p != c.wantPath {
				t.Errorf("path = %q, want %q", p, c.wantPath)
			}
		})
	}
}

// TestLastN verifies the last-N-bytes helper used by sandbox failure summaries.
func TestLastN(t *testing.T) {
	if got := lastN("abc", 10); got != "abc" {
		t.Errorf("lastN(\"abc\",10) = %q, want \"abc\"", got)
	}
	if got := lastN("abcdef", 3); got != "def" {
		t.Errorf("lastN(\"abcdef\",3) = %q, want \"def\"", got)
	}
	if got := lastN("", 5); got != "" {
		t.Errorf("lastN(\"\",5) = %q, want \"\"", got)
	}
	if got := lastN("abc", 3); got != "abc" {
		t.Errorf("lastN(\"abc\",3) = %q, want \"abc\"", got)
	}
}

// TestExtractErrorContext verifies the context-extraction helper returns N
// lines around the first match.
func TestExtractErrorContext(t *testing.T) {
	logs := "line0\nline1\nline2: KeyError 'X'\nline3\nline4\nline5: KeyError 'Y'\n"
	got := extractErrorContext(logs, "KeyError", 1)
	// Should find the FIRST occurrence (line2) and return 1 line of context
	// on each side.
	want := "line1\nline2: KeyError 'X'\nline3"
	if got != want {
		t.Errorf("extractErrorContext = %q, want %q", got, want)
	}
}

func TestExtractErrorContextNotFound(t *testing.T) {
	logs := "no error here\njust normal logs\n"
	got := extractErrorContext(logs, "KeyError", 3)
	want := "KeyError (context not available)"
	if got != want {
		t.Errorf("extractErrorContext not-found = %q, want %q", got, want)
	}
}

// TestIsReproducerInfraError covers the Level 5 infra-error classification.
// This is the trickiest piece of logic in the sandbox: a broken test runner
// must not block a correct fix.
func TestIsReproducerInfraError(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    bool
	}{
		{"log fetch failure", "reproducer pod failed: fetch test pod logs: connection refused", true},
		{"pod failed", "reproducer pod failed: pod failed: ImagePullBackOff", true},
		{"runner error", "reproducer runner error (non-blocking): context deadline exceeded", true},
		{"invalid pod", "not valid for pod 'repro-runner-1'", true},

		// Real failures — must NOT be treated as infra.
		{"real traceback", "reproducer FAILED: crash\n--- Output ---\nTraceback (most recent call last)\nKeyError: 'X'", false},
		{"no reproducer generated", "no reproducer generated (reproducer gate skipped)", false},
		{"plain failure", "reproducer FAILED: exit code 1", false},
		{"empty summary", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReproducerInfraError(tc.summary); got != tc.want {
				t.Errorf("isReproducerInfraError(%q) = %v, want %v", tc.summary, got, tc.want)
			}
		})
	}
}

// TestOriginalErrorStillInReproducer covers the Level 5 false-negative
// detection: if the reproducer "failed" but the original error type is
// GONE from its output, the fix is correct.
func TestOriginalErrorStillInReproducer(t *testing.T) {
	cases := []struct {
		name             string
		reproOutput      string
		originalType     string
		wantStillPresent bool
	}{
		{
			name:             "error type gone — fix is correct",
			reproOutput:      "reproducer FAILED: exit code 1\n--- Output ---\nno error here\n",
			originalType:     "KeyError",
			wantStillPresent: false,
		},
		{
			name:             "error type present with colon",
			reproOutput:      "reproducer FAILED: exit 1\n--- Output ---\nKeyError: 'DATABASE_URL'",
			originalType:     "KeyError",
			wantStillPresent: true,
		},
		{
			name:             "error type present with raise",
			reproOutput:      "reproducer FAILED: exit 1\n--- Output ---\nraise KeyError('X')",
			originalType:     "KeyError",
			wantStillPresent: true,
		},
		{
			name:             "error type present with paren (Python builtin call)",
			reproOutput:      "reproducer FAILED: exit 1\n--- Output ---\nValueError('bad')",
			originalType:     "ValueError",
			wantStillPresent: true,
		},
		{
			name:             "no original type known — assume gone",
			reproOutput:      "reproducer FAILED: exit 1\n--- Output ---\nKeyError: 'X'",
			originalType:     "",
			wantStillPresent: false,
		},
		{
			name:             "case-insensitive match",
			reproOutput:      "reproducer FAILED: exit 1\n--- Output ---\ntypeerror: bad operand",
			originalType:     "TypeError",
			wantStillPresent: true,
		},
		{
			name:             "output before marker ignored",
			reproOutput:      "KeyError: ignored preamble\n--- Output ---\nall good, no error",
			originalType:     "KeyError",
			wantStillPresent: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := originalErrorStillInReproducer(tc.reproOutput, tc.originalType)
			if got != tc.wantStillPresent {
				t.Errorf("originalErrorStillInReproducer = %v, want %v", got, tc.wantStillPresent)
			}
		})
	}
}

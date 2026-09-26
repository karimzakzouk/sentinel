package executors

import (
	"context"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestSandboxProbePathsAutoDefault verifies the default is auto mode
// (no pinned paths): spec-derived paths when the workload declares them,
// otherwise the convention candidate set with any-of semantics.
func TestSandboxProbePathsAutoDefault(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)
	if len(s.probePaths) != 0 {
		t.Fatalf("expected auto mode (no pinned probe paths), got %v", s.probePaths)
	}
	// No spec paths available → convention candidates, any-of.
	paths, anyOf, source := s.resolveProbePaths(nil)
	if source != "convention" || !anyOf {
		t.Fatalf("auto mode without spec paths: want convention/any-of, got source=%q anyOf=%v", source, anyOf)
	}
	if len(paths) == 0 {
		t.Fatal("convention candidate set must not be empty")
	}
	// Spec paths available → they win, all-must-pass.
	paths, anyOf, source = s.resolveProbePaths([]string{"/health", "/ready"})
	if source != "spec" || anyOf || len(paths) != 2 {
		t.Fatalf("auto mode with spec paths: want spec/all-must-pass, got source=%q anyOf=%v paths=%v", source, anyOf, paths)
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
	// Explicit config outranks spec-derived paths.
	paths, anyOf, source := s.resolveProbePaths([]string{"/spec-health"})
	if source != "config" || anyOf || len(paths) != 5 || paths[0] != "/" {
		t.Fatalf("explicit config must win over spec: got source=%q anyOf=%v paths=%v", source, anyOf, paths)
	}
}

func TestSandboxProbePathsEmptyIgnored(t *testing.T) {
	s := NewSandbox(nil, "sentinel-sandbox", false)
	s.WithProbePaths(nil)
	if len(s.probePaths) != 0 {
		t.Errorf("nil paths must keep auto mode, got %v", s.probePaths)
	}
	s.WithProbePaths([]string{})
	if len(s.probePaths) != 0 {
		t.Errorf("empty paths must keep auto mode, got %v", s.probePaths)
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

// Run #30 post-mortem: transient_infra fixes (retry with backoff) do not
// eliminate the original error — the upstream dependency stays down. L3
// must accept observed retry behavior as verification for that fault
// class, while still failing a do-nothing fix (error present, no retries)
// and for other fault classes.
func TestVerifyErrorGoneFromLogs_RetryAwareForTransientInfra(t *testing.T) {
	s := &SandboxRunner{}
	baseInc := &schemas.Incident{
		Cluster: schemas.AnomalyCluster{
			Anomalies: []schemas.Anomaly{{Explanation: "socket.gaierror: [Errno -2] Name or service not known"}},
		},
	}
	// Celery-style canary output: error present + explicit retry lines.
	retryLogs := `[2026-09-26 09:00:01: ERROR/ForkPoolWorker-2] Task send_email failed with gaierror: [Errno -2] Name or service not known
[2026-09-26 09:00:01: INFO/MainProcess] Task send_email[e2f1] retry: Retry in 20s: gaierror: Name or service not known`
	// A canary with the error but no retry behavior at all.
	noRetryLogs := `[2026-09-26 09:00:01: ERROR/ForkPoolWorker-2] Task send_email failed with gaierror: [Errno -2] Name or service not known`

	// 1) transient_infra + retry evidence -> verified.
	incT := *baseInc
	incT.State = schemas.AgentState{RootCause: &schemas.RootCause{FaultClass: "transient_infra"}}
	if r := s.verifyErrorGoneFromLogs(context.Background(), &incT, retryLogs); !r.passed {
		t.Errorf("transient_infra + retry evidence should verify, got: %s", r.detail)
	}
	// 2) transient_infra but no retry evidence -> still failing.
	if r := s.verifyErrorGoneFromLogs(context.Background(), &incT, noRetryLogs); r.passed {
		t.Error("transient_infra without retry evidence must NOT verify (do-nothing fix)")
	}
	// 3) other fault class + retry evidence -> NOT verified by the retry path
	//    (the error must actually disappear).
	incL := *baseInc
	incL.State = schemas.AgentState{RootCause: &schemas.RootCause{FaultClass: "logic_data"}}
	if r := s.verifyErrorGoneFromLogs(context.Background(), &incL, retryLogs); r.passed {
		t.Error("logic_data + error present must NOT verify, even with stray retry lines")
	}
	// 4) error gone -> verified regardless of fault class.
	if r := s.verifyErrorGoneFromLogs(context.Background(), &incT, "all healthy"); !r.passed {
		t.Errorf("error gone should verify, got: %s", r.detail)
	}
}

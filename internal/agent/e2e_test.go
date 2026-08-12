package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/llm"
)

// e2e_test.go: end-to-end agent tests with a fake LLM.
//
// Before this file, the "6/6 bugs diagnosed correctly with llama-3.1-8b"
// claim in PR #65's commit message was a manual run — not reproducible in
// CI. If the agent regressed tomorrow, no test would catch it. This file
// converts that claim into a CI-checked assertion for at least one
// scenario, and structures the helper so adding more scenarios is a
// 10-line table entry.
//
// What this tests end-to-end:
//   - Planning call → plan string
//   - Tool-call turn → executes registered tool → tool result fed back
//   - Done turn → root cause + confidence + files
//   - Retry-on-malformed-output: garbage response → correction prompt → retry
//
// What this does NOT test:
//   - Real LLM reasoning quality (that's a model problem, not a code problem)
//   - Stuck detector triggering (separate unit tests cover that)
//   - MCTS / critic / condensation (default OFF — see v3_test.go for the
//     pure-helper coverage)

// fakeLLMServer returns an httptest.Server that responds to each LLM call
// with the next entry from `responses`. If the LLM call count exceeds
// len(responses), the server returns a 500 (test should fail loudly
// rather than silently looping). The returned callCount tracks how many
// LLM calls were made.
func fakeLLMServer(t *testing.T, responses []string) (*httptest.Server, *int32) {
	t.Helper()
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		idx := int(n) - 1
		if idx >= len(responses) {
			t.Errorf("fake LLM: call %d exceeded scripted responses (%d total) — agent is looping", n, len(responses))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		content := responses[idx]
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}, "finish_reason": "stop"},
			},
		}
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("fake LLM: marshal response: %v", err)
		}
		w.WriteHeader(200)
		w.Write(b)
	}))
	return server, &callCount
}

// makeAgent constructs an Agent wired to a fake LLM server with the given
// scripted responses. The agent has a single `read_file` tool that reads
// from the given temp dir.
func makeAgent(t *testing.T, responses []string, repoDir string) (*Agent, *httptest.Server, *int32) {
	t.Helper()
	server, callCount := fakeLLMServer(t, responses)
	router := llm.New(server.URL, "k", "fast-model", "frontier-model")
	a := New(router, AgentConfig{
		MaxTurns:         5,
		TurnTimeout:      5 * time.Second,
		CompactThreshold: 100, // high enough that condensation never triggers
	})
	// Register a minimal read_file tool that reads from repoDir.
	a.RegisterTool("read_file",
		"Read a file from the repo.",
		`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
		func(ctx context.Context, args map[string]any) ToolResult {
			path, _ := args["path"].(string)
			if path == "" {
				return ToolResult{Success: false, Output: "missing path"}
			}
			// Resolve relative to repoDir, prevent traversal.
			cleaned := filepath.Clean(path)
			if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
				return ToolResult{Success: false, Output: "path outside repo"}
			}
			full := filepath.Join(repoDir, cleaned)
			data, err := os.ReadFile(full)
			if err != nil {
				return ToolResult{Success: false, Output: err.Error()}
			}
			return ToolResult{Success: true, Output: string(data)}
		})
	return a, server, callCount
}

// TestAgent_E2E_KeyErrorDiagnosis is the e2e test that backs the "agent
// correctly diagnoses a bug" claim from PR #65. The scenario: a Python
// service crashes with `KeyError: 'DATABASE_URL'` because app/config.py
// accesses `os.environ['DATABASE_URL']` without a `.get()` fallback.
//
// The fake LLM is scripted to:
//  1. Return a plan string (planning call)
//  2. Call read_file on app/config.py (turn 1)
//  3. Return done with the correct root cause + 90% confidence (turn 2)
//
// The test asserts the agent:
//   - Returns a non-nil result
//   - Reports the correct root cause (mentions DATABASE_URL + config.py)
//   - Reports confidence >= 0.5
//   - Used <= MaxTurns turns
//   - Made exactly 3 LLM calls (1 plan + 2 turns)
func TestAgent_E2E_KeyErrorDiagnosis(t *testing.T) {
	repoDir := t.TempDir()
	configPy := `import os

def load_config():
    db_url = os.environ['DATABASE_URL']
    return {'db_url': db_url}
`
	if err := os.WriteFile(filepath.Join(repoDir, "app", "config.py"), []byte(configPy), 0o644); err != nil {
		// os.WriteFile doesn't create parent dirs.
		if err := os.MkdirAll(filepath.Join(repoDir, "app"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repoDir, "app", "config.py"), []byte(configPy), 0o644); err != nil {
			t.Fatalf("write config.py: %v", err)
		}
	}

	// Scripted LLM responses in order:
	// 1. Planning call → plan string
	// 2. Turn 1 → read_file tool call on app/config.py
	// 3. Turn 2 → done with root cause
	responses := []string{
		"Plan: Read app/config.py to find how DATABASE_URL is accessed.",
		`{"tool": "read_file", "args": {"path": "app/config.py"}}`,
		`{"done": true, "root_cause": "KeyError: DATABASE_URL accessed via os.environ['DATABASE_URL'] in app/config.py without .get() fallback", "confidence": 0.9, "files_to_change": ["app/config.py"]}`,
	}

	a, server, callCount := makeAgent(t, responses, repoDir)
	defer server.Close()

	systemPrompt := "Pod logs:\nTraceback (most recent call last):\n  File \"app/config.py\", line 4, in load_config\n    db_url = os.environ['DATABASE_URL']\nKeyError: 'DATABASE_URL'"
	result, err := a.Run(context.Background(), systemPrompt)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if !strings.Contains(result.RootCause, "DATABASE_URL") {
		t.Errorf("RootCause should mention DATABASE_URL, got: %s", result.RootCause)
	}
	if !strings.Contains(result.RootCause, "config.py") {
		t.Errorf("RootCause should mention config.py, got: %s", result.RootCause)
	}
	if result.Confidence < 0.5 {
		t.Errorf("Confidence = %f, want >= 0.5", result.Confidence)
	}
	if result.TurnsUsed > 5 {
		t.Errorf("TurnsUsed = %d, want <= 5 (MaxTurns)", result.TurnsUsed)
	}
	// 1 planning call + 2 turn calls = 3 total LLM calls.
	got := atomic.LoadInt32(callCount)
	if got != 3 {
		t.Errorf("LLM call count = %d, want 3 (1 plan + 2 turns)", got)
	}
}

// TestAgent_E2E_RetryOnGarbageOutput exercises the retry-on-malformed-output
// path end-to-end. The fake LLM returns garbage on the first turn, then a
// valid tool call on the retry. The agent should NOT count the retry as a
// turn (TurnsUsed should be 2, not 3).
//
// This backs the PR #63 claim: "when LLM returns non-JSON, send correction
// prompt and retry (up to 2 per turn, doesn't count as a turn)".
func TestAgent_E2E_RetryOnGarbageOutput(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "app"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "app", "main.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Scripted LLM responses:
	// 1. Planning call → plan string
	// 2. Turn 1, retry 0 → GARBAGE (not JSON) — should trigger correction prompt
	// 3. Turn 1, retry 1 → valid tool call (read_file)
	// 4. Turn 2 → done with root cause
	responses := []string{
		"Plan: Read app/main.py.",
		"I think the issue is with the database.", // garbage — triggers retry
		`{"tool": "read_file", "args": {"path": "app/main.py"}}`,
		`{"done": true, "root_cause": "bug in app/main.py", "confidence": 0.85, "files_to_change": ["app/main.py"]}`,
	}

	a, server, callCount := makeAgent(t, responses, repoDir)
	defer server.Close()

	result, err := a.Run(context.Background(), "Pod logs:\nError in app/main.py")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if !strings.Contains(result.RootCause, "main.py") {
		t.Errorf("RootCause should mention main.py, got: %s", result.RootCause)
	}
	// TurnsUsed should be 2 (turn 1 with retry, turn 2 done) — the retry
	// doesn't count as a separate turn.
	if result.TurnsUsed != 2 {
		t.Errorf("TurnsUsed = %d, want 2 (retry should not count as a turn)", result.TurnsUsed)
	}
	// 1 plan + 1 garbage + 1 retry + 1 done = 4 LLM calls.
	got := atomic.LoadInt32(callCount)
	if got != 4 {
		t.Errorf("LLM call count = %d, want 4 (1 plan + 1 garbage + 1 retry + 1 done)", got)
	}
}

// TestAgent_E2E_HitTurnCap verifies the agent returns a clear "did not
// complete" result when the LLM never returns done=true. This is the
// graceful-degradation path — the agent shouldn't hang or panic.
func TestAgent_E2E_HitTurnCap(t *testing.T) {
	repoDir := t.TempDir()

	// Scripted LLM responses: plan + 5 tool calls (never done).
	// The agent should hit MaxTurns=5 and return a result with
	// Confidence=0 and a clear "did not complete" root cause.
	responses := []string{
		"Plan: keep reading files.",
		`{"tool": "read_file", "args": {"path": "app/main.py"}}`,
		`{"tool": "read_file", "args": {"path": "app/util.py"}}`,
		`{"tool": "read_file", "args": {"path": "app/main.py"}}`,
		`{"tool": "read_file", "args": {"path": "app/util.py"}}`,
		`{"tool": "read_file", "args": {"path": "app/main.py"}}`,
	}
	// MaxTurns=5 → 1 plan call + 5 turn calls = 6 LLM calls total.

	a, server, _ := makeAgent(t, responses, repoDir)
	defer server.Close()

	// Override MaxTurns to 5 explicitly (already the default, but be explicit).
	a.cfg.MaxTurns = 5

	result, err := a.Run(context.Background(), "Pod logs:\nError")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if result.Confidence != 0.0 {
		t.Errorf("Confidence = %f, want 0.0 on turn-cap exhaustion", result.Confidence)
	}
	if !strings.Contains(result.RootCause, "did not complete") {
		t.Errorf("RootCause should mention 'did not complete', got: %s", result.RootCause)
	}
	if result.TurnsUsed != 5 {
		t.Errorf("TurnsUsed = %d, want 5 (MaxTurns)", result.TurnsUsed)
	}
}

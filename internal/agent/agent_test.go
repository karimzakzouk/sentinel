package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLLMResponse_Done(t *testing.T) {
	resp := `{"done": true, "root_cause": "DATABASE_URLL typo in deployment", "confidence": 0.9, "files_to_change": ["deployment/k8s.yaml"]}`
	parsed := parseLLMResponse(resp)

	if !parsed.Done {
		t.Error("expected done=true")
	}
	if parsed.RootCause != "DATABASE_URLL typo in deployment" {
		t.Errorf("root_cause = %q", parsed.RootCause)
	}
	if parsed.Confidence != 0.9 {
		t.Errorf("confidence = %f", parsed.Confidence)
	}
	if len(parsed.FilesToChange) != 1 || parsed.FilesToChange[0] != "deployment/k8s.yaml" {
		t.Errorf("files_to_change = %v", parsed.FilesToChange)
	}
}

func TestParseLLMResponse_ToolCall(t *testing.T) {
	resp := `{"tool": "read_file", "args": {"path": "app/main.py"}}`
	parsed := parseLLMResponse(resp)

	if parsed.Done {
		t.Error("expected done=false")
	}
	if parsed.Tool != "read_file" {
		t.Errorf("tool = %q", parsed.Tool)
	}
	if parsed.Args["path"] != "app/main.py" {
		t.Errorf("args.path = %v", parsed.Args["path"])
	}
}

func TestParseLLMResponse_ProseWithJSON(t *testing.T) {
	resp := `Let me look at the deployment file.

{"tool": "read_file", "args": {"path": "deployment/k8s.yaml"}}`
	parsed := parseLLMResponse(resp)

	if parsed.Tool != "read_file" {
		t.Errorf("tool = %q, expected read_file", parsed.Tool)
	}
}

func TestParseLLMResponse_Garbage(t *testing.T) {
	resp := `I think the issue is with the database.`
	parsed := parseLLMResponse(resp)

	if parsed.Done {
		t.Error("expected done=false for non-JSON, non-done response")
	}
	if parsed.Tool != "" {
		t.Errorf("expected no tool, got %q", parsed.Tool)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("short string should not be truncated")
	}
	if truncate("hello world", 5) != "hello..." {
		t.Errorf("got %q", truncate("hello world", 5))
	}
}

func TestFormatConversation(t *testing.T) {
	conv := []map[string]string{
		{"role": "system", "content": "You are an agent."},
		{"role": "user", "content": "Investigate."},
	}
	out := formatConversation(conv)
	if !contains(out, "[system]") || !contains(out, "[user]") {
		t.Errorf("expected role markers in output")
	}
}

func TestCompactConversation(t *testing.T) {
	conv := []map[string]string{
		{"role": "system", "content": "system prompt"},
		{"role": "user", "content": "initial"},
	}
	for i := 0; i < 15; i++ {
		conv = append(conv,
			map[string]string{"role": "assistant", "content": "response"},
			map[string]string{"role": "user", "content": "Tool result: something"},
		)
	}

	compacted := compactConversation(conv, "system prompt")
	if len(compacted) >= len(conv) {
		t.Errorf("compaction should reduce conversation: %d -> %d", len(conv), len(compacted))
	}
}

func TestRegisterRepoTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644)

	a := New(nil, AgentConfig{MaxTurns: 5})
	a.RegisterRepoTools(dir)

	if len(a.tools) != 3 {
		t.Errorf("expected 3 repo tools, got %d", len(a.tools))
	}

	result := a.toolFuncs["read_file"](context.Background(), map[string]any{"path": "main.py"})
	if !result.Success {
		t.Errorf("read_file failed: %s", result.Output)
	}

	result = a.toolFuncs["search_code"](context.Background(), map[string]any{"pattern": "print"})
	if !result.Success {
		t.Errorf("search_code failed: %s", result.Output)
	}

	result = a.toolFuncs["list_files"](context.Background(), map[string]any{})
	if !result.Success {
		t.Errorf("list_files failed: %s", result.Output)
	}
}

func TestExecuteTool_Unknown(t *testing.T) {
	a := New(nil, AgentConfig{MaxTurns: 5})
	result := a.executeTool(context.Background(), "nonexistent", nil)

	if result.Success {
		t.Error("expected failure for unknown tool")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

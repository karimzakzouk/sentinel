package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLLMResponse_Done(t *testing.T) {
	resp := `{"done": true, "root_cause": "DATABASE_URLL typo", "confidence": 0.9, "files_to_change": ["deployment/k8s.yaml"]}`
	parsed, ok := parseLLMResponse(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !parsed.Done {
		t.Error("expected done=true")
	}
	if parsed.RootCause != "DATABASE_URLL typo" {
		t.Errorf("root_cause = %q", parsed.RootCause)
	}
	if parsed.Confidence != 0.9 {
		t.Errorf("confidence = %f", parsed.Confidence)
	}
}

func TestParseLLMResponse_ToolCall(t *testing.T) {
	resp := `{"tool": "read_file", "args": {"path": "app/main.py"}}`
	parsed, ok := parseLLMResponse(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Tool != "read_file" {
		t.Errorf("tool = %q", parsed.Tool)
	}
	if parsed.Args["path"] != "app/main.py" {
		t.Errorf("args.path = %v", parsed.Args["path"])
	}
}

func TestParseLLMResponse_ProseWithJSON(t *testing.T) {
	resp := `Let me look at the file.\n\n{"tool": "read_file", "args": {"path": "main.go"}}`
	parsed, ok := parseLLMResponse(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Tool != "read_file" {
		t.Errorf("tool = %q", parsed.Tool)
	}
}

func TestParseLLMResponse_Garbage(t *testing.T) {
	resp := `I think the issue is with the database.`
	_, ok := parseLLMResponse(resp)
	if ok {
		t.Error("expected ok=false for non-JSON")
	}
}

func TestParseLLMResponse_MarkdownCodeBlock(t *testing.T) {
	resp := "Here is my analysis:\n\n```json\n{\"tool\": \"search_code\", \"args\": {\"pattern\": \"DATABASE_URL\"}}\n```\n\nDone."
	parsed, ok := parseLLMResponse(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Tool != "search_code" {
		t.Errorf("tool = %q", parsed.Tool)
	}
}

func TestParseLLMResponse_GarbageWithDoneKeyword(t *testing.T) {
	resp := `I think I'm done investigating.`
	parsed, ok := parseLLMResponse(resp)
	if ok {
		t.Error("expected ok=false")
	}
	if parsed.Done {
		t.Error("expected done=false — lenient fallback removed")
	}
}

func TestStuckDetector_RepeatedSameCall(t *testing.T) {
	sd := NewStuckDetector()
	sd.Record("search_code", map[string]any{"pattern": "foo"}, ToolResult{Success: true, Output: "results"})
	sd.Record("search_code", map[string]any{"pattern": "foo"}, ToolResult{Success: true, Output: "results"})
	if sd.CheckStuck() != "" {
		t.Error("should not be stuck after 2 calls")
	}
	sd.Record("search_code", map[string]any{"pattern": "foo"}, ToolResult{Success: true, Output: "results"})
	if sd.CheckStuck() == "" {
		t.Error("should be stuck after 3 identical calls")
	}
}

func TestStuckDetector_RepeatedTool(t *testing.T) {
	sd := NewStuckDetector()
	sd.Record("search_code", map[string]any{"pattern": "a"}, ToolResult{Success: true, Output: "1"})
	sd.Record("search_code", map[string]any{"pattern": "b"}, ToolResult{Success: true, Output: "2"})
	sd.Record("search_code", map[string]any{"pattern": "c"}, ToolResult{Success: true, Output: "3"})
	sd.Record("search_code", map[string]any{"pattern": "d"}, ToolResult{Success: true, Output: "4"})
	if sd.CheckStuck() == "" {
		t.Error("should be stuck after 4 calls to same tool")
	}
}

func TestStuckDetector_NotStuck(t *testing.T) {
	sd := NewStuckDetector()
	sd.Record("read_file", map[string]any{"path": "a.go"}, ToolResult{Success: true, Output: "contents"})
	sd.Record("search_code", map[string]any{"pattern": "err"}, ToolResult{Success: true, Output: "match"})
	sd.Record("read_file", map[string]any{"path": "b.go"}, ToolResult{Success: true, Output: "contents"})
	if sd.CheckStuck() != "" {
		t.Error("should not be stuck with diverse calls")
	}
}

func TestStuckDetector_RepeatedErrors(t *testing.T) {
	sd := NewStuckDetector()
	sd.Record("search_code", map[string]any{"pattern": "foo"}, ToolResult{Success: false, Output: "No matches found."})
	sd.Record("search_code", map[string]any{"pattern": "bar"}, ToolResult{Success: false, Output: "No matches found."})
	sd.Record("search_code", map[string]any{"pattern": "baz"}, ToolResult{Success: false, Output: "No matches found."})
	msg := sd.CheckStuck()
	if msg == "" {
		t.Error("should be stuck after 3 failed calls")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("short string should not truncate")
	}
	if truncate("hello world", 5) != "hello..." {
		t.Errorf("got %q", truncate("hello world", 5))
	}
}

func TestRegisterRepoTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644)
	a := New(nil, AgentConfig{MaxTurns: 5})
	a.RegisterRepoTools(dir)
	if len(a.tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(a.tools))
	}
	result := a.toolFuncs["read_file"](context.Background(), map[string]any{"path": "main.py"})
	if !result.Success {
		t.Errorf("read_file failed: %s", result.Output)
	}
}

package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestReproducerGenerate_Success verifies the happy path: the LLM returns a
// valid reproducer script and it's parsed into a GeneratedReproducer.
func TestReproducerGenerate_Success(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"script_file":     "reproduce_error.py",
		"script_language": "python",
		"script_content":  "#!/usr/bin/env python3\nimport sys\nfrom app.main import format_price\ntry:\n    result = format_price(50)\n    print(f'Result: {result}')\n    sys.exit(0)\nexcept Exception as e:\n    print(f'CRASH: {e}', file=sys.stderr)\n    sys.exit(1)",
		"run_command":     "python /app/reproduce_error.py",
		"description":     "Calls format_price(50)",
	})
	server := httptest.NewServer(llmJSONResponseFunc(t, string(content)))
	defer server.Close()

	g := NewReproducerGenerator(llm.New(server.URL, "k", "m", "f"))
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "TypeError in format_price",
			Confidence: 0.8,
		},
		FixProposal: &schemas.FixProposal{
			Description: "Convert int to str before concatenation",
		},
		FetchedCode: map[string]string{"app/main.py": "def format_price(p): return 'Price: ' + p"},
	}
	result, err := g.Generate(context.Background(), state)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result.ScriptFile != "reproduce_error.py" {
		t.Errorf("ScriptFile = %q, want reproduce_error.py", result.ScriptFile)
	}
	if result.ScriptLanguage != "python" {
		t.Errorf("ScriptLanguage = %q, want python", result.ScriptLanguage)
	}
	if !strings.Contains(result.ScriptContent, "format_price") {
		t.Errorf("expected format_price in script content, got: %s", result.ScriptContent)
	}
	if result.RunCommand != "python /app/reproduce_error.py" {
		t.Errorf("RunCommand = %q", result.RunCommand)
	}
	if result.Description != "Calls format_price(50)" {
		t.Errorf("Description = %q", result.Description)
	}
}

// TestReproducerGenerate_NoRootCause verifies the precondition check.
func TestReproducerGenerate_NoRootCause(t *testing.T) {
	g := NewReproducerGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		FixProposal: &schemas.FixProposal{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for missing root cause")
	}
	if !strings.Contains(err.Error(), "root cause") {
		t.Errorf("expected 'root cause' in error, got: %v", err)
	}
}

// TestReproducerGenerate_NoFixProposal verifies the precondition check.
func TestReproducerGenerate_NoFixProposal(t *testing.T) {
	g := NewReproducerGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		RootCause: &schemas.RootCause{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for missing fix proposal")
	}
	if !strings.Contains(err.Error(), "fix proposal") {
		t.Errorf("expected 'fix proposal' in error, got: %v", err)
	}
}

// TestReproducerGenerate_EmptyContent verifies that an LLM response missing
// the script_file or script_content returns an error.
func TestReproducerGenerate_EmptyContent(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
	}{
		{"missing script_file", map[string]any{
			"script_language": "python",
			"script_content":  "print('hello')",
		}},
		{"missing script_content", map[string]any{
			"script_file":     "reproduce.py",
			"script_language": "python",
		}},
		{"both empty", map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, _ := json.Marshal(c.resp)
			server := httptest.NewServer(llmJSONResponseFunc(t, string(content)))
			defer server.Close()

			g := NewReproducerGenerator(llm.New(server.URL, "k", "m", "f"))
			state := &schemas.AgentState{
				RootCause:   &schemas.RootCause{},
				FixProposal: &schemas.FixProposal{},
			}
			_, err := g.Generate(context.Background(), state)
			if err == nil {
				t.Fatal("expected error for empty LLM response")
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Errorf("expected 'empty' in error, got: %v", err)
			}
		})
	}
}

// TestReproducerGenerate_LLMFailure verifies that an LLM error is wrapped
// with the "reproducer LLM call" prefix.
func TestReproducerGenerate_LLMFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)
	g := NewReproducerGenerator(router)
	state := &schemas.AgentState{
		RootCause:   &schemas.RootCause{},
		FixProposal: &schemas.FixProposal{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for LLM failure")
	}
	if !strings.Contains(err.Error(), "reproducer LLM call") {
		t.Errorf("expected 'reproducer LLM call' in error, got: %v", err)
	}
}

// TestReproducerBuildUserMessage verifies that the user message includes the
// root cause, error context, source code, and language hint.
func TestReproducerBuildUserMessage(t *testing.T) {
	g := NewReproducerGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service: "api",
			Anomalies: []schemas.Anomaly{{
				Explanation: `Traceback (most recent call last):
  File "app/main.py", line 10
TypeError: can't concatenate str + int`,
			}},
		},
		RootCause: &schemas.RootCause{Statement: "TypeError in format_price"},
		FixProposal: &schemas.FixProposal{
			Description: "Convert int to str",
		},
		FetchedCode: map[string]string{
			"app/main.py": "def format_price(p): return 'Price: ' + p",
		},
	}
	msg := g.buildUserMessage(state)
	if !strings.Contains(msg, "TypeError in format_price") {
		t.Errorf("expected root cause in message")
	}
	if !strings.Contains(msg, "TypeError: can't concatenate") {
		t.Errorf("expected error context in message")
	}
	if !strings.Contains(msg, "def format_price") {
		t.Errorf("expected source code in message")
	}
	if !strings.Contains(msg, "Convert int to str") {
		t.Errorf("expected fix description in message")
	}
	if !strings.Contains(msg, "Detected Language: python") {
		t.Errorf("expected language hint in message")
	}
}

// TestReproducerBuildUserMessage_NoRootCause verifies that a missing root
// cause returns an empty message (graceful degradation).
func TestReproducerBuildUserMessage_NoRootCause(t *testing.T) {
	g := NewReproducerGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		FixProposal: &schemas.FixProposal{},
	}
	msg := g.buildUserMessage(state)
	if msg != "" {
		t.Errorf("expected empty message for missing root cause, got: %s", msg)
	}
}

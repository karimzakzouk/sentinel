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

// TestTestGenerator_Success verifies the happy path: the LLM returns a valid
// regression test and it's parsed into a GeneratedTest.
func TestTestGenerator_Success(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"test_file":      "tests/test_format_price.py",
		"test_framework": "pytest",
		"test_language":  "python",
		"test_content":   "import pytest\nfrom app.main import format_price\n\ndef test_format_price_with_integer():\n    result = format_price(50)\n    assert result == \"Price: 50\"",
		"test_command":   "pytest tests/test_format_price.py -v",
		"description":    "Tests that format_price handles integer input",
	})
	server := httptest.NewServer(llmJSONResponseFunc(t, string(content)))
	defer server.Close()

	g := NewTestGenerator(llm.New(server.URL, "k", "m", "f"))
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api", Namespace: "default"},
		RootCause: &schemas.RootCause{
			Statement:  "TypeError in format_price",
			Category:   "bug",
			Confidence: 0.8,
		},
		FixProposal: &schemas.FixProposal{
			Description: "Convert int to str",
			FilesToChange: []schemas.FileChange{{
				Path: "app/main.py",
				Changes: []schemas.SearchReplace{{
					Search:  "    return 'Price: ' + p",
					Replace: "    return 'Price: ' + str(p)",
				}},
			}},
		},
		FetchedCode: map[string]string{"app/main.py": "def format_price(p): return 'Price: ' + p"},
	}
	result, err := g.Generate(context.Background(), state)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result.TestFile != "tests/test_format_price.py" {
		t.Errorf("TestFile = %q", result.TestFile)
	}
	if result.TestFramework != "pytest" {
		t.Errorf("TestFramework = %q", result.TestFramework)
	}
	if result.TestLanguage != "python" {
		t.Errorf("TestLanguage = %q", result.TestLanguage)
	}
	if !strings.Contains(result.TestContent, "def test_format_price_with_integer") {
		t.Errorf("expected test function in content, got: %s", result.TestContent)
	}
	if result.TestCommand != "pytest tests/test_format_price.py -v" {
		t.Errorf("TestCommand = %q", result.TestCommand)
	}
}

// TestTestGenerator_NoRootCause verifies the precondition check.
func TestTestGenerator_NoRootCause(t *testing.T) {
	g := NewTestGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		FixProposal: &schemas.FixProposal{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for missing root cause")
	}
}

// TestTestGenerator_NoFixProposal verifies the precondition check.
func TestTestGenerator_NoFixProposal(t *testing.T) {
	g := NewTestGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		RootCause: &schemas.RootCause{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for missing fix proposal")
	}
}

// TestTestGenerator_EmptyContent verifies that an LLM response missing the
// test_file or test_content returns an error.
func TestTestGenerator_EmptyContent(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
	}{
		{"missing test_file", map[string]any{
			"test_framework": "pytest",
			"test_content":   "def test_x(): assert True",
		}},
		{"missing test_content", map[string]any{
			"test_file": "test_x.py",
		}},
		{"both empty", map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, _ := json.Marshal(c.resp)
			server := httptest.NewServer(llmJSONResponseFunc(t, string(content)))
			defer server.Close()

			g := NewTestGenerator(llm.New(server.URL, "k", "m", "f"))
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

// TestTestGenerator_LLMFailure verifies that an LLM error is wrapped with the
// "test generation LLM call" prefix.
func TestTestGenerator_LLMFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	router := llm.New(server.URL, "k", "m", "f")
	router.SetRetryConfig(0, 0)
	g := NewTestGenerator(router)
	state := &schemas.AgentState{
		RootCause:   &schemas.RootCause{},
		FixProposal: &schemas.FixProposal{},
	}
	_, err := g.Generate(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for LLM failure")
	}
	if !strings.Contains(err.Error(), "test generation LLM call") {
		t.Errorf("expected 'test generation LLM call' in error, got: %v", err)
	}
}

// TestTestGeneratorBuildUserMessage verifies that the user message includes
// the root cause, original source code, proposed fix, error context, and
// language hint.
func TestTestGeneratorBuildUserMessage(t *testing.T) {
	g := NewTestGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service: "api",
			Anomalies: []schemas.Anomaly{{
				Explanation: `Traceback (most recent call last):
  File "app/main.py", line 10
TypeError: can't concatenate str + int`,
			}},
		},
		RootCause: &schemas.RootCause{
			Statement: "TypeError in format_price",
			Category:  "bug",
		},
		FixProposal: &schemas.FixProposal{
			Description: "Convert int to str",
			FilesToChange: []schemas.FileChange{{
				Path: "app/main.py",
				Changes: []schemas.SearchReplace{{
					Search:  "    return 'Price: ' + p",
					Replace: "    return 'Price: ' + str(p)",
				}},
			}},
		},
		FetchedCode: map[string]string{"app/main.py": "def format_price(p): return 'Price: ' + p"},
	}
	msg := g.buildUserMessage(state)
	if !strings.Contains(msg, "TypeError in format_price") {
		t.Errorf("expected root cause in message")
	}
	if !strings.Contains(msg, "Original Source Code") {
		t.Errorf("expected original source code section in message")
	}
	if !strings.Contains(msg, "Proposed Fix") {
		t.Errorf("expected proposed fix section in message")
	}
	if !strings.Contains(msg, "Error Context") {
		t.Errorf("expected error context section in message")
	}
	if !strings.Contains(msg, "Detected Language: python") {
		t.Errorf("expected language hint in message")
	}
}

// TestTestGeneratorBuildUserMessage_NoRootCause verifies that a missing root
// cause returns an empty message (graceful degradation).
func TestTestGeneratorBuildUserMessage_NoRootCause(t *testing.T) {
	g := NewTestGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		FixProposal: &schemas.FixProposal{},
	}
	msg := g.buildUserMessage(state)
	if msg != "" {
		t.Errorf("expected empty message for missing root cause, got: %s", msg)
	}
}

// TestDetectLanguageFromState verifies language detection from the anomaly
// explanations.
func TestDetectLanguageFromState(t *testing.T) {
	cases := []struct {
		name string
		anom []schemas.Anomaly
		want string
	}{
		{
			name: "python KeyError",
			anom: []schemas.Anomaly{{Explanation: `Traceback (most recent call last):
  File "x.py", line 1
KeyError: 'foo'`}},
			want: "python",
		},
		{
			name: "go panic",
			anom: []schemas.Anomaly{{Explanation: `panic: runtime error: index out of range
goroutine 1 [running]:
main.foo()
	/app/main.go:10`}},
			want: "go",
		},
		{
			name: "empty anomalies",
			anom: []schemas.Anomaly{},
			want: "",
		},
		{
			name: "unparseable logs",
			anom: []schemas.Anomaly{{Explanation: "no error here"}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := &schemas.AgentState{
				Cluster: schemas.AnomalyCluster{Anomalies: c.anom},
			}
			got := detectLanguageFromState(state)
			if got != c.want {
				t.Errorf("detectLanguageFromState() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestTestGenerator_WholeFileChangeInMessage verifies that a whole-file
// replacement in the fix proposal is rendered correctly in the user message.
func TestTestGenerator_WholeFileChangeInMessage(t *testing.T) {
	g := NewTestGenerator(llm.New("", "", "", ""))
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{Service: "api"},
		RootCause: &schemas.RootCause{
			Statement: "TypeError in format_price",
			Category:  "bug",
		},
		FixProposal: &schemas.FixProposal{
			Description: "Convert int to str",
			FilesToChange: []schemas.FileChange{{
				Path:       "app/main.py",
				NewContent: "def format_price(p): return 'Price: ' + str(p)",
			}},
		},
		FetchedCode: map[string]string{"app/main.py": "old content"},
	}
	msg := g.buildUserMessage(state)
	if !strings.Contains(msg, "New content:") {
		t.Errorf("expected 'New content:' label for whole-file change, got: %s", msg)
	}
	if !strings.Contains(msg, "def format_price(p): return 'Price: ' + str(p)") {
		t.Errorf("expected new content in message")
	}
}

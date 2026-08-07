package agents

import (
	"encoding/json"
	"net/http"
	"testing"
)

// llmJSONResponse builds an OpenAI-style JSON response with the given content
// string and writes it to w. Use this in tests to mock the LLM API. The
// content is JSON-encoded so newlines, quotes, and backslashes are escaped
// correctly.
func llmJSONResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}, "finish_reason": "stop"},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal LLM response: %v", err)
	}
	w.WriteHeader(200)
	w.Write(b)
}

// llmJSONResponseFunc returns an http.HandlerFunc that responds with the
// given content on every call.
func llmJSONResponseFunc(t *testing.T, content string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		llmJSONResponse(t, w, content)
	}
}

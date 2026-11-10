package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each format sends the model, the prompts and the key the way its APIs
// expect, and reads the reply's text.
func TestBothFormatsAskAndRead(t *testing.T) {
	for _, tc := range []struct {
		format       Format
		path, header string
		reply        string
	}{
		{OpenAI, "/v1/chat/completions", "Authorization", `{"choices":[{"message":{"content":"FILE: a.py"}}]}`},
		{Anthropic, "/v1/messages", "X-Api-Key", `{"content":[{"type":"text","text":"FILE: "},{"type":"text","text":"a.py"}]}`},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Header.Get(tc.header) == "" {
					http.Error(w, "wrong request "+r.URL.Path, http.StatusBadRequest)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&got)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			c := &Client{Format: tc.format, URL: srv.URL + "/v1", Model: "m", Key: "k"}
			text, err := c.Complete(context.Background(), "be brief", "fix it")
			if err != nil || text != "FILE: a.py" {
				t.Fatalf("%q, %v", text, err)
			}
			if got["model"] != "m" || got["temperature"] != float64(0) {
				t.Fatalf("sent %v", got)
			}
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), "be brief") || !strings.Contains(string(raw), "fix it") {
				t.Fatalf("the prompts are missing from %s", raw)
			}
		})
	}
}

// An error from the API says what it said, never with the key in it.
func TestAnAPIErrorHidesTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad key sk-secret"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := (&Client{URL: srv.URL, Model: "m", Key: "sk-secret"}).Complete(context.Background(), "s", "u")
	if err == nil || strings.Contains(err.Error(), "sk-secret") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("%v", err)
	}
}

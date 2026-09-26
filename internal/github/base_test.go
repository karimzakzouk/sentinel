package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Run #28: the revert-commit payload marshalled "parents" as a JSON
// STRING ("[\"sha\"]"), GitHub rejected it with 422, and the auto-revert
// of a regressed merge silently never happened. The typed struct must
// keep parents a real array.
func TestRevertCommitPayloadParentsIsArray(t *testing.T) {
	b, err := json.Marshal(revertCommitPayload{
		Message: "revert: abc",
		Tree:    "tree-sha",
		Parents: []string{"head-sha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"parents":["head-sha"]`) {
		t.Errorf("parents must marshal as a JSON array, got %s", s)
	}
	if strings.Contains(s, `"parents":"`) {
		t.Errorf("parents must NOT be a string, got %s", s)
	}
}

// TestRevertMergeCommitEndToEnd runs the full revert flow against a mock
// GitHub API and pins BOTH run-#28/#29 bugs: parents must be a JSON array
// (not a string), and the tree must come from the parent commit's
// top-level tree (parents entries carry no tree field).
func TestRevertMergeCommitEndToEnd(t *testing.T) {
	const (
		mergeSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // the merge commit
		parentSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // pre-merge code state
		parentTr  = "cccccccccccccccccccccccccccccccccccccccc" // 40-hex tree of parent
		headSHA   = "dddddddddddddddddddddddddddddddddddddddd" // current base HEAD
		newCommit = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	)
	var gotCommitPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/commits/"+mergeSHA):
			json.NewEncoder(w).Encode(map[string]any{
				"sha":      mergeSHA,
				"tree":     map[string]string{"sha": "irrelevant-top-tree"},
				"parents":  []map[string]string{{"sha": parentSHA}}, // NOTE: no tree inside parents
				"commit":   map[string]string{"message": "merge"},
				"html_url": "https://x",
			})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/commits/"+parentSHA):
			json.NewEncoder(w).Encode(map[string]any{
				"sha":     parentSHA,
				"tree":    map[string]string{"sha": parentTr},
				"parents": []map[string]string{{"sha": "root"}},
			})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/ref"):
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": headSHA},
			})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/commits"):
			body, _ := io.ReadAll(r.Body)
			gotCommitPayload = map[string]any{}
			if err := json.Unmarshal(body, &gotCommitPayload); err != nil {
				t.Errorf("git/commits payload not JSON: %v", err)
			}
			json.NewEncoder(w).Encode(map[string]string{"sha": newCommit})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": newCommit},
			})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"number":   42,
				"html_url": "https://github.com/x/y/pull/42",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	bc := &BaseClient{Token: "t", APIURL: srv.URL, HTTP: srv.Client()}
	pr, err := bc.RevertMergeCommit(context.Background(), "owner/repo", mergeSHA, "main", "regression")
	if err != nil {
		t.Fatalf("RevertMergeCommit: %v", err)
	}
	if pr != 42 {
		t.Errorf("pr = %d, want 42", pr)
	}

	// Pin run #28: parents must be a real JSON array of SHA strings.
	parents, ok := gotCommitPayload["parents"].([]any)
	if !ok || len(parents) != 1 || parents[0] != headSHA {
		t.Errorf("parents = %#v, want [%s] as a JSON array", gotCommitPayload["parents"], headSHA)
	}
	// Pin run #29: tree must be the PARENT commit's tree, 40 hex chars.
	if gotCommitPayload["tree"] != parentTr {
		t.Errorf("tree = %#v, want the parent commit's tree %s", gotCommitPayload["tree"], parentTr)
	}
}

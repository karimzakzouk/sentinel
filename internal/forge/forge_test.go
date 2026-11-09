package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Only repositories on the forge itself have an owner and name; the token
// goes nowhere else.
func TestRepoAcceptsOnlyTheForgesRepositories(t *testing.T) {
	c := &Client{Web: "http://gitea-http.gitea.svc:3000"}
	for in, want := range map[string]string{
		"http://gitea-http.gitea.svc:3000/sentinel/delivery.git": "sentinel/delivery",
		"http://gitea-http.gitea.svc:3000/sentinel/delivery":     "sentinel/delivery",
		"https://gitea-http.gitea.svc:3000/sentinel/delivery":    "",
		"http://evil.example/sentinel/delivery.git":              "",
		"http://user@gitea-http.gitea.svc:3000/sentinel/x.git":   "",
		"http://gitea-http.gitea.svc:3000/sentinel/a/b.git":      "",
		"ssh://git@gitea-http.gitea.svc:3000/sentinel/x.git":     "",
	} {
		owner, name, err := c.Repo(in)
		if got := owner + "/" + name; (want == "" && err == nil) || (want != "" && got != want) {
			t.Errorf("Repo(%s) = %s, %v; want %q", in, got, err, want)
		}
	}
}

// A pull request is opened with the token and read back.
func TestOpenAndGetAPullRequest(t *testing.T) {
	var opened map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token s3cret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/sentinel/delivery/pulls":
			_ = json.NewDecoder(r.Body).Decode(&opened)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number": 7, "html_url": "http://gitea/sentinel/delivery/pulls/7", "state": "open", "merged": false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/sentinel/delivery/pulls/7":
			_, _ = w.Write([]byte(`{"number": 7, "state": "closed", "merged": true, "merge_commit_sha": "abc123"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "s3cret"}
	ctx := context.Background()
	pr, err := c.Open(ctx, "sentinel", "delivery", "sentinel/fix-1", "main", "Fix", "Because")
	if err != nil || pr.Number != 7 || opened["head"] != "sentinel/fix-1" || opened["base"] != "main" {
		t.Fatalf("Open = %+v, %v; sent %v", pr, err, opened)
	}
	pr, err = c.Get(ctx, "sentinel", "delivery", 7)
	if err != nil || !pr.Merged || pr.MergeCommit != "abc123" {
		t.Fatalf("Get = %+v, %v", pr, err)
	}
	c.Token = "wrong"
	if _, err := c.Get(ctx, "sentinel", "delivery", 7); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("with a wrong token: %v", err)
	}
}

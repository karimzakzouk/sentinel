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
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/sentinel/delivery/pulls/7/merge":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["Do"] != "merge" {
				http.Error(w, "no merge style", http.StatusUnprocessableEntity)
			}
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
	if err := c.Merge(ctx, "sentinel", "delivery", 7); err != nil {
		t.Fatalf("Merge: %v", err)
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

// Repositories are named as each forge names them: GitLab's under groups
// and subgroups, Bitbucket Data Center's under a project key.
func TestRepoReadsEachForgesPaths(t *testing.T) {
	for _, tc := range []struct {
		kind      Kind
		web, url  string
		owner, nm string
	}{
		{GitLab, "https://gitlab.example", "https://gitlab.example/shop/backend/cart.git", "shop/backend", "cart"},
		{GitLab, "https://gitlab.example", "https://gitlab.example/shop.git", "", ""},
		{Bitbucket, "https://bitbucket.org", "https://bitbucket.org/acme/delivery.git", "acme", "delivery"},
		{BitbucketServer, "https://bb.example", "https://bb.example/scm/OPS/delivery.git", "OPS", "delivery"},
		{BitbucketServer, "https://bb.example", "https://bb.example/projects/OPS/repos/delivery", "", ""},
	} {
		c := &Client{Kind: tc.kind, Web: tc.web}
		owner, name, err := c.Repo(tc.url)
		if tc.owner == "" {
			if err == nil {
				t.Errorf("%s %s: accepted as %s/%s", tc.kind, tc.url, owner, name)
			}
			continue
		}
		if err != nil || owner != tc.owner || name != tc.nm {
			t.Errorf("%s %s = %s/%s, %v", tc.kind, tc.url, owner, name, err)
		}
	}
}

// Each forge's own API opens, comments on, merges and reads a pull
// request, which reads back the same way for all of them.
func TestEachForgeOpensMergesAndReadsAPullRequest(t *testing.T) {
	type route struct{ method, path, reply string }
	for _, tc := range []struct {
		kind   Kind
		api    string
		auth   [2]string
		owner  string
		routes []route
		sent   string
	}{
		{GitLab, "/api/v4", [2]string{"PRIVATE-TOKEN", "s3cret"}, "shop/backend", []route{
			{"POST", "/api/v4/projects/shop%2Fbackend%2Fcart/merge_requests", `{"iid": 7, "web_url": "https://gl/mr/7", "state": "opened"}`},
			{"POST", "/api/v4/projects/shop%2Fbackend%2Fcart/merge_requests/7/notes", `{}`},
			{"PUT", "/api/v4/projects/shop%2Fbackend%2Fcart/merge_requests/7/merge", `{}`},
			{"GET", "/api/v4/projects/shop%2Fbackend%2Fcart/merge_requests/7", `{"iid": 7, "state": "merged", "merge_commit_sha": null, "sha": "ff0123"}`},
		}, "source_branch"},
		{Bitbucket, "/2.0", [2]string{"Authorization", "Bearer s3cret"}, "acme", []route{
			{"POST", "/2.0/repositories/acme/cart/pullrequests", `{"id": 7, "state": "OPEN", "links": {"html": {"href": "https://bb/pr/7"}}}`},
			{"POST", "/2.0/repositories/acme/cart/pullrequests/7/comments", `{}`},
			{"POST", "/2.0/repositories/acme/cart/pullrequests/7/merge", `{}`},
			{"GET", "/2.0/repositories/acme/cart/pullrequests/7", `{"id": 7, "state": "MERGED", "merge_commit": {"hash": "ff0123"}}`},
		}, "destination"},
		{BitbucketServer, "/rest/api/1.0", [2]string{"Authorization", "Bearer s3cret"}, "OPS", []route{
			{"POST", "/rest/api/1.0/projects/OPS/repos/cart/pull-requests", `{"id": 7, "version": 0, "state": "OPEN", "links": {"self": [{"href": "https://bbs/pr/7"}]}}`},
			{"POST", "/rest/api/1.0/projects/OPS/repos/cart/pull-requests/7/comments", `{}`},
			{"POST", "/rest/api/1.0/projects/OPS/repos/cart/pull-requests/7/merge", `{}`},
			{"GET", "/rest/api/1.0/projects/OPS/repos/cart/pull-requests/7", `{"id": 7, "version": 3, "state": "MERGED"}`},
			{"GET", "/rest/api/1.0/projects/OPS/repos/cart/pull-requests/7/activities", `{"values": [{"action": "APPROVED"}, {"action": "MERGED", "commit": {"id": "ff0123"}}]}`},
		}, "fromRef"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var opened map[string]any
			var mergedAt string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(tc.auth[0]) != tc.auth[1] {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				for _, rt := range tc.routes {
					if r.Method == rt.method && r.URL.EscapedPath() == rt.path {
						if strings.HasSuffix(rt.path, "/merge") {
							mergedAt = r.URL.RawQuery
						}
						if r.Method == http.MethodPost && opened == nil {
							_ = json.NewDecoder(r.Body).Decode(&opened)
						}
						_, _ = w.Write([]byte(rt.reply))
						return
					}
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()
			c := &Client{Kind: tc.kind, Web: srv.URL, API: srv.URL + tc.api, Token: "s3cret"}
			ctx := context.Background()
			pr, err := c.Open(ctx, tc.owner, "cart", "sentinel/fix-1", "main", "Fix", "Because")
			if err != nil || pr.Number != 7 || pr.State != "open" || pr.URL == "" || opened[tc.sent] == nil {
				t.Fatalf("Open = %+v, %v; sent %v", pr, err, opened)
			}
			if err := c.Comment(ctx, tc.owner, "cart", 7, "verdict"); err != nil {
				t.Fatalf("Comment: %v", err)
			}
			if err := c.Merge(ctx, tc.owner, "cart", 7); err != nil {
				t.Fatalf("Merge: %v", err)
			}
			if tc.kind == BitbucketServer && mergedAt != "version=3" {
				t.Fatalf("merged at version %q", mergedAt)
			}
			pr, err = c.Get(ctx, tc.owner, "cart", 7)
			if err != nil || !pr.Merged || pr.State != "closed" || pr.MergeCommit != "ff0123" {
				t.Fatalf("Get = %+v, %v", pr, err)
			}
		})
	}
}

// Package forge opens, merges, comments on and reads pull requests on the
// forges a source may live on: GitHub, Gitea, GitLab, Bitbucket Cloud and
// Bitbucket Data Center. Each speaks its own API; the Fixer sees one shape.
package forge

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Kind names a forge's API.
type Kind string

// The forges the Fixer works with.
const (
	GitHub    Kind = "github"
	Gitea     Kind = "gitea"
	GitLab    Kind = "gitlab"
	Bitbucket Kind = "bitbucket"
	// BitbucketServer is Bitbucket Data Center, formerly Server.
	BitbucketServer Kind = "bitbucket-server"
)

// Kinds lists every forge, for configuration to check against.
var Kinds = []Kind{GitHub, Gitea, GitLab, Bitbucket, BitbucketServer}

// Client talks to one forge.
type Client struct {
	// Kind is the forge's API; empty means GitHub at api.github.com and
	// Gitea anywhere else.
	Kind Kind
	// Web is the forge's address, which its repositories' URLs start with:
	// https://github.com, https://gitlab.com, https://bitbucket.org, or a
	// server's root URL.
	Web string
	// API is its API's base URL: https://api.github.com, a Gitea root URL
	// followed by /api/v1, a GitLab one by /api/v4,
	// https://api.bitbucket.org/2.0, or a Bitbucket Data Center root URL
	// followed by /rest/api/1.0.
	API   string
	Token string
	HTTP  *http.Client
}

// PullRequest is what the Fixer reads of a pull request, or merge request.
type PullRequest struct {
	Number int
	URL    string
	// State is open or closed; a merged one is closed.
	State       string
	Merged      bool
	MergeCommit string
}

func (c *Client) kind() Kind {
	if c.Kind != "" {
		return c.Kind
	}
	if api, err := url.Parse(c.API); err == nil && api.Host == "api.github.com" {
		return GitHub
	}
	return Gitea
}

// Repo returns the owner and name of a repository on this forge from its
// clone URL: a GitLab owner may be a group with subgroups, and a Bitbucket
// Data Center owner is a project key. A URL anywhere else is refused, so
// the forge's token is never sent to another host.
func (c *Client) Repo(cloneURL string) (owner, name string, err error) {
	web, err := url.Parse(c.Web)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != web.Scheme || u.Host != web.Host || u.User != nil {
		return "", "", fmt.Errorf("%s is not a repository on %s", cloneURL, c.Web)
	}
	rest, ok := strings.CutPrefix(u.Path, strings.TrimSuffix(web.Path, "/")+"/")
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git"), "/")
	want := "OWNER/NAME"
	switch c.kind() {
	case GitLab:
		want = "GROUP/NAME"
		ok = ok && len(parts) >= 2
	case BitbucketServer:
		want = "scm/PROJECT/NAME"
		ok = ok && len(parts) == 3 && parts[0] == "scm"
		parts = parts[min(1, len(parts)):]
	default:
		ok = ok && len(parts) == 2
	}
	for _, p := range parts {
		ok = ok && p != ""
	}
	if !ok {
		return "", "", fmt.Errorf("%s does not name a repository as %s on %s", cloneURL, want, c.Web)
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1], nil
}

// Open opens a pull request from the head branch into the base branch.
func (c *Client) Open(ctx context.Context, owner, repo, head, base, title, body string) (PullRequest, error) {
	switch c.kind() {
	case GitLab:
		var mr gitlabMR
		err := c.call(ctx, http.MethodPost, gitlabProject(owner, repo)+"/merge_requests",
			map[string]string{"source_branch": head, "target_branch": base, "title": title, "description": body}, &mr)
		return mr.pull(), err
	case Bitbucket:
		var pr bitbucketPR
		err := c.call(ctx, http.MethodPost, bitbucketRepo(owner, repo)+"/pullrequests", map[string]any{
			"title": title, "description": body,
			"source":      map[string]any{"branch": map[string]string{"name": head}},
			"destination": map[string]any{"branch": map[string]string{"name": base}},
		}, &pr)
		return pr.pull(), err
	case BitbucketServer:
		var pr serverPR
		err := c.call(ctx, http.MethodPost, serverRepo(owner, repo)+"/pull-requests", map[string]any{
			"title": title, "description": body,
			"fromRef": map[string]string{"id": "refs/heads/" + head},
			"toRef":   map[string]string{"id": "refs/heads/" + base},
		}, &pr)
		return pr.pull(), err
	}
	var pr githubPR
	err := c.call(ctx, http.MethodPost, githubRepo(owner, repo)+"/pulls",
		map[string]string{"head": head, "base": base, "title": title, "body": body}, &pr)
	return pr.pull(), err
}

// Merge merges a pull request with a merge commit.
func (c *Client) Merge(ctx context.Context, owner, repo string, number int) error {
	n := strconv.Itoa(number)
	switch c.kind() {
	case GitHub:
		return c.call(ctx, http.MethodPut, githubRepo(owner, repo)+"/pulls/"+n+"/merge", map[string]string{"merge_method": "merge"}, nil)
	case GitLab:
		return c.call(ctx, http.MethodPut, gitlabProject(owner, repo)+"/merge_requests/"+n+"/merge", map[string]any{}, nil)
	case Bitbucket:
		return c.call(ctx, http.MethodPost, bitbucketRepo(owner, repo)+"/pullrequests/"+n+"/merge",
			map[string]string{"merge_strategy": "merge_commit"}, nil)
	case BitbucketServer:
		var pr serverPR
		if err := c.call(ctx, http.MethodGet, serverRepo(owner, repo)+"/pull-requests/"+n, nil, &pr); err != nil {
			return err
		}
		return c.call(ctx, http.MethodPost, serverRepo(owner, repo)+"/pull-requests/"+n+"/merge?version="+strconv.Itoa(pr.Version), map[string]any{}, nil)
	}
	return c.call(ctx, http.MethodPost, githubRepo(owner, repo)+"/pulls/"+n+"/merge", map[string]string{"Do": "merge"}, nil)
}

// Comment adds a comment to a pull request.
func (c *Client) Comment(ctx context.Context, owner, repo string, number int, body string) error {
	n := strconv.Itoa(number)
	switch c.kind() {
	case GitLab:
		return c.call(ctx, http.MethodPost, gitlabProject(owner, repo)+"/merge_requests/"+n+"/notes", map[string]string{"body": body}, nil)
	case Bitbucket:
		return c.call(ctx, http.MethodPost, bitbucketRepo(owner, repo)+"/pullrequests/"+n+"/comments",
			map[string]any{"content": map[string]string{"raw": body}}, nil)
	case BitbucketServer:
		return c.call(ctx, http.MethodPost, serverRepo(owner, repo)+"/pull-requests/"+n+"/comments", map[string]string{"text": body}, nil)
	}
	return c.call(ctx, http.MethodPost, githubRepo(owner, repo)+"/issues/"+n+"/comments", map[string]string{"body": body}, nil)
}

// Get reads a pull request.
func (c *Client) Get(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	n := strconv.Itoa(number)
	switch c.kind() {
	case GitLab:
		var mr gitlabMR
		err := c.call(ctx, http.MethodGet, gitlabProject(owner, repo)+"/merge_requests/"+n, nil, &mr)
		return mr.pull(), err
	case Bitbucket:
		var pr bitbucketPR
		err := c.call(ctx, http.MethodGet, bitbucketRepo(owner, repo)+"/pullrequests/"+n, nil, &pr)
		return pr.pull(), err
	case BitbucketServer:
		var pr serverPR
		if err := c.call(ctx, http.MethodGet, serverRepo(owner, repo)+"/pull-requests/"+n, nil, &pr); err != nil {
			return PullRequest{}, err
		}
		out := pr.pull()
		if out.Merged && out.MergeCommit == "" {
			var activities struct {
				Values []struct {
					Action string `json:"action"`
					Commit struct {
						ID string `json:"id"`
					} `json:"commit"`
				} `json:"values"`
			}
			if err := c.call(ctx, http.MethodGet, serverRepo(owner, repo)+"/pull-requests/"+n+"/activities", nil, &activities); err != nil {
				return PullRequest{}, err
			}
			for _, a := range activities.Values {
				if a.Action == "MERGED" && a.Commit.ID != "" {
					out.MergeCommit = a.Commit.ID
					break
				}
			}
		}
		return out, nil
	}
	var pr githubPR
	err := c.call(ctx, http.MethodGet, githubRepo(owner, repo)+"/pulls/"+n, nil, &pr)
	return pr.pull(), err
}

func githubRepo(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// gitlabProject names a project by its path, a group's slashes escaped.
func gitlabProject(owner, repo string) string {
	return "/projects/" + url.PathEscape(owner+"/"+repo)
}

func bitbucketRepo(owner, repo string) string {
	return "/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func serverRepo(owner, repo string) string {
	return "/projects/" + url.PathEscape(owner) + "/repos/" + url.PathEscape(repo)
}

type githubPR struct {
	Number      int    `json:"number"`
	URL         string `json:"html_url"`
	State       string `json:"state"`
	Merged      bool   `json:"merged"`
	MergeCommit string `json:"merge_commit_sha"`
}

func (p githubPR) pull() PullRequest {
	return PullRequest(p)
}

type gitlabMR struct {
	IID         int    `json:"iid"`
	URL         string `json:"web_url"`
	State       string `json:"state"`
	MergeCommit string `json:"merge_commit_sha"`
	Squash      string `json:"squash_commit_sha"`
	Head        string `json:"sha"`
}

// pull reads a merge request; a fast-forward merge has no merge commit,
// and its head is what the target branch now holds.
func (m gitlabMR) pull() PullRequest {
	out := PullRequest{Number: m.IID, URL: m.URL, State: "closed", Merged: m.State == "merged"}
	switch {
	case m.State == "opened":
		out.State = "open"
	case out.Merged:
		out.MergeCommit = cmp.Or(m.MergeCommit, m.Squash, m.Head)
	}
	return out
}

type bitbucketPR struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	MergeCommit *struct {
		Hash string `json:"hash"`
	} `json:"merge_commit"`
}

func (p bitbucketPR) pull() PullRequest {
	out := PullRequest{Number: p.ID, URL: p.Links.HTML.Href, State: "closed", Merged: p.State == "MERGED"}
	if p.State == "OPEN" {
		out.State = "open"
	}
	if out.Merged && p.MergeCommit != nil {
		out.MergeCommit = p.MergeCommit.Hash
	}
	return out
}

type serverPR struct {
	ID      int    `json:"id"`
	Version int    `json:"version"`
	State   string `json:"state"`
	Links   struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
	Properties struct {
		MergeCommit struct {
			ID string `json:"id"`
		} `json:"mergeCommit"`
	} `json:"properties"`
}

func (p serverPR) pull() PullRequest {
	out := PullRequest{Number: p.ID, State: "closed", Merged: p.State == "MERGED"}
	if p.State == "OPEN" {
		out.State = "open"
	}
	if len(p.Links.Self) > 0 {
		out.URL = p.Links.Self[0].Href
	}
	if out.Merged {
		out.MergeCommit = p.Properties.MergeCommit.ID
	}
	return out
}

// StatusError is a forge's refusal, with its HTTP status.
type StatusError struct {
	Code int
	What string
}

func (e *StatusError) Error() string { return e.What }

// Refused reports whether a forge turned a request down for what it asked,
// as for a token without the permission it needs, so that asking again
// changes nothing.
func Refused(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Code >= 400 && s.Code < 500 && s.Code != http.StatusTooManyRequests
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.API, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.Token == "" {
		return fmt.Errorf("no token for %s", c.Web)
	}
	switch c.kind() {
	case GitLab:
		req.Header.Set("PRIVATE-TOKEN", c.Token)
	case Bitbucket, BitbucketServer:
		req.Header.Set("Authorization", "Bearer "+c.Token)
	default:
		req.Header.Set("Authorization", "token "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &StatusError{Code: resp.StatusCode, What: fmt.Sprintf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

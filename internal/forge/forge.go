// Package forge opens and reads pull requests on Gitea and GitHub, whose
// pull request APIs have the same shape where the Fixer needs them.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to one forge.
type Client struct {
	// Web is the forge's address, which its repositories' URLs start with:
	// https://github.com, or a Gitea server's root URL.
	Web string
	// API is its API's base URL: https://api.github.com, or a Gitea
	// server's root URL followed by /api/v1.
	API   string
	Token string
	HTTP  *http.Client
}

// PullRequest is what the Fixer reads of a pull request.
type PullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"html_url"`
	State       string `json:"state"`
	Merged      bool   `json:"merged"`
	MergeCommit string `json:"merge_commit_sha"`
}

// Repo returns the owner and name of a repository on this forge from its
// clone URL. A URL anywhere else is refused, so the forge's token is never
// sent to another host.
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
	if !ok || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%s does not name a repository as OWNER/NAME on %s", cloneURL, c.Web)
	}
	return parts[0], parts[1], nil
}

// Open opens a pull request from the head branch into the base branch.
func (c *Client) Open(ctx context.Context, owner, repo, head, base, title, body string) (PullRequest, error) {
	var pr PullRequest
	err := c.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repo)),
		map[string]string{"head": head, "base": base, "title": title, "body": body}, &pr)
	return pr, err
}

// Merge merges a pull request with a merge commit, as GitHub and Gitea each
// ask for one.
func (c *Client) Merge(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", url.PathEscape(owner), url.PathEscape(repo), number)
	if api, err := url.Parse(c.API); err == nil && api.Host == "api.github.com" {
		return c.call(ctx, http.MethodPut, path, map[string]string{"merge_method": "merge"}, nil)
	}
	return c.call(ctx, http.MethodPost, path, map[string]string{"Do": "merge"}, nil)
}

// Comment adds a comment to a pull request, as both GitHub and Gitea take
// one on its issue.
func (c *Client) Comment(ctx context.Context, owner, repo string, number int, body string) error {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), number),
		map[string]string{"body": body}, nil)
}

// Get reads a pull request.
func (c *Client) Get(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	var pr PullRequest
	err := c.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number), nil, &pr)
	return pr, err
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
	req.Header.Set("Authorization", "token "+c.Token)
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
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

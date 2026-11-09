package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Forge is a Gitea's API as the person who owns the scenarios' repositories:
// what commit faults change, and the pull requests that person merges.
// Sentinel's Fixer merges only where a policy grants it fix.merge; else
// this person does.
type Forge struct {
	// API is the API's base URL, a Gitea root URL followed by /api/v1.
	API   string
	Token string
	HTTP  *http.Client
}

// file reads a file of a repository's branch and its blob hash.
func (f *Forge) file(ctx context.Context, repo, branch, path string) (string, string, error) {
	var out struct {
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	if err := f.call(ctx, http.MethodGet, "/repos/"+repo+"/contents/"+path+"?ref="+url.QueryEscape(branch), nil, &out); err != nil {
		return "", "", err
	}
	content, err := base64.StdEncoding.DecodeString(out.Content)
	return string(content), out.SHA, err
}

// put commits a file's new content to a branch, over the blob hash read,
// and returns the commit.
func (f *Forge) put(ctx context.Context, repo, branch, path, content, sha, message string) (string, error) {
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := f.call(ctx, http.MethodPut, "/repos/"+repo+"/contents/"+path, map[string]string{
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "sha": sha, "message": message, "branch": branch,
	}, &out)
	return out.Commit.SHA, err
}

// merge merges a pull request.
func (f *Forge) merge(ctx context.Context, repo string, number int) error {
	return f.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), map[string]string{"Do": "merge"}, nil)
}

func (f *Forge) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(f.API, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+f.Token)
	req.Header.Set("Content-Type", "application/json")
	client := f.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

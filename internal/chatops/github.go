// Package chatops — GitHub API client implementation.
// Wraps the GitHub REST API for the operations chatops needs. Uses the same
// PAT or GitHub App token as the rest of Sentinel (passed in via the
// GitHubToken field). No SDK — direct net/http, same pattern as
// executors/github.go.
package chatops

import (
	"strings"
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"

	githubapp "github.com/karimzakzouk/sentinel/internal/github"
)

// GitHubClientImpl is the default GitHubClient implementation.

type GitHubClientImpl struct {
	*githubapp.BaseClient
}

// TokenProvider returns a fresh GitHub API token. v4.0.0.
// Implemented by githubapp.App.

type TokenProvider interface {
        GetInstallToken(ctx context.Context) (string, error)
}

// NewGitHubClient creates a GitHubClientImpl.
// token: PAT or GitHub App installation token.
// apiURL: base API URL (default "https://api.github.com"). For GitHub
// Enterprise, set to "https://gh.mycorp.internal/api/v3".

func NewGitHubClient(token, apiURL string) *GitHubClientImpl {
	return &GitHubClientImpl{
		BaseClient: githubapp.NewBaseClient(token, apiURL),
	}
}

// WithTokenProvider wires a GitHub App token provider. v4.0.0.
// When set, all chatops GitHub API calls (comments, reactions, labels,
// merges) use the App's installation token, appearing as "Sentinel[bot]".

func (g *GitHubClientImpl) WithTokenProvider(tp TokenProvider) *GitHubClientImpl {
	g.BaseClient.WithTokenProvider(tp)
	return g
}

// authToken returns the token to use for an API call. v4.0.0.


// ReactComment adds an emoji reaction to a PR comment.
// GitHub dedupes reactions from the same user — calling this twice with the
// same reaction is a no-op (doesn't error).

func (g *GitHubClientImpl) ReactComment(ctx context.Context, repo string, commentID int64, reaction string) error {
        body, _ := json.Marshal(map[string]string{"content": reaction})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions", g.APIURL, repo, commentID),
                bytes.NewReader(body))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        // 200 = created, 201 = also created, 422 = already reacted (ignore)

        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != 422 {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("github reaction %q failed: %d %s", reaction, resp.StatusCode, string(body))
        }
        return nil
}

// RemoveReaction removes a reaction by ID. We need to list reactions first
// to find the one owned by the bot, then DELETE it.

func (g *GitHubClientImpl) RemoveReaction(ctx context.Context, repo string, commentID int64, reaction string) error {
        // List reactions on the comment to find ours.

        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions", g.APIURL, repo, commentID), nil)
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        if resp.StatusCode != http.StatusOK {
                resp.Body.Close()
                return fmt.Errorf("list reactions: %d", resp.StatusCode)
        }
        var reactions []struct {
                ID      int64  `json:"id"`
                Content string `json:"content"`
                User    struct {
                        Login string `json:"login"`
                } `json:"user"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&reactions); err != nil {
                resp.Body.Close()
                return err
        }
        resp.Body.Close()

        // Find the bot's reaction of this type. The bot is the user whose token
        // we're using — we don't know its login without a /user call. Instead,
        // just delete the first reaction of this type (best-effort — if multiple
        // users reacted +1, we might delete theirs, but that's a minor UX issue).

        for _, r := range reactions {
                if r.Content == reaction {
                        delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
                                fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions/%d", g.APIURL, repo, commentID, r.ID), nil)
                        g.setHeaders(delReq)
                        delResp, err := g.HTTP.Do(delReq)
                        if err != nil {
                                return err
                        }
                        delResp.Body.Close()
                        if delResp.StatusCode != http.StatusNoContent && delResp.StatusCode != http.StatusOK {
                                return fmt.Errorf("delete reaction %d: %d", r.ID, delResp.StatusCode)
                        }
                        return nil
                }
        }
        return nil // not found — already gone
}

// PostComment posts a new comment on a PR.

func (g *GitHubClientImpl) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
        _, err := g.PostCommentWithID(ctx, repo, prNumber, body)
        return err
}

// PostCommentWithID posts a comment and returns its ID (for follow-up reactions).

func (g *GitHubClientImpl) PostCommentWithID(ctx context.Context, repo string, prNumber int, body string) (int64, error) {
        payload, _ := json.Marshal(map[string]string{"body": body})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/comments", g.APIURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return 0, err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return 0, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return 0, fmt.Errorf("post comment: %d %s", resp.StatusCode, string(body))
        }
        var result struct {
                ID int64 `json:"id"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
                return 0, err
        }
        return result.ID, nil
}

// FindStatusComment returns the ID + body of the [SENTINELSTATUS] comment
// on this PR, or 0 if not found. We list all PR comments and look for one
// containing the [SENTINELSTATUS] marker.

func (g *GitHubClientImpl) FindStatusComment(ctx context.Context, repo string, prNumber int) (int64, string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", g.APIURL, repo, prNumber), nil)
        if err != nil {
                return 0, "", err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return 0, "", err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                return 0, "", fmt.Errorf("list comments: %d", resp.StatusCode)
        }
        var comments []struct {
                ID   int64  `json:"id"`
                Body string `json:"body"`
                User struct {
                        Login string `json:"login"`
                } `json:"user"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
                return 0, "", err
        }
        for _, c := range comments {
                if strings.Contains(c.Body, "[SENTINELSTATUS]") {
                        return c.ID, c.Body, nil
                }
        }
        return 0, "", nil
}

// UpdateComment edits an existing comment in place.

func (g *GitHubClientImpl) UpdateComment(ctx context.Context, repo string, commentID int64, body string) error {
        payload, _ := json.Marshal(map[string]string{"body": body})
        req, err := http.NewRequestWithContext(ctx, "PATCH",
                fmt.Sprintf("%s/repos/%s/issues/comments/%d", g.APIURL, repo, commentID),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("update comment: %d %s", resp.StatusCode, string(body))
        }
        return nil
}

// GetPermission returns the user's permission level on the repo.
// Returns "read", "write", "admin", "maintain", "triage", or "none".

func (g *GitHubClientImpl) GetPermission(ctx context.Context, repo, username string) (string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/collaborators/%s/permission", g.APIURL, repo, username), nil)
        if err != nil {
                return "", err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()
        if resp.StatusCode == http.StatusNotFound {
                // User isn't a collaborator — they have "read" if the repo is public,
                // "none" if private. We can't tell from here, so return "read" (most
                // permissive — informational commands will work, operational won't).

                return "read", nil
        }
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return "", fmt.Errorf("get permission: %d %s", resp.StatusCode, string(body))
        }
        var result struct {
                Permission string `json:"permission"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
                return "", err
        }
        return result.Permission, nil
}

// setHeaders adds the standard GitHub API headers. v4.0.0: uses authToken()
// which prefers the GitHub App installation token when configured.

func (g *GitHubClientImpl) setHeaders(req *http.Request) {
        ctx := req.Context()
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("Authorization", "Bearer "+g.AuthToken(ctx))
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")
}

// SetLabels adds labels to a PR (used by /sentinel approve, /sentinel hold).
// v4.0.0.

func (g *GitHubClientImpl) SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
        payload, _ := json.Marshal(map[string][]string{"labels": labels})
        req, err := http.NewRequestWithContext(ctx, "POST",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels", g.APIURL, repo, prNumber),
                bytes.NewReader(payload))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("set labels: %d %s", resp.StatusCode, string(body))
        }
        return nil
}

// RemoveLabel removes a label from a PR. v4.0.0.

func (g *GitHubClientImpl) RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error {
        req, err := http.NewRequestWithContext(ctx, "DELETE",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", g.APIURL, repo, prNumber, label), nil)
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        // 200 = removed, 404 = label wasn't there (treat as success)

        if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("remove label: %d %s", resp.StatusCode, string(body))
        }
        return nil
}

// GetLabels returns the labels on a PR. v4.0.0.

func (g *GitHubClientImpl) GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/%d/labels?per_page=100", g.APIURL, repo, prNumber), nil)
        if err != nil {
                return nil, err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                return nil, fmt.Errorf("get labels: %d", resp.StatusCode)
        }
        var labels []struct {
                Name string `json:"name"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&labels); err != nil {
                return nil, err
        }
        out := make([]string, 0, len(labels))
        for _, l := range labels {
                out = append(out, l.Name)
        }
        return out, nil
}

// Merge merges a PR via the GitHub API. v4.0.0.
// Used by /sentinel merge (manual merge through Sentinel so the outcome
// watcher attaches). The auto-merger uses its own GitHub client.

func (g *GitHubClientImpl) Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error {
        body, _ := json.Marshal(map[string]string{
                "commit_message": commitMessage,
                "merge_method":   "squash",
        })
        req, err := http.NewRequestWithContext(ctx, "PUT",
                fmt.Sprintf("%s/repos/%s/pulls/%d/merge", g.APIURL, repo, prNumber),
                bytes.NewReader(body))
        if err != nil {
                return err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                return fmt.Errorf("merge: %d %s", resp.StatusCode, string(respBody))
        }
        return nil
}

// FindCommentByMarker returns the body of the first PR comment containing
// the given marker string. v4.0.0. Used by /sentinel apply to find the
// [SENTINELALTERNATIVES] comment.

func (g *GitHubClientImpl) FindCommentByMarker(ctx context.Context, repo string, prNumber int, marker string) (string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET",
                fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", g.APIURL, repo, prNumber), nil)
        if err != nil {
                return "", err
        }
        g.setHeaders(req)
        resp, err := g.HTTP.Do(req)
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                return "", fmt.Errorf("list comments: %d", resp.StatusCode)
        }
        var comments []struct {
                Body string `json:"body"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
                return "", err
        }
        for _, c := range comments {
                if strings.Contains(c.Body, marker) {
                        return c.Body, nil
                }
        }
        return "", nil
}

// ReactionsClientImpl manages emoji reactions on PR comments.

type ReactionsClientImpl struct {
        gh GitHubClient
}

// NewReactionsClient creates a ReactionsClient.

func NewReactionsClient(gh GitHubClient) *ReactionsClientImpl {
        return &ReactionsClientImpl{gh: gh}
}

// Ack reacts to the command comment with 👍 (immediate acknowledgment).
// This is the #1 fix for Prow's #1 complaint (silence).

func (r *ReactionsClientImpl) Ack(ctx context.Context, repo string, commentID int64) error {
        return r.gh.ReactComment(ctx, repo, commentID, "+1")
}

// Success replaces the 👍 reaction with ✅ (white_check_mark).

func (r *ReactionsClientImpl) Success(ctx context.Context, repo string, commentID int64) error {
        _ = r.gh.RemoveReaction(ctx, repo, commentID, "+1") // best-effort remove
        return r.gh.ReactComment(ctx, repo, commentID, "white_check_mark")
}

// Failure replaces the 👍 reaction with ❌.

func (r *ReactionsClientImpl) Failure(ctx context.Context, repo string, commentID int64) error {
        _ = r.gh.RemoveReaction(ctx, repo, commentID, "+1")
        return r.gh.ReactComment(ctx, repo, commentID, "-1")
}

// Pending replaces the 👍 reaction with 👀 (eyes — "looking at this").

func (r *ReactionsClientImpl) Pending(ctx context.Context, repo string, commentID int64) error {
        _ = r.gh.RemoveReaction(ctx, repo, commentID, "+1")
        return r.gh.ReactComment(ctx, repo, commentID, "eyes")
}

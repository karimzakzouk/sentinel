package chatops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	githubapp "github.com/karimzakzouk/sentinel/internal/github"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// GitHubClientImpl is the default GitHubClient implementation. It embeds
// github.BaseClient for PostComment / GetLabels / Merge / SetLabels /
// RemoveLabel / RevertMergeCommit and adds the chatops-specific methods
// (ReactComment, RemoveReaction, FindStatusComment, UpdateComment,
// GetPermission, PostCommentWithID, FindCommentByMarker).
type GitHubClientImpl struct {
	*githubapp.BaseClient
	botLogin string
}

// TokenProvider returns a fresh GitHub installation token. Implemented by
// *github.App.
type TokenProvider interface {
	GetInstallToken(ctx context.Context) (string, error)
}

// NewGitHubClient creates a GitHubClientImpl with the given PAT and API URL.
func NewGitHubClient(token, apiURL string) *GitHubClientImpl {
	return &GitHubClientImpl{
		BaseClient: githubapp.NewBaseClient(token, apiURL),
	}
}

// WithTokenProvider wires a GitHub App token provider so all chatops GitHub
// API calls (comments, reactions, labels, merges) use the installation
// token and appear as the Sentinel[bot] user.
func (g *GitHubClientImpl) WithTokenProvider(tp TokenProvider) *GitHubClientImpl {
	g.BaseClient.WithTokenProvider(tp)
	return g
}

// getBotLogin fetches the authenticated user's login (cached after the
// first call). Used by RemoveReaction to match the bot's own reaction
// rather than a random user's.
func (g *GitHubClientImpl) getBotLogin(ctx context.Context) string {
	if g.botLogin != "" {
		return g.botLogin
	}
	req, err := http.NewRequestWithContext(ctx, "GET", g.APIURL+"/user", nil)
	if err != nil {
		return ""
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return ""
	}
	g.botLogin = user.Login
	return g.botLogin
}

// ReactComment adds an emoji reaction to a PR comment. GitHub dedupes
// reactions from the same user — calling this twice with the same reaction
// is a no-op (does not error).
func (g *GitHubClientImpl) ReactComment(ctx context.Context, repo string, commentID int64, reaction string) error {
	body, _ := json.Marshal(map[string]string{"content": reaction})
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions", g.APIURL, repo, commentID),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 200 = created, 201 = also created, 422 = already reacted (ignore).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != 422 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github reaction %q failed: %d %s", reaction, resp.StatusCode, string(body))
	}
	return nil
}

// RemoveReaction removes the bot's reaction of the given type. We list
// reactions first to find ours, then DELETE it. The bot login is fetched
// via /user so we don't accidentally delete a user's reaction.
func (g *GitHubClientImpl) RemoveReaction(ctx context.Context, repo string, commentID int64, reaction string) error {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions", g.APIURL, repo, commentID), nil)
	if err != nil {
		return err
	}
	g.SetHeaders(req)
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

	botLogin := g.getBotLogin(ctx)
	if botLogin == "" {
		logging.Warn("chatops: could not determine bot login for reaction removal — may delete wrong user's reaction")
	}

	for _, r := range reactions {
		if r.Content != reaction {
			continue
		}
		// When we know the bot login, only delete the bot's reaction.
		// Otherwise fall back to first match.
		if botLogin != "" && r.User.Login != botLogin {
			continue
		}
		delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
			fmt.Sprintf("%s/repos/%s/issues/comments/%d/reactions/%d", g.APIURL, repo, commentID, r.ID), nil)
		g.SetHeaders(delReq)
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
	return nil
}

// PostComment posts a new comment on a PR.
func (g *GitHubClientImpl) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
	_, err := g.PostCommentWithID(ctx, repo, prNumber, body)
	return err
}

// PostCommentWithID posts a comment and returns its ID (for follow-up
// reactions).
func (g *GitHubClientImpl) PostCommentWithID(ctx context.Context, repo string, prNumber int, body string) (int64, error) {
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/repos/%s/issues/%d/comments", g.APIURL, repo, prNumber),
		bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	g.SetHeaders(req)
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
// on this PR, or 0 if not found.
func (g *GitHubClientImpl) FindStatusComment(ctx context.Context, repo string, prNumber int) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", g.APIURL, repo, prNumber), nil)
	if err != nil {
		return 0, "", err
	}
	g.SetHeaders(req)
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
	g.SetHeaders(req)
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

// GetPermission returns the user's permission level on the repo. Returns
// "read", "write", "admin", "maintain", "triage", or "none". A 404 (user
// isn't a collaborator) returns "read" so informational commands still
// work for non-collaborators on public repos.
func (g *GitHubClientImpl) GetPermission(ctx context.Context, repo, username string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/collaborators/%s/permission", g.APIURL, repo, username), nil)
	if err != nil {
		return "", err
	}
	g.SetHeaders(req)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
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

// FindCommentByMarker returns the body of the first PR comment containing
// the given marker string. Used by /sentinel apply to find the
// [SENTINELALTERNATIVES] comment.
func (g *GitHubClientImpl) FindCommentByMarker(ctx context.Context, repo string, prNumber int, marker string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", g.APIURL, repo, prNumber), nil)
	if err != nil {
		return "", err
	}
	g.SetHeaders(req)
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
func (r *ReactionsClientImpl) Ack(ctx context.Context, repo string, commentID int64) error {
	return r.gh.ReactComment(ctx, repo, commentID, "+1")
}

// Success replaces the 👍 reaction with ✅ (white_check_mark).
func (r *ReactionsClientImpl) Success(ctx context.Context, repo string, commentID int64) error {
	_ = r.gh.RemoveReaction(ctx, repo, commentID, "+1")
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

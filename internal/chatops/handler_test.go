package chatops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockGitHubClient records every GitHubClient call for assertions.
type mockGitHubClient struct {
	postedComments   []postedComment
	reactions        []reactionOp
	perm             string
	permErr          error
	labels           []string
	setLabelCalls    []string
	removedLabels    []string
	mergedPRs        []int
	statusCommentID  int64
	statusCommentBdy string
	findMarkerBody   string
}

type postedComment struct {
	Repo     string
	PRNumber int
	Body     string
}

type reactionOp struct {
	Repo      string
	CommentID int64
	Reaction  string
	Remove    bool
}

func (m *mockGitHubClient) ReactComment(ctx context.Context, repo string, commentID int64, reaction string) error {
	m.reactions = append(m.reactions, reactionOp{Repo: repo, CommentID: commentID, Reaction: reaction})
	return nil
}
func (m *mockGitHubClient) RemoveReaction(ctx context.Context, repo string, commentID int64, reaction string) error {
	m.reactions = append(m.reactions, reactionOp{Repo: repo, CommentID: commentID, Reaction: reaction, Remove: true})
	return nil
}
func (m *mockGitHubClient) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
	m.postedComments = append(m.postedComments, postedComment{Repo: repo, PRNumber: prNumber, Body: body})
	return nil
}
func (m *mockGitHubClient) FindStatusComment(ctx context.Context, repo string, prNumber int) (int64, string, error) {
	return m.statusCommentID, m.statusCommentBdy, nil
}
func (m *mockGitHubClient) UpdateComment(ctx context.Context, repo string, commentID int64, body string) error {
	m.statusCommentBdy = body
	return nil
}
func (m *mockGitHubClient) GetPermission(ctx context.Context, repo, username string) (string, error) {
	if m.permErr != nil {
		return "", m.permErr
	}
	if m.perm == "" {
		return "admin", nil
	}
	return m.perm, nil
}
func (m *mockGitHubClient) PostCommentWithID(ctx context.Context, repo string, prNumber int, body string) (int64, error) {
	m.postedComments = append(m.postedComments, postedComment{Repo: repo, PRNumber: prNumber, Body: body})
	return int64(len(m.postedComments)), nil
}
func (m *mockGitHubClient) SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error {
	m.setLabelCalls = append(m.setLabelCalls, labels...)
	return nil
}
func (m *mockGitHubClient) RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error {
	m.removedLabels = append(m.removedLabels, label)
	return nil
}
func (m *mockGitHubClient) GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error) {
	return m.labels, nil
}
func (m *mockGitHubClient) Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error {
	m.mergedPRs = append(m.mergedPRs, prNumber)
	return nil
}
func (m *mockGitHubClient) FindCommentByMarker(ctx context.Context, repo string, prNumber int, marker string) (string, error) {
	return m.findMarkerBody, nil
}

// mockReactionsClient records every reaction for assertions.
type mockReactionsClient struct {
	acks      []int64
	successes []int64
	failures  []int64
	pendings  []int64
}

func (r *mockReactionsClient) Ack(ctx context.Context, repo string, commentID int64) error {
	r.acks = append(r.acks, commentID)
	return nil
}
func (r *mockReactionsClient) Success(ctx context.Context, repo string, commentID int64) error {
	r.successes = append(r.successes, commentID)
	return nil
}
func (r *mockReactionsClient) Failure(ctx context.Context, repo string, commentID int64) error {
	r.failures = append(r.failures, commentID)
	return nil
}
func (r *mockReactionsClient) Pending(ctx context.Context, repo string, commentID int64) error {
	r.pendings = append(r.pendings, commentID)
	return nil
}

// TestHandler_ACKReaction verifies that the dispatcher adds a 👍 ACK
// reaction immediately when a command is received, then swaps to ✅ on
// success or ❌ on failure.
func TestHandler_ACKReaction(t *testing.T) {
	gh := &mockGitHubClient{}
	rx := &mockReactionsClient{}
	deps := &Deps{GitHub: gh, Reactions: rx}

	cmd := &Command{
		Name:      "help",
		Author:    "alice",
		PRNumber:  42,
		PRRepo:    "org/repo",
		CommentID: 100,
	}

	if err := Dispatch(context.Background(), []*Command{cmd}, deps); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Expect exactly one ACK + one Success.
	if len(rx.acks) != 1 || rx.acks[0] != 100 {
		t.Errorf("expected 1 ACK on comment 100, got %v", rx.acks)
	}
	if len(rx.successes) != 1 || rx.successes[0] != 100 {
		t.Errorf("expected 1 Success on comment 100, got %v", rx.successes)
	}
	if len(rx.failures) != 0 {
		t.Errorf("expected 0 Failures, got %v", rx.failures)
	}

	// /sentinel help should have posted one comment.
	if len(gh.postedComments) != 1 {
		t.Errorf("expected 1 comment posted, got %d", len(gh.postedComments))
	}
}

// TestHandler_ACKThenFailure verifies that an unknown command ACKs then
// fails.
func TestHandler_ACKThenFailure(t *testing.T) {
	gh := &mockGitHubClient{}
	rx := &mockReactionsClient{}
	deps := &Deps{GitHub: gh, Reactions: rx}

	cmd := &Command{
		Name:      "nonexistent-command",
		Author:    "alice",
		PRNumber:  42,
		PRRepo:    "org/repo",
		CommentID: 200,
	}

	_ = Dispatch(context.Background(), []*Command{cmd}, deps)

	if len(rx.acks) != 1 || rx.acks[0] != 200 {
		t.Errorf("expected 1 ACK, got %v", rx.acks)
	}
	if len(rx.failures) != 1 || rx.failures[0] != 200 {
		t.Errorf("expected 1 Failure, got %v", rx.failures)
	}
	if len(rx.successes) != 0 {
		t.Errorf("expected 0 Successes, got %v", rx.successes)
	}
}

// TestHandler_PermissionDenied verifies a permission-denied command ACKs
// then fails with an actionable error.
func TestHandler_PermissionDenied(t *testing.T) {
	gh := &mockGitHubClient{perm: "read"}
	rx := &mockReactionsClient{}
	deps := &Deps{GitHub: gh, Reactions: rx}

	// /sentinel hold requires PermWrite; alice only has read.
	cmd := &Command{
		Name:      "hold",
		Author:    "alice",
		PRNumber:  42,
		PRRepo:    "org/repo",
		CommentID: 300,
	}

	_ = Dispatch(context.Background(), []*Command{cmd}, deps)

	if len(rx.acks) != 1 {
		t.Errorf("expected 1 ACK, got %v", rx.acks)
	}
	if len(rx.failures) != 1 {
		t.Errorf("expected 1 Failure (denied), got %v", rx.failures)
	}
	if len(gh.postedComments) != 1 {
		t.Fatalf("expected 1 comment posted, got %d", len(gh.postedComments))
	}
	if !strings.Contains(gh.postedComments[0].Body, "doesn't have the required permission") {
		t.Errorf("expected actionable permission error, got: %s", gh.postedComments[0].Body)
	}
	if !strings.Contains(gh.postedComments[0].Body, "Request access") {
		t.Errorf("expected 'Request access' link in permission error, got: %s", gh.postedComments[0].Body)
	}
}

// TestHandler_VerifySignature verifies the HMAC-SHA256 signature check.
func TestHandler_VerifySignature(t *testing.T) {
	h := NewWebhookHandler(&Deps{GitHub: &mockGitHubClient{}}, "test-secret")

	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !h.verifySignature(body, sig) {
		t.Error("valid signature was rejected")
	}
	if h.verifySignature(body, "sha256=deadbeef") {
		t.Error("invalid signature was accepted")
	}
	if h.verifySignature(body, "") {
		t.Error("empty signature was accepted")
	}
	if h.verifySignature(body, "deadbeef") {
		t.Error("signature without sha256= prefix was accepted")
	}
}

// TestHandler_RejectsNonIssueComment verifies the handler returns 200 (not
// an error) for non-issue_comment events so GitHub doesn't retry.
func TestHandler_RejectsNonIssueComment(t *testing.T) {
	h := NewWebhookHandler(&Deps{GitHub: &mockGitHubClient{}}, "test-secret")
	body := []byte(`{}`)

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", signBody("test-secret", body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for non-issue_comment event, got %d", w.Code)
	}
}

// TestHandler_RejectsGET verifies only POST is accepted.
func TestHandler_RejectsGET(t *testing.T) {
	h := NewWebhookHandler(&Deps{GitHub: &mockGitHubClient{}}, "test-secret")
	req := httptest.NewRequest("GET", "/webhook/github", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestHandler_SkipsBotComments verifies the handler returns 200 without
// dispatching when the comment author is the sentinel bot.
func TestHandler_SkipsBotComments(t *testing.T) {
	gh := &mockGitHubClient{}
	rx := &mockReactionsClient{}
	h := NewWebhookHandler(&Deps{GitHub: gh, Reactions: rx}, "test-secret")

	payload, _ := json.Marshal(map[string]any{
		"action": "created",
		"comment": map[string]any{
			"id":   1,
			"body": "/sentinel help",
			"user": map[string]any{"login": "sentinel-bot"},
		},
		"issue": map[string]any{
			"number":       42,
			"pull_request": map[string]any{"url": "https://api.github.com/repos/org/repo/pulls/42"},
		},
		"repository": map[string]any{"full_name": "org/repo"},
	})

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", signBody("test-secret", payload))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if len(rx.acks) != 0 {
		t.Errorf("expected no ACK reactions for bot comment, got %d", len(rx.acks))
	}
}

// TestHandler_IssueCommentNotOnPR verifies the handler returns 200 without
// dispatching when the comment is on an issue (not a PR).
func TestHandler_IssueCommentNotOnPR(t *testing.T) {
	gh := &mockGitHubClient{}
	rx := &mockReactionsClient{}
	h := NewWebhookHandler(&Deps{GitHub: gh, Reactions: rx}, "test-secret")

	payload, _ := json.Marshal(map[string]any{
		"action": "created",
		"comment": map[string]any{
			"id":   1,
			"body": "/sentinel help",
			"user": map[string]any{"login": "alice"},
		},
		"issue": map[string]any{
			"number":       42,
			"pull_request": map[string]any{},
		},
		"repository": map[string]any{"full_name": "org/repo"},
	})

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", signBody("test-secret", payload))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if len(rx.acks) != 0 {
		t.Errorf("expected no ACK for non-PR comment, got %d", len(rx.acks))
	}
}

// signBody computes the X-Hub-Signature-256 header value for a body + secret.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// dummyReader is a no-op Reader used to silence unused imports in tests
// that exercise the handler via httptest only.
type dummyReader struct{}

func (dummyReader) Read(p []byte) (int, error) { return 0, io.EOF }

var _ = dummyReader{}

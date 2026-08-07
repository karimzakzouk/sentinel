package chatops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// WebhookHandler is the HTTP handler for POST /webhook/github. It receives
// all incoming GitHub webhook events; for issue_comment events on a PR that
// contain /sentinel commands, it dispatches them via Dispatch. Other event
// types are acked so GitHub doesn't retry.
//
// It coexists with prreview.Reviewer (which handles pull_request events for
// predictive PR review). Both handlers run on the same webhook endpoint —
// the event type determines which one fires.
type WebhookHandler struct {
	deps          *Deps
	webhookSecret string
	// dispatchSem caps concurrent dispatch goroutines. Each /sentinel
	// command spawns a goroutine that can live up to 30 minutes (long
	// sandbox retries). Without a cap, an attacker posting many /sentinel
	// retest commands could exhaust memory.
	dispatchSem chan struct{}
}

// NewWebhookHandler creates a WebhookHandler with the given deps and secret.
// The semaphore size defaults to 8 concurrent dispatches.
func NewWebhookHandler(deps *Deps, webhookSecret string) *WebhookHandler {
	return &WebhookHandler{
		deps:          deps,
		webhookSecret: webhookSecret,
		dispatchSem:   make(chan struct{}, 8),
	}
}

// ServeHTTP receives a GitHub webhook event.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cap the body at 1MB — webhook payloads are small.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	if h.webhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !h.verifySignature(body, sig) {
			logging.Error("chatops: webhook signature verification failed")
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
	}

	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	if eventType != "issue_comment" {
		// Not ours — return 200 so GitHub doesn't retry.
		w.WriteHeader(http.StatusOK)
		return
	}

	var event struct {
		Action  string `json:"action"`
		Comment struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
		Issue struct {
			Number      int `json:"number"`
			PullRequest struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		} `json:"issue"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		logging.Error("chatops: failed to parse issue_comment webhook", "err", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Only "created" actions matter — edits/deletes are too risky to honor.
	if event.Action != "created" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only PR comments, not issue comments. If pull_request.url is empty,
	// the comment was on an issue, not a PR — skip.
	if event.Issue.PullRequest.URL == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Skip the bot's own comments (prevents infinite loops).
	if strings.Contains(strings.ToLower(event.Comment.User.Login), "sentinel") ||
		strings.Contains(event.Comment.User.Login, "[bot]") {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Skip our own status comment (defense in depth — ParseCommand also
	// handles it).
	if strings.Contains(event.Comment.Body, "[SENTINELSTATUS]") {
		w.WriteHeader(http.StatusOK)
		return
	}

	commands := ParseCommand(
		event.Comment.Body,
		event.Comment.User.Login,
		event.Issue.Number,
		event.Repository.FullName,
		event.Comment.ID,
	)
	if len(commands) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	logging.Info("chatops: received commands",
		"count", len(commands), "author", event.Comment.User.Login,
		"repo", event.Repository.FullName, "pr", event.Issue.Number,
		"delivery", deliveryID)

	// Dispatch in a goroutine so we return 200 immediately — GitHub times
	// out webhooks after 10s. Long-running commands (retry-sandbox takes
	// minutes) must run async. The bounded semaphore caps concurrent
	// dispatches at 8; the 9th waits up to 30s, then we return 503 so
	// GitHub retries later.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("chatops: dispatch panic recovered", "panic", r)
			}
		}()
		select {
		case h.dispatchSem <- struct{}{}:
			defer func() { <-h.dispatchSem }()
		case <-time.After(30 * time.Second):
			logging.Warn("chatops: dispatch queue full, dropping commands",
				"author", event.Comment.User.Login,
				"repo", event.Repository.FullName, "pr", event.Issue.Number)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := Dispatch(ctx, commands, h.deps); err != nil {
			logging.Error("chatops: dispatch error", "err", err)
		}
	}()

	w.WriteHeader(http.StatusOK)
}

// verifySignature checks the X-Hub-Signature-256 header against the body.
func (h *WebhookHandler) verifySignature(body []byte, sig string) bool {
	if sig == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(sig, prefix) {
		return false
	}
	expected, err := hex.DecodeString(sig[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	actual := mac.Sum(nil)
	return hmac.Equal(expected, actual)
}

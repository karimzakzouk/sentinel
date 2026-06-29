// Package chatops — GitHub webhook handler for issue_comment events.
//
// Wired into api/server.go via SetChatopsHandler. Receives all incoming
// /webhook/github events, dispatches to chatops.Dispatch when the event is
// an issue_comment on a PR containing a /sentinel command.
//
// Coexists with prreview.Reviewer (which handles pull_request events for
// predictive PR review). Both handlers run on the same webhook endpoint —
// the event type determines which one fires.
package chatops

import (
        "context"
        "crypto/hmac"
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
        "io"
        "log"
        "net/http"
        "strings"
        "time"
)

// WebhookHandler is the HTTP handler for /webhook/github.
type WebhookHandler struct {
        deps          *Deps
        webhookSecret string // optional HMAC-SHA256 secret for signature verification
}

// NewWebhookHandler creates a webhook handler.
// deps is the dependency bag (must be fully wired before this is called).
// webhookSecret is the GitHub webhook secret (empty = skip signature check).
func NewWebhookHandler(deps *Deps, webhookSecret string) *WebhookHandler {
        return &WebhookHandler{
                deps:          deps,
                webhookSecret: webhookSecret,
        }
}

// ServeHTTP receives a GitHub webhook event.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
                return
        }

        // Read body (capped at 1MB — webhook payloads are small).
        body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
        if err != nil {
                http.Error(w, "read body failed", http.StatusBadRequest)
                return
        }

        // Verify signature if a secret is configured.
        if h.webhookSecret != "" {
                sig := r.Header.Get("X-Hub-Signature-256")
                if !h.verifySignature(body, sig) {
                        log.Printf("[chatops] webhook signature verification failed")
                        http.Error(w, "signature mismatch", http.StatusUnauthorized)
                        return
                }
        }

        eventType := r.Header.Get("X-GitHub-Event")
        deliveryID := r.Header.Get("X-GitHub-Delivery")

        // Only issue_comment events are interesting to chatops.
        // (pull_request events are handled by prreview.Reviewer.)
        if eventType != "issue_comment" {
                // Not ours — return 200 so GitHub doesn't retry.
                w.WriteHeader(http.StatusOK)
                return
        }

        // Parse the webhook payload.
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
                        Number       int    `json:"number"`
                        PullRequest  struct {
                                URL string `json:"url"`
                        } `json:"pull_request"`
                } `json:"issue"`
                Repository struct {
                        FullName string `json:"full_name"`
                } `json:"repository"`
        }
        if err := json.Unmarshal(body, &event); err != nil {
                log.Printf("[chatops] failed to parse issue_comment webhook: %v", err)
                http.Error(w, "bad payload", http.StatusBadRequest)
                return
        }

        // Only "created" actions matter (we don't process edits/deletes — too risky).
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

        // Skip bot's own comments (prevents infinite loops).
        if strings.Contains(strings.ToLower(event.Comment.User.Login), "sentinel") ||
                strings.Contains(event.Comment.User.Login, "[bot]") {
                w.WriteHeader(http.StatusOK)
                return
        }

        // Skip our own status comment (shouldn't happen — ParseCommand handles it —
        // but defense in depth).
        if strings.Contains(event.Comment.Body, "[SENTINELSTATUS]") {
                w.WriteHeader(http.StatusOK)
                return
        }

        // Parse the comment for /sentinel commands.
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

        log.Printf("[chatops] received %d command(s) from @%s on %s#%d (delivery %s)",
                len(commands), event.Comment.User.Login, event.Repository.FullName, event.Issue.Number, deliveryID)

        // Dispatch in a goroutine so we return 200 immediately — GitHub times out
        // webhooks after 10s. Long-running commands (retry-sandbox takes minutes)
        // must run async.
        go func() {
                ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
                defer cancel()
                if err := Dispatch(ctx, commands, h.deps); err != nil {
                        log.Printf("[chatops] dispatch error: %v", err)
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

// missing time import fix

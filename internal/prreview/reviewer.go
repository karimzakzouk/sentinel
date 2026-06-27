// Package prreview implements predictive PR review — the "prevent before it
// happens" feature.
//
// When a developer opens a PR, GitHub sends a webhook to Sentinel. Sentinel:
//   1. Verifies the webhook signature (HMAC-SHA256 with a shared secret)
//   2. Fetches the PR diff via the GitHub API
//   3. Chunks the diff into hunks (one per file)
//   4. Embeds each chunk and queries the HNSW incident memory for similar
//      past incidents (especially ones with outcome=failure)
//   5. If similarity > threshold, posts a review comment warning the developer:
//      "This change resembles incident #3 which caused a KeyError CrashLoopBackOff."
//
// This is the differentiator feature — Sentinel doesn't just fix incidents,
// it prevents them.
//
// v3.5.0: New package. Endpoints added to api/server.go:
//   POST /webhook/github  — receives GitHub webhook events
package prreview

import (
        "bytes"
        "context"
        "crypto/hmac"
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "regexp"
        "strings"
        "time"
)

// Reviewer scores PR diffs against the HNSW incident memory.
type Reviewer struct {
        githubToken    string
        githubApp      GitHubAppTokenProvider // v3.13.0: optional — when set, takes precedence over githubToken
        webhookSecret  string                 // HMAC secret for verifying webhook signatures
        vectorStore    VectorStore
        similarityThreshold float32 // default 0.75
        httpClient     *http.Client
        apiURL         string // v3.11.0: base API URL (default "https://api.github.com")
}

// GitHubAppTokenProvider is implemented by githubapp.App. v3.13.0: when set,
// the Reviewer uses the App's installation token for API calls (verified bot
// identity) instead of the PAT. The PAT is used as a fallback if the App
// token call fails.
type GitHubAppTokenProvider interface {
        GetInstallToken(ctx context.Context) (string, error)
}

// WithGitHubApp wires a GitHub App for verified-bot-identity PR comments.
// v3.13.0: audit finding #2 — the App was initialized in main.go but never
// passed to the Reviewer, so PR comments were always posted as the PAT user.
// When set, the Reviewer calls GetInstallToken() before each API call and
// uses the installation token (which makes comments appear as the verified
// "Sentinel" bot). Falls back to the PAT on error.
func (r *Reviewer) WithGitHubApp(app GitHubAppTokenProvider) *Reviewer {
        r.githubApp = app
        return r
}

// authToken returns the token to use for GitHub API calls. v3.13.0.
// Prefers the GitHub App installation token (verified bot identity) when
// configured, falls back to the PAT.
func (r *Reviewer) authToken(ctx context.Context) string {
        if r.githubApp != nil {
                if token, err := r.githubApp.GetInstallToken(ctx); err == nil && token != "" {
                        return token
                }
                // Fall back to PAT on error — better to post as PAT than not at all.
        }
        return r.githubToken
}

// Warning represents a chunk of the PR diff that resembles a past incident.
type Warning struct {
        Chunk    string
        Sim      float32
        Incident *VectorMetadata
}

// VectorStore is the interface for incident memory retrieval.
// Satisfied by feedback.VectorStore (via an adapter in main.go).
type VectorStore interface {
        Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error)
        UsingHashFallback() bool
}

// VectorMetadata is a past incident stored in the vector index.
type VectorMetadata struct {
        IncidentID string
        Service    string
        RootCause  string
        FixType    string
        FixPattern string
        Outcome    string // "success" or "failure"
}

// New creates a Reviewer.
func New(githubToken, webhookSecret string, vs VectorStore) *Reviewer {
        return &Reviewer{
                githubToken:         githubToken,
                webhookSecret:       webhookSecret,
                vectorStore:         vs,
                similarityThreshold: 0.75,
                httpClient:          &http.Client{Timeout: 30 * time.Second},
                apiURL:              "https://api.github.com", // v3.11.0: default; override via WithAPIURL
        }
}

// WithAPIURL sets the GitHub API base URL. v3.11.0: Replaces the hardcoded
// "https://api.github.com" (CRITICAL audit #2). For GitHub Enterprise:
// "https://gh.mycorp.internal/api/v3".
func (r *Reviewer) WithAPIURL(url string) *Reviewer {
        if url != "" {
                r.apiURL = strings.TrimRight(url, "/")
        }
        return r
}

// SetSimilarityThreshold configures the threshold above which a review
// comment is posted. Default 0.75. Lower = more sensitive (more comments).
func (r *Reviewer) SetSimilarityThreshold(t float32) {
        r.similarityThreshold = t
}

// VerifySignature verifies the GitHub webhook HMAC-SHA256 signature.
// Returns true if the signature matches, false otherwise.
// GitHub sends: X-Hub-Signature-256: sha256=<hex-hmac>
//
// v3.8.4: When webhookSecret is empty, returns false (refuses to verify)
// instead of true (skip verification). The previous behavior allowed
// forged webhooks to drive Sentinel into fetching arbitrary diff_url
// values with the GitHub PAT attached — a forced-PR-comment vector.
// Operators who want webhook review must set sentinel.githubWebhookSecret.
// The handler returns 401 in this case so the misconfiguration is loud.
func (r *Reviewer) VerifySignature(payload []byte, signature string) bool {
        if r.webhookSecret == "" {
                // No secret configured — refuse to verify. The webhook is
                // unauthenticated and must not be trusted.
                log.Printf("[prreview] SECURITY: no webhook secret configured — refusing to verify signature. Set sentinel.githubWebhookSecret to enable PR review.")
                return false
        }

        // Strip "sha256=" prefix
        sig := strings.TrimPrefix(signature, "sha256=")

        mac := hmac.New(sha256.New, []byte(r.webhookSecret))
        mac.Write(payload)
        expectedMAC := hex.EncodeToString(mac.Sum(nil))

        return hmac.Equal([]byte(sig), []byte(expectedMAC))
}

// HandleWebhook is the HTTP handler for POST /webhook/github.
// It parses the event, dispatches to the right handler, and returns 200.
func (r *Reviewer) HandleWebhook(w http.ResponseWriter, req *http.Request) {
        if req.Method != "POST" {
                http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
                return
        }

        // v3.16.0: Limit body size BEFORE signature verification to prevent
        // unauthenticated DoS (10GB POST would OOM the 256Mi pod).
        req.Body = http.MaxBytesReader(w, req.Body, 1<<20) // 1MB
        payload, err := io.ReadAll(req.Body)
        if err != nil {
                http.Error(w, "read body failed", http.StatusBadRequest)
                return
        }

        // Verify signature
        sig := req.Header.Get("X-Hub-Signature-256")
        if !r.VerifySignature(payload, sig) {
                log.Printf("[prreview] signature verification failed")
                http.Error(w, "invalid signature", http.StatusUnauthorized)
                return
        }

        eventType := req.Header.Get("X-GitHub-Event")
        switch eventType {
        case "pull_request":
                r.handlePullRequest(req.Context(), payload)
        case "ping":
                log.Printf("[prreview] received ping event")
        default:
                // Ignore other events
        }

        w.WriteHeader(http.StatusOK)
        w.Write([]byte(`{"status":"ok"}`))
}

// handlePullRequest processes a pull_request webhook event.
// v3.5.0: On "opened" or "synchronize" actions, fetches the diff, scores it
// against incident memory, and posts a review comment if similarity is high.
func (r *Reviewer) handlePullRequest(ctx context.Context, payload []byte) {
        var event struct {
                Action string `json:"action"`
                Number int    `json:"number"`
                PullRequest struct {
                        Number  int    `json:"number"`
                        DiffURL string `json:"diff_url"`
                        Title   string `json:"title"`
                        Body    string `json:"body"`
                        Base    struct {
                                Repo struct {
                                        Name  string `json:"name"`
                                        Owner struct {
                                                Login string `json:"login"`
                                        } `json:"owner"`
                                } `json:"repo"`
                        } `json:"base"`
                        Head struct {
                                SHA string `json:"sha"`
                        } `json:"head"`
                } `json:"pull_request"`
                Repository struct {
                        Name  string `json:"name"`
                        Owner struct {
                                Login string `json:"login"`
                        } `json:"owner"`
                } `json:"repository"`
        }

        if err := json.Unmarshal(payload, &event); err != nil {
                log.Printf("[prreview] failed to parse pull_request event: %v", err)
                return
        }

        // Only review on opened or synchronize (new commits pushed)
        if event.Action != "opened" && event.Action != "synchronize" {
                return
        }

        repo := event.Repository.Owner.Login + "/" + event.Repository.Name
        prNumber := event.PullRequest.Number
        headSHA := event.PullRequest.Head.SHA

        log.Printf("[prreview] reviewing PR #%d in %s (action=%s)", prNumber, repo, event.Action)

        // v3.18.0: SECURITY — never trust diff_url from the webhook body (SSRF + PAT exfil).
        // Rebuild the diff URL from the trusted apiURL + repo + PR number.
        diffURLOverride := r.apiURL + "/repos/" + repo + "/pulls/" + fmt.Sprintf("%d", prNumber)
        diff, err := r.fetchPRDiff(ctx, diffURLOverride)
        if err != nil {
                log.Printf("[prreview] failed to fetch diff for PR #%d: %v", prNumber, err)
                return
        }

        // Chunk the diff by file
        chunks := ChunkDiff(diff)
        if len(chunks) == 0 {
                log.Printf("[prreview] no chunks in PR #%d diff", prNumber)
                return
        }

        // Score each chunk against incident memory
        var warnings []Warning

        for _, chunk := range chunks {
                metas, scores, err := r.vectorStore.Search(ctx, chunk, 5)
                if err != nil || len(metas) == 0 {
                        continue
                }
                // Look for high-similarity matches, especially failures
                for i, meta := range metas {
                        if scores[i] >= r.similarityThreshold {
                                // Prioritize failures (they're the ones we want to prevent)
                                if meta.Outcome == "failure" || i == 0 {
                                        warnings = append(warnings, Warning{
                                                Chunk:    chunk,
                                                Sim:      scores[i],
                                                Incident: meta,
                                        })
                                        break // one warning per chunk
                                }
                        }
                }
        }

        if len(warnings) == 0 {
                log.Printf("[prreview] PR #%d: no risky changes detected (checked %d chunks)", prNumber, len(chunks))
                return
        }

        // Post a review comment
        comment := r.buildReviewComment(warnings)
        if err := r.postReviewComment(ctx, repo, prNumber, headSHA, comment); err != nil {
                log.Printf("[prreview] failed to post review comment on PR #%d: %v", prNumber, err)
                return
        }

        log.Printf("[prreview] posted review comment on PR #%d: %d warnings", prNumber, len(warnings))
}

// fetchPRDiff fetches the PR diff text via the GitHub API.
// v3.18.0: diffURL must be a trusted URL reconstructed from r.apiURL — NEVER
// the diff_url field from the webhook body, which is an SSRF + PAT exfil
// vector (an attacker who can send a webhook can substitute their own URL).
func (r *Reviewer) fetchPRDiff(ctx context.Context, diffURL string) (string, error) {
        req, err := http.NewRequestWithContext(ctx, "GET", diffURL, nil)
        if err != nil {
                return "", err
        }
        req.Header.Set("Accept", "application/vnd.github.v3.diff")
        req.Header.Set("Authorization", "Bearer "+r.authToken(ctx))

        resp, err := r.httpClient.Do(req)
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()

        if resp.StatusCode != 200 {
                return "", fmt.Errorf("diff fetch returned %d", resp.StatusCode)
        }

        body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB cap
        if err != nil {
                return "", err
        }
        return string(body), nil
}

// diffFileHeaderRe matches the start of a new file section in a unified diff.
var diffFileHeaderRe = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)

// ChunkDiff splits a unified diff into per-file chunks.
// Each chunk starts with "diff --git" and includes all hunks for that file.
// v3.5.0: We chunk by file (not by hunk) because a single file's changes
// are usually coherent enough for semantic similarity matching.
func ChunkDiff(diff string) []string {
        if diff == "" {
                return nil
        }

        lines := strings.Split(diff, "\n")
        var chunks []string
        var current []string

        for _, line := range lines {
                if diffFileHeaderRe.MatchString(line) {
                        // Start of a new file — flush the previous chunk
                        if len(current) > 0 {
                                chunks = append(chunks, strings.Join(current, "\n"))
                        }
                        current = []string{line}
                } else {
                        if len(current) == 0 {
                                // Preamble before the first "diff --git" — skip
                                continue
                        }
                        current = append(current, line)
                }
        }
        // Flush the last chunk
        if len(current) > 0 {
                chunks = append(chunks, strings.Join(current, "\n"))
        }

        return chunks
}

// buildReviewComment constructs the Markdown review body from the warnings.
func (r *Reviewer) buildReviewComment(warnings []Warning) string {
        var b strings.Builder
        b.WriteString("## Sentinel Predictive Review\n\n")
        b.WriteString(":warning: This PR resembles past incidents that Sentinel has diagnosed. ")
        b.WriteString("Please review the similarities before merging.\n\n")

        for i, w := range warnings {
                // Truncate the chunk for readability
                chunkPreview := w.Chunk
                if len(chunkPreview) > 300 {
                        chunkPreview = chunkPreview[:300] + "..."
                }

                outcomeEmoji := ":white_check_mark:"
                if w.Incident.Outcome == "failure" {
                        outcomeEmoji = ":x:"
                }

                b.WriteString(fmt.Sprintf("### Warning %d (similarity: %.0f%%)\n", i+1, w.Sim*100))
                b.WriteString(fmt.Sprintf("%s **Incident %s** (outcome: %s)\n", outcomeEmoji, w.Incident.IncidentID, w.Incident.Outcome))
                b.WriteString(fmt.Sprintf("- **Root cause:** %s\n", w.Incident.RootCause))
                b.WriteString(fmt.Sprintf("- **Fix pattern:** %s\n", w.Incident.FixPattern))
                b.WriteString(fmt.Sprintf("- **Service:** %s\n\n", w.Incident.Service))
                b.WriteString("```diff\n")
                b.WriteString(chunkPreview)
                b.WriteString("\n```\n\n")
        }

        b.WriteString("---\n")
        b.WriteString("_This review was generated by Sentinel's predictive PR review. ")
        b.WriteString("It compares your changes against past incident memory to catch ")
        b.WriteString("known-bad patterns before they reach production._\n")
        return b.String()
}

// postReviewComment posts a review comment on the PR via the GitHub API.
// Uses POST /repos/{owner}/{repo}/pulls/{pr_number}/reviews with event=COMMENT.
func (r *Reviewer) postReviewComment(ctx context.Context, repo string, prNumber int, headSHA, body string) error {
        // Parse repo into owner/repo
        parts := strings.Split(repo, "/")
        if len(parts) != 2 {
                return fmt.Errorf("invalid repo format: %s", repo)
        }
        owner, repoName := parts[0], parts[1]

        reviewBody := map[string]any{
                "body":  body,
                "event": "COMMENT",
                "commit_id": headSHA,
        }
        payload, err := json.Marshal(reviewBody)
        if err != nil {
                return err
        }

        url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", r.apiURL, owner, repoName, prNumber)
        req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
        if err != nil {
                return err
        }
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("Authorization", "Bearer "+r.authToken(ctx))
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")

        resp, err := r.httpClient.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()

        if resp.StatusCode >= 400 {
                body, _ := io.ReadAll(resp.Body)
                return fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
        }
        return nil
}


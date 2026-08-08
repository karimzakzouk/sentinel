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
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Reviewer scores PR diffs against the HNSW incident memory.
type Reviewer struct {
	githubToken         string
	githubApp           GitHubAppTokenProvider
	webhookSecret       string
	vectorStore         VectorStore
	similarityThreshold float32
	httpClient          *http.Client
	apiURL              string

	deliveryMu      sync.Mutex
	deliverySeen    map[string]time.Time
	deliveryMaxSize int
}

// GitHubAppTokenProvider is implemented by github.App. When set, the
// Reviewer uses the App's installation token for API calls so review
// comments appear as the verified Sentinel bot. Falls back to the PAT on
// error.
type GitHubAppTokenProvider interface {
	GetInstallToken(ctx context.Context) (string, error)
}

// WithGitHubApp wires a GitHub App for verified-bot-identity PR comments.
func (r *Reviewer) WithGitHubApp(app GitHubAppTokenProvider) *Reviewer {
	r.githubApp = app
	return r
}

// authToken returns the token to use for GitHub API calls. Prefers the
// GitHub App installation token when configured; falls back to the PAT.
func (r *Reviewer) authToken(ctx context.Context) string {
	if r.githubApp != nil {
		if token, err := r.githubApp.GetInstallToken(ctx); err == nil && token != "" {
			return token
		}
	}
	return r.githubToken
}

// Warning is a chunk of the PR diff that resembles a past incident.
type Warning struct {
	Chunk    string
	Sim      float32
	Incident *VectorMetadata
}

// VectorStore is the interface for incident memory retrieval. Satisfied by
// feedback.VectorStore (via an adapter in main.go).
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
	Outcome    string
}

// New creates a Reviewer.
func New(githubToken, webhookSecret string, vs VectorStore) *Reviewer {
	return &Reviewer{
		githubToken:         githubToken,
		webhookSecret:       webhookSecret,
		vectorStore:         vs,
		similarityThreshold: 0.75,
		httpClient:          &http.Client{Timeout: 30 * time.Second},
		apiURL:              "https://api.github.com",
		deliverySeen:        make(map[string]time.Time),
		deliveryMaxSize:     1000,
	}
}

// isDuplicateDelivery reports whether the delivery ID has been seen
// recently. Entries expire after 1 hour; the map is hard-capped at
// deliveryMaxSize with LRU-style eviction of the oldest 25%.
func (r *Reviewer) isDuplicateDelivery(deliveryID string) bool {
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)
	for id, ts := range r.deliverySeen {
		if ts.Before(cutoff) {
			delete(r.deliverySeen, id)
		}
	}
	if len(r.deliverySeen) >= r.deliveryMaxSize {
		type kv struct {
			k string
			v time.Time
		}
		var entries []kv
		for k, v := range r.deliverySeen {
			entries = append(entries, kv{k, v})
		}
		// Evict oldest ~25% of entries.
		for i := 0; i < len(entries)/4; i++ {
			delete(r.deliverySeen, entries[i].k)
		}
	}

	if _, seen := r.deliverySeen[deliveryID]; seen {
		return true
	}
	r.deliverySeen[deliveryID] = now
	return false
}

// WithAPIURL sets the GitHub API base URL. For GitHub Enterprise, pass
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

// VerifySignature verifies the GitHub webhook HMAC-SHA256 signature. With
// no secret configured, verification is refused — operators must set
// sentinel.githubWebhookSecret to enable PR review.
func (r *Reviewer) VerifySignature(payload []byte, signature string) bool {
	if r.webhookSecret == "" {
		logging.Info("prreview: refusing to verify signature — no webhook secret configured")
		return false
	}
	sig := strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(r.webhookSecret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expectedMAC))
}

// HandleWebhook is the HTTP handler for POST /webhook/github.
func (r *Reviewer) HandleWebhook(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cap the body at 1MB to avoid unauthenticated DoS.
	req.Body = http.MaxBytesReader(w, req.Body, 1<<20)
	payload, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	sig := req.Header.Get("X-Hub-Signature-256")
	if !r.VerifySignature(payload, sig) {
		logging.Error("prreview: signature verification failed")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	deliveryID := req.Header.Get("X-GitHub-Delivery")
	if deliveryID != "" {
		if r.isDuplicateDelivery(deliveryID) {
			logging.Info("prreview: duplicate delivery acked", "delivery_id", deliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	eventType := req.Header.Get("X-GitHub-Event")
	switch eventType {
	case "pull_request":
		r.handlePullRequest(req.Context(), payload)
	case "ping":
		logging.Info("prreview: received ping event")
	default:
		// Ignore other events.
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handlePullRequest processes a pull_request webhook event: fetches the
// diff, chunks it, scores each chunk against incident memory, and posts a
// review comment if similarity is high.
func (r *Reviewer) handlePullRequest(ctx context.Context, payload []byte) {
	var event struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
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
		logging.Error("prreview: failed to parse pull_request event", "err", err)
		return
	}

	if event.Action != "opened" && event.Action != "synchronize" {
		return
	}

	repo := event.Repository.Owner.Login + "/" + event.Repository.Name
	prNumber := event.PullRequest.Number
	headSHA := event.PullRequest.Head.SHA

	logging.Info("prreview: reviewing PR",
		"repo", repo, "pr", prNumber, "action", event.Action)

	// Rebuild the diff URL from the trusted apiURL + repo + PR number to
	// avoid trusting the webhook-supplied diff_url field (SSRF + PAT exfil).
	diffURL := r.apiURL + "/repos/" + repo + "/pulls/" + fmt.Sprintf("%d", prNumber)
	diff, err := r.fetchPRDiff(ctx, diffURL)
	if err != nil {
		logging.Error("prreview: failed to fetch diff", "repo", repo, "pr", prNumber, "err", err)
		return
	}

	chunks := ChunkDiff(diff)
	if len(chunks) == 0 {
		logging.Info("prreview: no chunks in diff", "repo", repo, "pr", prNumber)
		return
	}

	var warnings []Warning
	for _, chunk := range chunks {
		metas, scores, err := r.vectorStore.Search(ctx, chunk, 5)
		if err != nil || len(metas) == 0 {
			continue
		}
		for i, meta := range metas {
			if scores[i] >= r.similarityThreshold {
				// Prioritize failures (they're the ones we want to prevent).
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
		logging.Info("prreview: no risky changes detected",
			"repo", repo, "pr", prNumber, "chunks", len(chunks))
		return
	}

	comment := r.buildReviewComment(warnings)
	if err := r.postReviewComment(ctx, repo, prNumber, headSHA, comment); err != nil {
		logging.Error("prreview: failed to post review comment",
			"repo", repo, "pr", prNumber, "err", err)
		return
	}

	logging.Warn("prreview: posted review comment",
		"repo", repo, "pr", prNumber, "warnings", len(warnings))
}

// fetchPRDiff fetches the PR diff text via the GitHub API. The URL is
// constructed from the trusted apiURL, not the webhook-supplied diff_url.
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

// ChunkDiff splits a unified diff into per-file chunks. Each chunk starts
// with "diff --git" and includes all hunks for that file. Per-file chunks
// are coherent enough for semantic similarity matching.
func ChunkDiff(diff string) []string {
	if diff == "" {
		return nil
	}

	lines := strings.Split(diff, "\n")
	var chunks []string
	var current []string

	for _, line := range lines {
		if diffFileHeaderRe.MatchString(line) {
			if len(current) > 0 {
				chunks = append(chunks, strings.Join(current, "\n"))
			}
			current = []string{line}
		} else {
			if len(current) == 0 {
				// Preamble before the first "diff --git" — skip.
				continue
			}
			current = append(current, line)
		}
	}
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
		chunkPreview := w.Chunk
		if len(chunkPreview) > 300 {
			chunkPreview = chunkPreview[:300] + "..."
		}

		outcomeEmoji := ":white_check_mark:"
		if w.Incident.Outcome == "failure" {
			outcomeEmoji = ":x:"
		}

		fmt.Fprintf(&b, "### Warning %d (similarity: %.0f%%)\n", i+1, w.Sim*100)
		fmt.Fprintf(&b, "%s **Incident %s** (outcome: %s)\n", outcomeEmoji, w.Incident.IncidentID, w.Incident.Outcome)
		fmt.Fprintf(&b, "- **Root cause:** %s\n", w.Incident.RootCause)
		fmt.Fprintf(&b, "- **Fix pattern:** %s\n", w.Incident.FixPattern)
		fmt.Fprintf(&b, "- **Service:** %s\n\n", w.Incident.Service)
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
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format: %s", repo)
	}
	owner, repoName := parts[0], parts[1]

	reviewBody := map[string]any{
		"body":      body,
		"event":     "COMMENT",
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

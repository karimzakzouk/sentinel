package prreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockVectorStore implements VectorStore for testing.
type mockVectorStore struct {
	metas  []*VectorMetadata
	scores []float32
	mu     sync.Mutex
	calls  int
}

func (m *mockVectorStore) Search(ctx context.Context, text string, k int) ([]*VectorMetadata, []float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.metas, m.scores, nil
}

func (m *mockVectorStore) UsingHashFallback() bool { return false }

// TestChunkDiff verifies that a unified diff is correctly split into
// per-file chunks. Each chunk starts with "diff --git".
func TestChunkDiff(t *testing.T) {
	diff := `diff --git a/main.py b/main.py
index 1234567..abcdefg 100644
--- a/main.py
+++ b/main.py
@@ -10,7 +10,7 @@
-def foo():
+def bar():
     pass
diff --git a/utils.py b/utils.py
index 1234567..abcdefg 100644
--- a/utils.py
+++ b/utils.py
@@ -1,3 +1,3 @@
-old_value
+new_value
diff --git a/README.md b/README.md
index 1234567..abcdefg 100644
--- a/README.md
+++ b/README.md
@@ -1,1 +1,1 @@
-old
+new
`
	chunks := ChunkDiff(diff)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (one per file), got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "diff --git a/main.py") {
		t.Errorf("chunk 0 should start with main.py, got: %s", chunks[0][:50])
	}
	if !strings.HasPrefix(chunks[1], "diff --git a/utils.py") {
		t.Errorf("chunk 1 should start with utils.py, got: %s", chunks[1][:50])
	}
	if !strings.HasPrefix(chunks[2], "diff --git a/README.md") {
		t.Errorf("chunk 2 should start with README.md, got: %s", chunks[2][:50])
	}
}

// TestChunkDiffEmpty verifies that empty input returns nil.
func TestChunkDiffEmpty(t *testing.T) {
	if chunks := ChunkDiff(""); chunks != nil {
		t.Errorf("expected nil for empty diff, got %v", chunks)
	}
}

// TestChunkDiff_PreambleSkipped verifies that any text before the first
// "diff --git" header is dropped (e.g. an "X of Y files changed" preamble).
func TestChunkDiff_PreambleSkipped(t *testing.T) {
	diff := "preamble line\nrandom stats\ndiff --git a/foo b/foo\n+hello\n"
	chunks := ChunkDiff(diff)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "diff --git a/foo") {
		t.Errorf("chunk 0 should start with diff --git, got: %q", chunks[0][:40])
	}
}

// TestVerifySignature_Valid verifies that a correct HMAC-SHA256 signature
// is accepted.
func TestVerifySignature_Valid(t *testing.T) {
	secret := "my-webhook-secret"
	payload := []byte(`{"action":"opened"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	r := New("token", secret, nil)
	if !r.VerifySignature(payload, sig) {
		t.Error("valid signature was rejected")
	}
}

// TestVerifySignature_Invalid verifies that a wrong signature is rejected.
func TestVerifySignature_Invalid(t *testing.T) {
	r := New("token", "secret", nil)
	if r.VerifySignature([]byte("payload"), "sha256=deadbeef") {
		t.Error("invalid signature was accepted")
	}
}

// TestVerifySignature_NoSecret verifies that with no secret configured,
// verification is refused (forged webhooks could exfiltrate the PAT).
func TestVerifySignature_NoSecret(t *testing.T) {
	r := New("token", "", nil)
	if r.VerifySignature([]byte("payload"), "sha256=anything") {
		t.Error("with no secret configured, all signatures should be refused")
	}
}

// TestHandleWebhook_Ping verifies the ping event returns 200 when a valid
// webhook secret is configured and the signature verifies.
func TestHandleWebhook_Ping(t *testing.T) {
	secret := "test-webhook-secret"
	r := New("token", secret, &mockVectorStore{})

	payload := []byte(`{}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(string(payload)))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()

	r.HandleWebhook(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestHandleWebhook_MethodNotAllowed verifies GET is rejected.
func TestHandleWebhook_MethodNotAllowed(t *testing.T) {
	r := New("token", "", &mockVectorStore{})

	req := httptest.NewRequest("GET", "/webhook/github", nil)
	w := httptest.NewRecorder()

	r.HandleWebhook(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestHandleWebhook_InvalidSignature verifies that a bad signature returns 401.
func TestHandleWebhook_InvalidSignature(t *testing.T) {
	r := New("token", "secret", &mockVectorStore{})

	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	r.HandleWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid signature, got %d", w.Code)
	}
}

// TestHandleWebhook_DuplicateDeliveryDeduped verifies that GitHub
// redeliveries (same X-GitHub-Delivery) are acked without re-processing.
func TestHandleWebhook_DuplicateDeliveryDeduped(t *testing.T) {
	secret := "dedup-secret"
	r := New("token", secret, &mockVectorStore{calls: 0})

	payload := []byte(`{}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// First delivery: acked, processed (ping handler).
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(string(payload)))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "abc-123")
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()
	r.HandleWebhook(w, req)
	if w.Code != 200 {
		t.Fatalf("first delivery: expected 200, got %d", w.Code)
	}

	// Second delivery with same ID: acked without re-processing.
	req2 := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(string(payload)))
	req2.Header.Set("X-GitHub-Event", "ping")
	req2.Header.Set("X-GitHub-Delivery", "abc-123")
	req2.Header.Set("X-Hub-Signature-256", sig)
	w2 := httptest.NewRecorder()
	r.HandleWebhook(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("duplicate delivery: expected 200, got %d", w2.Code)
	}
}

// TestBuildReviewComment verifies the Markdown output format.
func TestBuildReviewComment(t *testing.T) {
	r := New("token", "", nil)
	warnings := []Warning{
		{
			Chunk: "diff --git a/main.py b/main.py\n-old\n+new",
			Sim:   0.85,
			Incident: &VectorMetadata{
				IncidentID: "inc-123",
				RootCause:  "KeyError on DATABASE_URLL",
				FixPattern: "Fixed typo in env var name",
				Service:    "demo-app",
				Outcome:    "failure",
			},
		},
	}

	comment := r.buildReviewComment(warnings)

	if !strings.Contains(comment, "Sentinel Predictive Review") {
		t.Error("comment should contain header")
	}
	if !strings.Contains(comment, "85%") {
		t.Error("comment should contain similarity percentage")
	}
	if !strings.Contains(comment, "inc-123") {
		t.Error("comment should contain incident ID")
	}
	if !strings.Contains(comment, "KeyError on DATABASE_URLL") {
		t.Error("comment should contain root cause")
	}
	if !strings.Contains(comment, ":x:") {
		t.Error("comment should contain failure emoji for failed incidents")
	}
}

// TestBuildReviewComment_SuccessOutcome uses :white_check_mark: for success.
func TestBuildReviewComment_SuccessOutcome(t *testing.T) {
	r := New("token", "", nil)
	warnings := []Warning{
		{
			Chunk: "diff",
			Sim:   0.80,
			Incident: &VectorMetadata{
				IncidentID: "inc-456",
				Outcome:    "success",
			},
		},
	}

	comment := r.buildReviewComment(warnings)
	if !strings.Contains(comment, ":white_check_mark:") {
		t.Error("comment should contain success emoji for successful incidents")
	}
}

// TestBuildReviewComment_TruncatesLongChunk verifies that chunk previews
// longer than 300 chars are truncated with "...".
func TestBuildReviewComment_TruncatesLongChunk(t *testing.T) {
	r := New("token", "", nil)
	longChunk := "diff --git a/x b/x\n+" + strings.Repeat("x", 500)
	warnings := []Warning{
		{
			Chunk: longChunk,
			Sim:   0.9,
			Incident: &VectorMetadata{
				IncidentID: "inc-1",
				Outcome:    "failure",
			},
		},
	}
	comment := r.buildReviewComment(warnings)
	if !strings.Contains(comment, "...") {
		t.Error("comment should contain truncation marker")
	}
}

// TestSetSimilarityThreshold verifies the threshold is configurable.
func TestSetSimilarityThreshold(t *testing.T) {
	r := New("token", "", nil)
	if r.similarityThreshold != 0.75 {
		t.Errorf("default threshold = %v, want 0.75", r.similarityThreshold)
	}
	r.SetSimilarityThreshold(0.90)
	if r.similarityThreshold != 0.90 {
		t.Errorf("threshold = %v, want 0.90", r.similarityThreshold)
	}
}

// TestWithAPIURL verifies that a custom API URL is honored (trailing slash
// trimmed).
func TestWithAPIURL(t *testing.T) {
	r := New("token", "", nil).WithAPIURL("https://gh.mycorp.internal/api/v3/")
	if r.apiURL != "https://gh.mycorp.internal/api/v3" {
		t.Errorf("expected trimmed URL, got %q", r.apiURL)
	}
}

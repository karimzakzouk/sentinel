package slackbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestRequest creates an HTTP request for testing.
func newTestRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req
}

// newTestResponse creates a ResponseRecorder for testing.
func newTestResponse() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

// TestDispatchCommand_Pause verifies the pause command works.
func TestDispatchCommand_Pause(t *testing.T) {
	b := New("secret", "U123", nil, nil)

	resp := b.dispatchCommand("pause for 30 minutes", "U456")
	if !strings.Contains(resp, "paused for 30 minutes") {
		t.Errorf("expected 'paused for 30 minutes', got: %s", resp)
	}
	if !b.IsPaused() {
		t.Error("bot should be paused after pause command")
	}
}

// TestDispatchCommand_Resume verifies the resume command works.
func TestDispatchCommand_Resume(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	b.Pause(1*time.Hour, "test")

	resp := b.dispatchCommand("resume", "U456")
	if !strings.Contains(resp, "resumed") {
		t.Errorf("expected 'resumed', got: %s", resp)
	}
	if b.IsPaused() {
		t.Error("bot should not be paused after resume command")
	}
}

// TestDispatchCommand_Status verifies the status command.
func TestDispatchCommand_Status(t *testing.T) {
	b := New("secret", "U123", nil, nil)

	resp := b.dispatchCommand("status", "U456")
	if !strings.Contains(resp, "ACTIVE") {
		t.Errorf("active status should contain 'ACTIVE', got: %s", resp)
	}

	b.Pause(30*time.Minute, "test")
	resp = b.dispatchCommand("status", "U456")
	if !strings.Contains(resp, "PAUSED") {
		t.Errorf("paused status should contain 'PAUSED', got: %s", resp)
	}
}

// TestDispatchCommand_Help verifies the help command.
func TestDispatchCommand_Help(t *testing.T) {
	b := New("secret", "U123", nil, nil)

	resp := b.dispatchCommand("help", "U456")
	if !strings.Contains(resp, "Commands:") {
		t.Errorf("help should list commands, got: %s", resp)
	}
	if !strings.Contains(resp, "pause") {
		t.Error("help should mention pause command")
	}
}

// TestDispatchCommand_EmptyReturnsHelp verifies empty input returns help.
func TestDispatchCommand_EmptyReturnsHelp(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	resp := b.dispatchCommand("", "U456")
	if !strings.Contains(resp, "Commands:") {
		t.Errorf("empty command should return help, got: %s", resp)
	}
}

// TestDispatchCommand_Investigate verifies the investigate command.
func TestDispatchCommand_Investigate(t *testing.T) {
	called := false
	var calledService string
	b := New("secret", "U123",
		func(service, namespace, reason string) (string, error) {
			called = true
			calledService = service
			return "cluster-123", nil
		},
		nil,
	)

	resp := b.dispatchCommand("investigate my-api", "U456")
	if !called {
		t.Error("investigate function should have been called")
	}
	if calledService != "my-api" {
		t.Errorf("called service = %q, want my-api", calledService)
	}
	if !strings.Contains(resp, "my-api") {
		t.Errorf("response should mention service, got: %s", resp)
	}
	if !strings.Contains(resp, "cluster-123") {
		t.Errorf("response should mention cluster ID, got: %s", resp)
	}
}

// TestDispatchCommand_List verifies the list command.
func TestDispatchCommand_List(t *testing.T) {
	b := New("secret", "U123", nil,
		func(ctx context.Context, limit int) ([]map[string]any, error) {
			if limit != 5 {
				t.Errorf("expected limit 5, got %d", limit)
			}
			return []map[string]any{
				{"incident_id": "inc-1", "service": "api", "status": "verified"},
				{"incident_id": "inc-2", "service": "web", "status": "failed"},
			}, nil
		},
	)

	resp := b.dispatchCommand("show me the last 5 incidents", "U456")
	if !strings.Contains(resp, "inc-1") {
		t.Errorf("response should contain inc-1, got: %s", resp)
	}
	if !strings.Contains(resp, "inc-2") {
		t.Errorf("response should contain inc-2, got: %s", resp)
	}
}

// TestDispatchCommand_InvestigateNoService verifies the usage message when
// no service is provided.
func TestDispatchCommand_InvestigateNoService(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	resp := b.dispatchCommand("investigate", "U456")
	if !strings.Contains(resp, "Usage:") {
		t.Errorf("expected usage message, got: %s", resp)
	}
}

// TestDispatchCommand_Unknown verifies an unknown command falls through
// to the help text.
func TestDispatchCommand_Unknown(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	resp := b.dispatchCommand("do the thing", "U456")
	if !strings.Contains(resp, "Unknown command") {
		t.Errorf("expected 'Unknown command' prefix, got: %s", resp)
	}
}

// TestExtractMinutes verifies duration parsing.
func TestExtractMinutes(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"pause for 30 minutes", 30},
		{"pause for 30m", 30},
		{"pause 60", 60},
		{"pause for 1 hour", 60},
		{"pause for 2 hours", 120},
		{"pause", 0},                  // no number
		{"pause for five minutes", 0}, // non-numeric
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := extractMinutes(c.in)
			if got != c.want {
				t.Errorf("extractMinutes(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestPauseExpiry verifies that a pause auto-expires after the duration.
func TestPauseExpiry(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	b.Pause(50*time.Millisecond, "test")

	if !b.IsPaused() {
		t.Error("should be paused immediately after Pause()")
	}

	time.Sleep(60 * time.Millisecond)

	if b.IsPaused() {
		t.Error("should auto-resume after duration expires")
	}
}

// TestIsPaused_Concurrent verifies that IsPaused is safe to call
// concurrently with Pause/Resume.
func TestIsPaused_Concurrent(t *testing.T) {
	b := New("secret", "U123", nil, nil)
	done := make(chan struct{})

	go func() {
		for range 100 {
			b.Pause(1*time.Millisecond, "test")
			b.Resume()
		}
		close(done)
	}()

	for range 100 {
		_ = b.IsPaused()
	}

	<-done
}

// TestHandleEvents_URLVerification verifies the Slack URL verification
// challenge is echoed back.
func TestHandleEvents_URLVerification(t *testing.T) {
	b := New("", "U123", nil, nil)

	body := `{"type":"url_verification","challenge":"test-challenge-123"}`
	req := newTestRequest("POST", "/slack/events", body)
	w := newTestResponse()

	b.HandleEvents(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test-challenge-123") {
		t.Errorf("response should echo challenge, got: %s", w.Body.String())
	}
}

// TestHandleEvents_SignatureVerification verifies that requests with a
// signing secret set are rejected without proper HMAC signature.
func TestHandleEvents_SignatureVerification(t *testing.T) {
	b := New("my-secret", "U123", nil, nil)

	body := `{"type":"url_verification","challenge":"test"}`
	req := newTestRequest("POST", "/slack/events", body)
	// No signature headers — should be rejected.
	w := newTestResponse()

	b.HandleEvents(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for missing signature, got %d", w.Code)
	}
}

// TestSlackbot_SignatureVerification is the end-to-end test for Slack
// signature verification: a properly signed request is accepted, a
// tampered request is rejected, a stale timestamp is rejected.
func TestSlackbot_SignatureVerification(t *testing.T) {
	secret := "test-signing-secret"
	b := New(secret, "U123", nil, nil)

	// 1. Properly signed request — accepted.
	body := []byte(`{"type":"url_verification","challenge":"abc"}`)
	ts := time.Now().Unix()
	sig := signSlackBody(secret, ts, body)

	req := newTestRequest("POST", "/slack/events", string(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", sig)
	w := newTestResponse()

	b.HandleEvents(w, req)
	if w.Code != 200 {
		t.Errorf("signed request: expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	// 2. Tampered body — rejected.
	tampered := []byte(`{"type":"url_verification","challenge":"EVIL"}`)
	req2 := newTestRequest("POST", "/slack/events", string(tampered))
	req2.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req2.Header.Set("X-Slack-Signature", sig)
	w2 := newTestResponse()

	b.HandleEvents(w2, req2)
	if w2.Code != 401 {
		t.Errorf("tampered request: expected 401, got %d", w2.Code)
	}

	// 3. Stale timestamp (>5min old) — rejected.
	staleTS := time.Now().Add(-10 * time.Minute).Unix()
	staleSig := signSlackBody(secret, staleTS, body)
	req3 := newTestRequest("POST", "/slack/events", string(body))
	req3.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(staleTS, 10))
	req3.Header.Set("X-Slack-Signature", staleSig)
	w3 := newTestResponse()

	b.HandleEvents(w3, req3)
	if w3.Code != 401 {
		t.Errorf("stale request: expected 401, got %d", w3.Code)
	}

	// 4. Future timestamp (>60s ahead) — rejected.
	futureTS := time.Now().Add(5 * time.Minute).Unix()
	futureSig := signSlackBody(secret, futureTS, body)
	req4 := newTestRequest("POST", "/slack/events", string(body))
	req4.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(futureTS, 10))
	req4.Header.Set("X-Slack-Signature", futureSig)
	w4 := newTestResponse()

	b.HandleEvents(w4, req4)
	if w4.Code != 401 {
		t.Errorf("future request: expected 401, got %d", w4.Code)
	}
}

// signSlackBody computes the X-Slack-Signature header value for a body +
// secret + timestamp.
func signSlackBody(secret string, ts int64, body []byte) string {
	base := "v0:" + strconv.FormatInt(ts, 10) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

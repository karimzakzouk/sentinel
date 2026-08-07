package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// noopStore is a minimal Store for testing — returns empty results.
type noopStore struct{}

func (n *noopStore) ListRecentIncidents(ctx context.Context, limit int) ([]*schemas.Incident, error) {
	return nil, nil
}
func (n *noopStore) GetIncident(ctx context.Context, id uuid.UUID) (*schemas.Incident, error) {
	return nil, nil
}
func (n *noopStore) FindIncidentByPrefix(ctx context.Context, prefix string) (*schemas.Incident, error) {
	return nil, nil
}

// stubStore lets a test customize the Store behavior inline.
type stubStore struct {
	list    []*schemas.Incident
	listErr error
	get     *schemas.Incident
	getErr  error
	find    *schemas.Incident
	findErr error
}

func (s *stubStore) ListRecentIncidents(ctx context.Context, limit int) ([]*schemas.Incident, error) {
	return s.list, s.listErr
}
func (s *stubStore) GetIncident(ctx context.Context, id uuid.UUID) (*schemas.Incident, error) {
	return s.get, s.getErr
}
func (s *stubStore) FindIncidentByPrefix(ctx context.Context, prefix string) (*schemas.Incident, error) {
	return s.find, s.findErr
}

// fakePauseController records Pause/Resume calls for assertions.
type fakePauseController struct {
	mu          sync.Mutex
	paused      bool
	pauseDur    time.Duration
	pauseReason string
}

func (f *fakePauseController) IsPaused() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}

func (f *fakePauseController) Pause(d time.Duration, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = true
	f.pauseDur = d
	f.pauseReason = reason
}

func (f *fakePauseController) Resume() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = false
}

// newTestServer builds a Server with a noop store + always-ready ready checker.
func newTestServer() *Server {
	return New(":0", &noopStore{},
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "cluster-1", nil },
		func() bool { return true })
}

// TestHandleSSEMaxSubscribers verifies the SSE subscriber cap. Opening
// more than maxSSESubscribers concurrent connections should return 503.
func TestHandleSSEMaxSubscribers(t *testing.T) {
	s := newTestServer()

	// Fill eventSubs to the cap directly.
	s.eventSubsMu.Lock()
	for i := 0; i < maxSSESubscribers; i++ {
		s.eventSubs[make(chan ServerEvent, 16)] = true
	}
	s.eventSubsMu.Unlock()

	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()
	s.handleSSE(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when subscribers at cap, got %d", w.Code)
	}
}

// TestReadBodyMaxBytes verifies bodies larger than maxRequestBodyBytes are
// rejected.
func TestReadBodyMaxBytes(t *testing.T) {
	largeBody := strings.Repeat("x", maxRequestBodyBytes+100)
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(largeBody))

	_, err := readBody(req)
	if err == nil {
		t.Error("expected error when reading body > 1MB, got nil")
	}
}

// TestHandleSettingsRejectsDelete verifies non-GET/POST methods return 405.
func TestHandleSettingsRejectsDelete(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest("DELETE", "/settings", nil)
	w := httptest.NewRecorder()
	s.handleSettings(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for DELETE /settings, got %d", w.Code)
	}
}

// TestHandleInvestigateMissingService verifies POST /admin/investigate
// without a service field returns 400.
func TestHandleInvestigateMissingService(t *testing.T) {
	s := newTestServer()

	body := `{"namespace":"sim"}`
	req := httptest.NewRequest("POST", "/admin/investigate", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleInvestigate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing service, got %d", w.Code)
	}
}

// TestServer_BearerAuth verifies the bearer-token middleware:
//   - With apiToken set, admin endpoints require 'Authorization: Bearer <token>'.
//   - Missing token → 401; wrong token → 401; correct token → 200.
//   - Public endpoints (healthz, readyz, metrics, /) are always allowed.
func TestServer_BearerAuth(t *testing.T) {
	const token = "secret-token"

	cases := []struct {
		name       string
		tokenSet   bool
		path       string
		method     string
		authHeader string
		wantCode   int
	}{
		{"no token configured — admin allowed", false, "/admin/pause", "POST", "", 200},
		{"token set — no auth header", true, "/admin/pause", "POST", "", 401},
		{"token set — wrong token", true, "/admin/pause", "POST", "Bearer wrong", 401},
		{"token set — malformed header", true, "/admin/pause", "POST", "Token xyz", 401},
		{"token set — correct token", true, "/admin/pause", "POST", "Bearer " + token, 200},
		{"token set — healthz is public", true, "/healthz", "GET", "", 200},
		{"token set — readyz is public", true, "/readyz", "GET", "", 200},
		{"token set — metrics is public", true, "/metrics", "GET", "", 200},
		{"token set — / is public", true, "/", "GET", "", 200},
		{"token set — GET /incidents is read-only", true, "/incidents", "GET", "", 200},
		{"token set — GET /providers is read-only", true, "/providers", "GET", "", 200},
		{"token set — GET /settings is read-only", true, "/settings", "GET", "", 200},
		{"token set — POST /settings requires token (none)", true, "/settings", "POST", "", 401},
		{"token set — POST /settings with token", true, "/settings", "POST", "Bearer " + token, 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer()
			if tc.tokenSet {
				s.SetAPIToken(token)
			}
			// POST /settings needs a runtime cfg saver to return 200.
			s.SetRuntimeCfgSaver(&noopCfgSaver{})
			// /admin/pause needs a pause controller to return 200.
			s.SetPauseController(&fakePauseController{})

			var body string
			if tc.method == "POST" {
				switch tc.path {
				case "/admin/pause":
					body = `{}`
				case "/settings":
					body = `{}`
				default:
					body = `{}`
				}
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			w := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("got %d, want %d (body: %s)", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

// noopCfgSaver is a RuntimeCfgSaver that always succeeds.
type noopCfgSaver struct{}

func (noopCfgSaver) SaveFromJSON(jsonBytes []byte) error { return nil }

// TestServer_SSEBroadcaster verifies that events flow from BroadcastEvent
// to connected SSE subscribers. We test the broadcaster mechanism directly
// rather than via an HTTP round-trip because httptest.ResponseRecorder
// does not support concurrent reads while the handler is still writing.
func TestServer_SSEBroadcaster(t *testing.T) {
	s := newTestServer()

	// Register a subscriber directly (same as handleSSE does).
	ch := make(chan ServerEvent, 16)
	s.eventSubsMu.Lock()
	s.eventSubs[ch] = true
	s.eventSubsMu.Unlock()
	defer func() {
		s.eventSubsMu.Lock()
		delete(s.eventSubs, ch)
		s.eventSubsMu.Unlock()
	}()

	// Broadcast an event.
	s.BroadcastEvent("incident.created", map[string]string{"id": "abc"})

	select {
	case ev := <-ch:
		if ev.Type != "incident.created" {
			t.Errorf("event Type = %q, want incident.created", ev.Type)
		}
		data, ok := ev.Data.(map[string]string)
		if !ok {
			t.Fatalf("event Data type = %T, want map[string]string", ev.Data)
		}
		if data["id"] != "abc" {
			t.Errorf("event Data[id] = %q, want abc", data["id"])
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive broadcast event within 1s")
	}
}

// TestServer_SSEBroadcasterMultipleSubs verifies a broadcast reaches every
// connected subscriber.
func TestServer_SSEBroadcasterMultipleSubs(t *testing.T) {
	s := newTestServer()

	const numSubs = 3
	subs := make([]chan ServerEvent, numSubs)
	s.eventSubsMu.Lock()
	for i := range subs {
		subs[i] = make(chan ServerEvent, 16)
		s.eventSubs[subs[i]] = true
	}
	s.eventSubsMu.Unlock()
	defer func() {
		s.eventSubsMu.Lock()
		for _, ch := range subs {
			delete(s.eventSubs, ch)
		}
		s.eventSubsMu.Unlock()
	}()

	s.BroadcastEvent("test.event", map[string]string{"k": "v"})

	for i, ch := range subs {
		select {
		case ev := <-ch:
			if ev.Type != "test.event" {
				t.Errorf("sub %d: event Type = %q, want test.event", i, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub %d: did not receive event within 1s", i)
		}
	}
}

// TestServer_SSEBroadcasterSlowSubDropped verifies a slow subscriber (full
// channel) is dropped via the non-blocking select rather than blocking
// the broadcaster.
func TestServer_SSEBroadcasterSlowSubDropped(t *testing.T) {
	s := newTestServer()

	// Register a subscriber with a tiny buffer that we never drain.
	ch := make(chan ServerEvent, 1)
	s.eventSubsMu.Lock()
	s.eventSubs[ch] = true
	s.eventSubsMu.Unlock()
	defer func() {
		s.eventSubsMu.Lock()
		delete(s.eventSubs, ch)
		s.eventSubsMu.Unlock()
	}()

	// Broadcast 5 events. Only the first fits in the buffer; the rest are
	// dropped via the non-blocking select in BroadcastEvent.
	for i := 0; i < 5; i++ {
		s.BroadcastEvent("e", i)
	}

	// Drain: we should get at most 1 event (the first one). The broadcaster
	// must not have blocked.
	received := 0
loop:
	for {
		select {
		case <-ch:
			received++
		default:
			break loop
		}
	}
	if received > 1 {
		t.Errorf("expected at most 1 event in the buffer, got %d", received)
	}
}

// TestServer_PauseResume verifies the pause + resume endpoints wire to
// the PauseController.
func TestServer_PauseResume(t *testing.T) {
	s := newTestServer()
	pc := &fakePauseController{}
	s.SetPauseController(pc)

	// POST /admin/pause with default duration.
	req := httptest.NewRequest("POST", "/admin/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("pause: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !pc.paused {
		t.Error("PauseController.Pause was not called")
	}
	if pc.pauseDur != 30*time.Minute {
		t.Errorf("default pause duration = %v, want 30m", pc.pauseDur)
	}
	if pc.pauseReason != "paused via CLI" {
		t.Errorf("default pause reason = %q, want 'paused via CLI'", pc.pauseReason)
	}

	// POST /admin/pause with explicit duration + reason.
	req = httptest.NewRequest("POST", "/admin/pause", strings.NewReader(`{"duration_min":10,"reason":"deploy"}`))
	w = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("pause with body: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if pc.pauseDur != 10*time.Minute {
		t.Errorf("explicit pause duration = %v, want 10m", pc.pauseDur)
	}
	if pc.pauseReason != "deploy" {
		t.Errorf("explicit pause reason = %q, want 'deploy'", pc.pauseReason)
	}

	// POST /admin/resume.
	req = httptest.NewRequest("POST", "/admin/resume", nil)
	w = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("resume: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if pc.paused {
		t.Error("PauseController.Resume was not called")
	}

	// 405 on non-POST.
	req = httptest.NewRequest("GET", "/admin/pause", nil)
	w = httptest.NewRecorder()
	s.handlePause(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /admin/pause: got %d, want 405", w.Code)
	}
}

// TestServer_PauseWithoutController verifies the endpoint returns 503 when
// no pause controller is wired.
func TestServer_PauseWithoutController(t *testing.T) {
	s := newTestServer() // no SetPauseController
	req := httptest.NewRequest("POST", "/admin/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handlePause(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without pause controller, got %d", w.Code)
	}
}

// TestServer_IncidentsList verifies the GET /incidents route uses the Store
// and applies the limit query param.
func TestServer_IncidentsList(t *testing.T) {
	store := &stubStore{
		list: []*schemas.Incident{
			{IncidentID: uuid.New(), Status: schemas.StatusInvestigating},
			{IncidentID: uuid.New(), Status: schemas.StatusVerified},
		},
	}
	s := New(":0", store,
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return true })

	req := httptest.NewRequest("GET", "/incidents?limit=5", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var got []*schemas.Incident
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d incidents, want 2", len(got))
	}
}

// TestServer_APIMeta verifies /api/meta returns the endpoint list.
func TestServer_APIMeta(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/api/meta", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	endpoints, _ := resp["endpoints"].([]any)
	if len(endpoints) == 0 {
		t.Error("expected non-empty endpoints list")
	}
	// /api/meta should be in the list (self-reference).
	found := false
	for _, e := range endpoints {
		if e == "/api/meta" {
			found = true
			break
		}
	}
	if !found {
		t.Error("/api/meta not in endpoints list")
	}
}

// TestServer_ProvidersDefault verifies /providers returns a disabled
// payload when no providers-status getter is wired.
func TestServer_ProvidersDefault(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/providers", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Errorf("expected 'not configured' message, got: %s", w.Body.String())
	}
}

// TestServer_RateLimiter verifies the per-IP rate limiter blocks the 11th
// request within a minute.
func TestServer_RateLimiter(t *testing.T) {
	rl := newIPRateLimiter(10, time.Minute)
	addr := "1.2.3.4:5678"
	for i := 0; i < 10; i++ {
		if !rl.Allow(addr) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow(addr) {
		t.Error("11th request should be denied")
	}
	// Different IP is allowed.
	if !rl.Allow("5.6.7.8:9999") {
		t.Error("different IP should be allowed")
	}
}

// TestSecureCompare verifies the constant-time compare helper.
func TestSecureCompare(t *testing.T) {
	if !secureCompare("abc", "abc") {
		t.Error("equal strings should compare equal")
	}
	if secureCompare("abc", "abd") {
		t.Error("different strings should compare unequal")
	}
	if secureCompare("abc", "abcd") {
		t.Error("different-length strings should compare unequal")
	}
}

// TestServer_Healthz verifies the healthz payload includes version + auth state.
func TestServer_Healthz(t *testing.T) {
	s := newTestServer()
	s.SetAPIToken("xyz")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"auth_required":true`) {
		t.Errorf("expected auth_required=true, got: %s", w.Body.String())
	}
}

// TestServer_Readyz verifies readyz reflects the ready checker.
func TestServer_Readyz(t *testing.T) {
	s := New(":0", &noopStore{},
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return false })
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ready":false`) {
		t.Errorf("expected ready=false, got: %s", w.Body.String())
	}
}

// TestServer_InvestigateTrigger verifies POST /admin/investigate wires to
// the investigate callback.
func TestServer_InvestigateTrigger(t *testing.T) {
	var (
		mu        sync.Mutex
		gotSvc    string
		gotNs     string
		gotReason string
	)
	s := New(":0", &noopStore{},
		func() map[string]any { return nil },
		func(svc, ns, reason string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			gotSvc, gotNs, gotReason = svc, ns, reason
			return "cluster-xyz", nil
		},
		func() bool { return true })

	body := `{"service":"payments","namespace":"prod","reason":"manual"}`
	req := httptest.NewRequest("POST", "/admin/investigate", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleInvestigate(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotSvc != "payments" || gotNs != "prod" || gotReason != "manual" {
		t.Errorf("investigate callback got svc=%q ns=%q reason=%q", gotSvc, gotNs, gotReason)
	}
	if !strings.Contains(w.Body.String(), "cluster-xyz") {
		t.Errorf("expected cluster_id in response, got: %s", w.Body.String())
	}
}

// TestServer_InvestigateDefaults verifies missing namespace/reason fall back
// to "default" / "manual trigger".
func TestServer_InvestigateDefaults(t *testing.T) {
	var (
		mu    sync.Mutex
		gotNs string
		gotR  string
	)
	s := New(":0", &noopStore{},
		func() map[string]any { return nil },
		func(svc, ns, reason string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			gotNs, gotR = ns, reason
			return "c", nil
		},
		func() bool { return true })

	body := `{"service":"svc"}`
	req := httptest.NewRequest("POST", "/admin/investigate", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleInvestigate(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotNs != "default" {
		t.Errorf("default namespace = %q, want 'default'", gotNs)
	}
	if gotR != "manual trigger" {
		t.Errorf("default reason = %q, want 'manual trigger'", gotR)
	}
}

// TestServer_InvestigateInvalidJSON verifies malformed JSON returns 400.
func TestServer_InvestigateInvalidJSON(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("POST", "/admin/investigate", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	s.handleInvestigate(w, req)
	if w.Code != 400 {
		t.Errorf("got %d, want 400", w.Code)
	}
}

// TestServer_SettingsPOST verifies POST /settings wires to the RuntimeCfgSaver.
func TestServer_SettingsPOST(t *testing.T) {
	s := newTestServer()
	saver := &recordingCfgSaver{}
	s.SetRuntimeCfgSaver(saver)

	body := `{"llm_provider":"openrouter"}`
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if string(saver.lastJSON) != body {
		t.Errorf("saver got %q, want %q", saver.lastJSON, body)
	}
}

// recordingCfgSaver records the last JSON payload it received.
type recordingCfgSaver struct {
	lastJSON []byte
	err      error
}

func (r *recordingCfgSaver) SaveFromJSON(b []byte) error {
	r.lastJSON = b
	return r.err
}

// TestServer_SettingsPOSTNoSaver verifies POST /settings returns 501 when
// no saver is wired.
func TestServer_SettingsPOSTNoSaver(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleSettings(w, req)
	if w.Code != 501 {
		t.Errorf("got %d, want 501", w.Code)
	}
}

// TestServer_AutoMergeDefault verifies /automerge returns a disabled payload
// when no auto-merge status getter is wired.
func TestServer_AutoMergeDefault(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/automerge", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Errorf("expected 'not configured' message, got: %s", w.Body.String())
	}
}

// TestServer_WebhookNoHandler verifies /webhook/github returns 503 when no
// handler is wired.
func TestServer_WebhookNoHandler(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if w.Code != 503 {
		t.Errorf("got %d, want 503", w.Code)
	}
}

// TestServer_WebhookDualDispatch verifies /webhook/github dispatches to both
// the prreview and chatops handlers when both are wired.
func TestServer_WebhookDualDispatch(t *testing.T) {
	s := newTestServer()
	var prCount, chatopsCount atomic.Int32
	s.SetWebhookHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prCount.Add(1)
		writeJSON(w, 200, map[string]string{"ok": "prreview"})
	}))
	s.SetChatopsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatopsCount.Add(1)
	}))

	// pull_request event → only prreview.
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if prCount.Load() != 1 {
		t.Errorf("prreview: got %d, want 1", prCount.Load())
	}
	if chatopsCount.Load() != 0 {
		t.Errorf("chatops: got %d, want 0 for pull_request event", chatopsCount.Load())
	}

	// issue_comment event → both.
	prCount.Store(0)
	req = httptest.NewRequest("POST", "/webhook/github", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	w = httptest.NewRecorder()
	s.handleWebhook(w, req)
	if prCount.Load() != 1 {
		t.Errorf("prreview: got %d, want 1 for issue_comment", prCount.Load())
	}
	if chatopsCount.Load() != 1 {
		t.Errorf("chatops: got %d, want 1 for issue_comment", chatopsCount.Load())
	}
}

// TestServer_GitHubAppNotConfigured verifies /settings/github-app returns
// 503 when no manager is wired.
func TestServer_GitHubAppNotConfigured(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/settings/github-app", nil)
	w := httptest.NewRecorder()
	s.handleGitHubApp(w, req)
	if w.Code != 503 {
		t.Errorf("got %d, want 503", w.Code)
	}
}

// TestServer_GitHubAppGET verifies GET /settings/github-app returns the
// manager's status.
func TestServer_GitHubAppGET(t *testing.T) {
	s := newTestServer()
	s.SetGitHubAppManager(&fakeGitHubAppManager{
		status: schemas.GitHubAppStatus{Configured: true, AppID: "123", Slug: "sentinel", InstallID: "456"},
	})
	req := httptest.NewRequest("GET", "/settings/github-app", nil)
	w := httptest.NewRecorder()
	s.handleGitHubApp(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"app_id":"123"`) {
		t.Errorf("expected app_id 123 in response, got: %s", w.Body.String())
	}
}

// fakeGitHubAppManager is a GitHubAppManager stub for tests.
type fakeGitHubAppManager struct {
	status      schemas.GitHubAppStatus
	setErr      error
	removeErr   error
	installErr  error
	lastAppID   string
	lastInstall string
}

func (f *fakeGitHubAppManager) SetGitHubApp(appID, privateKey, installID, webhookSecret string) (string, error) {
	f.lastAppID = appID
	return "sentinel", f.setErr
}
func (f *fakeGitHubAppManager) SetGitHubAppInstall(installID string) error {
	f.lastInstall = installID
	return f.installErr
}
func (f *fakeGitHubAppManager) GetGitHubAppStatus() schemas.GitHubAppStatus { return f.status }
func (f *fakeGitHubAppManager) RemoveGitHubApp() error                      { return f.removeErr }

// TestServer_GitHubAppPOST verifies POST /settings/github-app validates
// required fields.
func TestServer_GitHubAppPOST(t *testing.T) {
	s := newTestServer()
	s.SetGitHubAppManager(&fakeGitHubAppManager{})

	// Missing private_key → 400.
	body := `{"app_id":"123"}`
	req := httptest.NewRequest("POST", "/settings/github-app", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleGitHubApp(w, req)
	if w.Code != 400 {
		t.Errorf("missing private_key: got %d, want 400", w.Code)
	}

	// Invalid JSON → 400.
	req = httptest.NewRequest("POST", "/settings/github-app", strings.NewReader("not json"))
	w = httptest.NewRecorder()
	s.handleGitHubApp(w, req)
	if w.Code != 400 {
		t.Errorf("invalid JSON: got %d, want 400", w.Code)
	}
}

// TestServer_GitHubAppDELETE verifies DELETE /settings/github-app calls RemoveGitHubApp.
func TestServer_GitHubAppDELETE(t *testing.T) {
	mgr := &fakeGitHubAppManager{}
	s := newTestServer()
	s.SetGitHubAppManager(mgr)
	req := httptest.NewRequest("DELETE", "/settings/github-app", nil)
	w := httptest.NewRecorder()
	s.handleGitHubApp(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "removed") {
		t.Errorf("expected 'removed' status, got: %s", w.Body.String())
	}
}

// TestServer_GitHubAppInstall verifies POST /settings/github-app-install.
func TestServer_GitHubAppInstall(t *testing.T) {
	mgr := &fakeGitHubAppManager{}
	s := newTestServer()
	s.SetGitHubAppManager(mgr)

	// Missing install_id → 400.
	req := httptest.NewRequest("POST", "/settings/github-app-install", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.handleGitHubAppInstall(w, req)
	if w.Code != 400 {
		t.Errorf("missing install_id: got %d, want 400", w.Code)
	}

	// Valid → 200.
	req = httptest.NewRequest("POST", "/settings/github-app-install", strings.NewReader(`{"install_id":"789"}`))
	w = httptest.NewRecorder()
	s.handleGitHubAppInstall(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if mgr.lastInstall != "789" {
		t.Errorf("manager received install_id=%q, want 789", mgr.lastInstall)
	}
}

// TestServer_Pprof verifies the pprof dispatcher routes to the right
// function based on URL path. We can't easily test the actual profile
// output, but we can verify the dispatcher doesn't panic.
func TestServer_Pprof(t *testing.T) {
	s := newTestServer()
	cases := []string{
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/symbol",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("handlePprof panicked for %s: %v", path, r)
				}
			}()
			s.handlePprof(w, req)
		})
	}
}

// TestServer_WebUIFallback verifies the placeholder HTML is served when no
// UI handler is wired.
func TestServer_WebUIFallback(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.handleWebUI(w, req)
	if w.Code != 200 {
		t.Errorf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sentinel") {
		t.Errorf("expected placeholder to mention Sentinel, got: %s", w.Body.String())
	}
}

// TestServer_IncidentByUUID verifies GET /incidents/{uuid} resolves via
// the Store.
func TestServer_IncidentByUUID(t *testing.T) {
	id := uuid.New()
	store := &stubStore{
		get: &schemas.Incident{IncidentID: id, Status: schemas.StatusVerified},
	}
	s := New(":0", store,
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return true })

	req := httptest.NewRequest("GET", "/incidents/"+id.String(), nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), id.String()) {
		t.Errorf("expected incident_id %s in response, got: %s", id, w.Body.String())
	}
}

// TestServer_IncidentByPrefix verifies GET /incidents/{prefix} resolves
// via FindIncidentByPrefix.
func TestServer_IncidentByPrefix(t *testing.T) {
	id := uuid.New()
	store := &stubStore{
		find: &schemas.Incident{IncidentID: id, Status: schemas.StatusVerified},
	}
	s := New(":0", store,
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return true })

	prefix := id.String()[:8]
	req := httptest.NewRequest("GET", "/incidents/"+prefix, nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
}

// TestServer_IncidentNotFound verifies GET /incidents/{id} returns 404 when
// the Store has no match.
func TestServer_IncidentNotFound(t *testing.T) {
	store := &stubStore{} // both get + find return nil
	s := New(":0", store,
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return true })

	req := httptest.NewRequest("GET", "/incidents/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("got %d, want 404", w.Code)
	}
}

// TestServer_MetricsSummary verifies /metrics-summary buckets incidents by
// lifecycle state.
func TestServer_MetricsSummary(t *testing.T) {
	store := &stubStore{
		list: []*schemas.Incident{
			{Status: schemas.StatusInvestigating},
			{Status: schemas.StatusVerified},
			{Status: schemas.StatusFailed},
			{Status: schemas.StatusEscalated},
		},
	}
	s := New(":0", store,
		func() map[string]any { return nil },
		func(s, ns, r string) (string, error) { return "", nil },
		func() bool { return true })

	req := httptest.NewRequest("GET", "/metrics-summary", nil)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"active":1`) {
		t.Errorf("expected 1 active, got: %s", body)
	}
	if !strings.Contains(body, `"resolved_24h":1`) {
		t.Errorf("expected 1 resolved, got: %s", body)
	}
	if !strings.Contains(body, `"failed":2`) {
		t.Errorf("expected 2 failed (failed+escalated), got: %s", body)
	}
}

// TestEnrichIncidentPRFields verifies the PR-field mirroring from
// state.execution_result to the top-level Incident fields.
func TestEnrichIncidentPRFields(t *testing.T) {
	prNum := 42
	prURL := "https://github.com/acme/repo/pull/42"
	prBranch := "fix-42"
	sandboxSummary := "verified"
	inc := &schemas.Incident{
		State: schemas.AgentState{
			ExecutionResult: &schemas.ExecutionResult{
				PRNumber:        &prNum,
				PRURL:           &prURL,
				PRBranch:        &prBranch,
				SandboxSummary:  &sandboxSummary,
				SandboxVerified: true,
			},
			FixProposal: &schemas.FixProposal{FixType: schemas.FixCodeFix},
		},
	}
	enrichIncidentPRFields(inc)
	if inc.PRNumber == nil || *inc.PRNumber != 42 {
		t.Errorf("PRNumber = %v, want 42", inc.PRNumber)
	}
	if inc.PRURL != prURL {
		t.Errorf("PRURL = %q, want %q", inc.PRURL, prURL)
	}
	if inc.PRBranch != prBranch {
		t.Errorf("PRBranch = %q, want %q", inc.PRBranch, prBranch)
	}
	if inc.SandboxSummary != sandboxSummary {
		t.Errorf("SandboxSummary = %q, want %q", inc.SandboxSummary, sandboxSummary)
	}
	if !inc.SandboxVerified {
		t.Error("SandboxVerified should be true")
	}
	if inc.FixType != string(schemas.FixCodeFix) {
		t.Errorf("FixType = %q, want %q", inc.FixType, schemas.FixCodeFix)
	}
}

// TestEnrichIncidentPRFieldsNil verifies the helper is a no-op on a nil incident.
func TestEnrichIncidentPRFieldsNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("enrichIncidentPRFields(nil) panicked: %v", r)
		}
	}()
	enrichIncidentPRFields(nil)
}

// fmt is used in some test names above; this keeps the import if the only
// reference is removed.
var _ = fmt.Sprintf

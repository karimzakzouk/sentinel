package alertmanager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHandler captures HandleAlert calls for assertions.
type fakeHandler struct {
	mu    sync.Mutex
	calls []alertCall
}

type alertCall struct {
	service     string
	namespace   string
	reason      string
	repo        string
	description string
	labels      map[string]string
}

func (f *fakeHandler) HandleAlert(service, namespace, reason, repo, description string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, alertCall{service, namespace, reason, repo, description, labels})
}

func (f *fakeHandler) snapshot() []alertCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]alertCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestReceiver_FiringAlert verifies that a firing alert is parsed and
// dispatched to the handler with the expected fields.
func TestReceiver_FiringAlert(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)

	payload := AlertmanagerPayload{
		Version: "4",
		Status:  "firing",
		Alerts: []Alert{
			{
				Status:      "firing",
				Labels:      map[string]string{"alertname": "HighErrorRate", "service": "payments-api", "namespace": "production"},
				Annotations: map[string]string{"summary": "Error rate > 5%", "description": "Too many 500s", "runbook_url": "https://runbooks.example.com/high-error-rate"},
				StartsAt:    "2026-06-29T12:00:00Z",
				Fingerprint: "abc123",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	calls := h.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 HandleAlert call, got %d", len(calls))
	}
	call := calls[0]
	if call.service != "payments-api" {
		t.Errorf("service: got %q, want payments-api", call.service)
	}
	if call.namespace != "production" {
		t.Errorf("namespace: got %q, want production", call.namespace)
	}
	if call.reason != "HighErrorRate: Error rate > 5%" {
		t.Errorf("reason: got %q", call.reason)
	}
	if !strings.Contains(call.description, "Too many 500s") {
		t.Errorf("description should contain the alert description: %q", call.description)
	}
}

// TestReceiver_ResolvedAlertSkipped verifies that resolved alerts do not
// trigger the handler (only firing alerts do).
func TestReceiver_ResolvedAlertSkipped(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)

	payload := AlertmanagerPayload{
		Alerts: []Alert{
			{Status: "resolved", Labels: map[string]string{"alertname": "HighErrorRate", "service": "api"}, Fingerprint: "xyz"},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(h.snapshot()) != 0 {
		t.Fatalf("expected 0 HandleAlert calls for resolved alert, got %d", len(h.snapshot()))
	}
}

// TestReceiver_DedupByFingerprint verifies that the same firing alert sent
// three times only triggers the handler once (within the cooldown window).
func TestReceiver_DedupByFingerprint(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)

	alert := Alert{
		Status:      "firing",
		Labels:      map[string]string{"alertname": "HighErrorRate", "service": "api"},
		Fingerprint: "fp-123",
	}
	payload := AlertmanagerPayload{Alerts: []Alert{alert}}
	body, _ := json.Marshal(payload)

	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	if len(h.snapshot()) != 1 {
		t.Fatalf("expected 1 HandleAlert call (deduped), got %d", len(h.snapshot()))
	}
}

// TestReceiver_ServiceFallback verifies the service-name fallback chain
// (service → job → deployment → pod → unknown).
func TestReceiver_ServiceFallback(t *testing.T) {
	tests := []struct {
		labels  map[string]string
		wantSvc string
	}{
		{map[string]string{"service": "svc"}, "svc"},
		{map[string]string{"job": "myjob"}, "myjob"},
		{map[string]string{"deployment": "dep"}, "dep"},
		{map[string]string{"pod": "mypod"}, "mypod"},
		{map[string]string{}, "unknown"},
	}
	for _, tt := range tests {
		h := &fakeHandler{}
		r := NewReceiver(h)
		payload := AlertmanagerPayload{
			Alerts: []Alert{{Status: "firing", Labels: tt.labels, Fingerprint: "fp-" + tt.wantSvc}},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		calls := h.snapshot()
		if len(calls) != 1 {
			t.Fatalf("expected 1 call for %v, got %d", tt.labels, len(calls))
		}
		if calls[0].service != tt.wantSvc {
			t.Errorf("labels=%v: service got %q, want %q", tt.labels, calls[0].service, tt.wantSvc)
		}
	}
}

// TestReceiver_MethodNotAllowed verifies GET is rejected.
func TestReceiver_MethodNotAllowed(t *testing.T) {
	r := NewReceiver(&fakeHandler{})
	req := httptest.NewRequest(http.MethodGet, "/webhook/alertmanager", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// TestReceiver_InvalidJSON verifies malformed JSON returns 400.
func TestReceiver_InvalidJSON(t *testing.T) {
	r := NewReceiver(&fakeHandler{})
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestReceiver_SecretVerification verifies the X-Sentinel-Webhook-Secret
// gate: a missing, wrong, or correct secret produces 401/401/200
// respectively.
func TestReceiver_SecretVerification(t *testing.T) {
	const secret = "s3cr3t"
	cases := []struct {
		name       string
		headerName string
		headerVal  string
		wantCode   int
	}{
		{"correct secret", "X-Sentinel-Webhook-Secret", secret, http.StatusOK},
		{"missing secret", "", "", http.StatusUnauthorized},
		{"wrong secret", "X-Sentinel-Webhook-Secret", "wrong", http.StatusUnauthorized},
		{"no secret configured allows anything", "__none__", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeHandler{}
			r := NewReceiver(h)
			if tc.headerName != "__none__" {
				r.WithSharedSecret(secret)
			}
			payload := AlertmanagerPayload{
				Alerts: []Alert{{Status: "firing", Labels: map[string]string{"alertname": "A", "service": "svc"}, Fingerprint: "fp-" + tc.name}},
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
			if tc.headerName != "" && tc.headerName != "__none__" {
				req.Header.Set(tc.headerName, tc.headerVal)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Errorf("got %d, want %d (body: %s)", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

// TestReceiver_NormalizesAlerts verifies the end-to-end normalization from
// an Alertmanager payload into a Signal-shaped HandleAlert call.
func TestReceiver_NormalizesAlerts(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)

	payload := AlertmanagerPayload{
		Version:      "4",
		GroupKey:     "group-1",
		Status:       "firing",
		CommonLabels: map[string]string{"severity": "critical"},
		Alerts: []Alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "PodCrashLooping",
					"service":   "checkout",
					"namespace": "shop",
					"repo":      "acme/checkout",
				},
				Annotations: map[string]string{
					"summary":     "Pod is crash-looping",
					"description": "container exited with code 137",
					"runbook_url": "https://runbooks/crash",
				},
				StartsAt:    "2026-06-29T12:00:00Z",
				Fingerprint: "fp-normalize",
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	calls := h.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.service != "checkout" {
		t.Errorf("service = %q, want checkout", c.service)
	}
	if c.namespace != "shop" {
		t.Errorf("namespace = %q, want shop", c.namespace)
	}
	if c.repo != "acme/checkout" {
		t.Errorf("repo = %q, want acme/checkout", c.repo)
	}
	if !strings.Contains(c.reason, "PodCrashLooping") || !strings.Contains(c.reason, "crash-looping") {
		t.Errorf("reason = %q, want alertname + summary", c.reason)
	}
	// Description should include description, runbook, and labels sections.
	for _, want := range []string{"container exited with code 137", "Runbook:", "Labels:"} {
		if !strings.Contains(c.description, want) {
			t.Errorf("description missing %q\nfull description:\n%s", want, c.description)
		}
	}
	// Labels should be passed through unchanged.
	if c.labels["alertname"] != "PodCrashLooping" {
		t.Errorf("labels not passed through: %v", c.labels)
	}
}

// TestReceiver_CooldownExpiry verifies that after the cooldown window a
// duplicate fingerprint is allowed through again.
func TestReceiver_CooldownExpiry(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)
	r.SetCooldown(50 * time.Millisecond) // short for the test

	alert := Alert{
		Status:      "firing",
		Labels:      map[string]string{"alertname": "A", "service": "svc"},
		Fingerprint: "fp-expiry",
	}
	payload := AlertmanagerPayload{Alerts: []Alert{alert}}
	body, _ := json.Marshal(payload)

	// First send: 1 call.
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(h.snapshot()) != 1 {
		t.Fatalf("expected 1 call after first send, got %d", len(h.snapshot()))
	}

	// Immediate second send: deduped.
	req = httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(h.snapshot()) != 1 {
		t.Fatalf("expected 1 call after deduped second send, got %d", len(h.snapshot()))
	}

	// After cooldown: allowed through.
	time.Sleep(60 * time.Millisecond)
	req = httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(h.snapshot()) != 2 {
		t.Fatalf("expected 2 calls after cooldown expiry, got %d", len(h.snapshot()))
	}
}

// TestReceiver_PurgeOldEntries verifies the manual purge entrypoint removes
// stale fingerprints.
func TestReceiver_PurgeOldEntries(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)
	r.SetCooldown(50 * time.Millisecond)

	alert := Alert{
		Status:      "firing",
		Labels:      map[string]string{"alertname": "A", "service": "svc"},
		Fingerprint: "fp-purge",
	}
	payload := AlertmanagerPayload{Alerts: []Alert{alert}}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(h.snapshot()) != 1 {
		t.Fatalf("expected 1 call after first send, got %d", len(h.snapshot()))
	}

	time.Sleep(60 * time.Millisecond)
	r.PurgeOldEntries()

	// Same fingerprint should now be allowed through.
	req = httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(h.snapshot()) != 2 {
		t.Fatalf("expected 2 calls after purge, got %d", len(h.snapshot()))
	}
}

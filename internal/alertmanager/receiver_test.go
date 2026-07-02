package alertmanager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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
	if len(h.calls) != 1 {
		t.Fatalf("expected 1 HandleAlert call, got %d", len(h.calls))
	}
	call := h.calls[0]
	if call.service != "payments-api" {
		t.Errorf("service: got %q, want payments-api", call.service)
	}
	if call.namespace != "production" {
		t.Errorf("namespace: got %q, want production", call.namespace)
	}
	if call.reason != "HighErrorRate: Error rate > 5%" {
		t.Errorf("reason: got %q", call.reason)
	}
	if call.description == "" || !contains(call.description, "Too many 500s") {
		t.Errorf("description should contain the alert description: %q", call.description)
	}
}

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

	if len(h.calls) != 0 {
		t.Fatalf("expected 0 HandleAlert calls for resolved alert, got %d", len(h.calls))
	}
}

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

	// Send the same alert 3 times
	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	if len(h.calls) != 1 {
		t.Fatalf("expected 1 HandleAlert call (deduped), got %d", len(h.calls))
	}
}

func TestReceiver_ServiceFallback(t *testing.T) {
	h := &fakeHandler{}
	r := NewReceiver(h)

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
		h.calls = nil
		payload := AlertmanagerPayload{
			Alerts: []Alert{{Status: "firing", Labels: tt.labels, Fingerprint: "fp-" + tt.wantSvc}},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if len(h.calls) != 1 {
			t.Fatalf("expected 1 call for %v, got %d", tt.labels, len(h.calls))
		}
		if h.calls[0].service != tt.wantSvc {
			t.Errorf("labels=%v: service got %q, want %q", tt.labels, h.calls[0].service, tt.wantSvc)
		}
	}
}

func TestReceiver_MethodNotAllowed(t *testing.T) {
	r := NewReceiver(&fakeHandler{})
	req := httptest.NewRequest(http.MethodGet, "/webhook/alertmanager", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestReceiver_InvalidJSON(t *testing.T) {
	r := NewReceiver(&fakeHandler{})
	req := httptest.NewRequest(http.MethodPost, "/webhook/alertmanager", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

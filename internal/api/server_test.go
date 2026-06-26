package api

import (
        "context"
        "net/http"
        "net/http/httptest"
        "strings"
        "testing"

        "github.com/google/uuid"
)

// TestHandleSSEMaxSubscribers verifies the SSE subscriber cap. v3.13.0: audit #10.
// Opening more than maxSSESubscribers concurrent connections should return 503.
func TestHandleSSEMaxSubscribers(t *testing.T) {
        s := New(":0", &noopStoreAdapter{}, func() map[string]any { return nil }, func(s, ns, r string) (string, error) { return "", nil }, func() bool { return true })

        // We can't easily test 100 concurrent connections in a unit test, but we
        // can verify the cap logic by directly filling eventSubs.
        s.eventSubsMu.Lock()
        for i := 0; i < maxSSESubscribers; i++ {
                s.eventSubs[make(chan ServerEvent, 16)] = true
        }
        s.eventSubsMu.Unlock()

        // Now the next SSE request should get 503
        req := httptest.NewRequest("GET", "/events", nil)
        w := httptest.NewRecorder()
        s.handleSSE(w, req)

        if w.Code != http.StatusServiceUnavailable {
                t.Errorf("expected 503 when subscribers at cap, got %d", w.Code)
        }
}

// TestReadBodyMaxBytes verifies the body size limit. v3.13.0: audit #3.
// Bodies larger than maxRequestBodyBytes should be truncated/rejected.
func TestReadBodyMaxBytes(t *testing.T) {
        // Create a body larger than 1MB
        largeBody := strings.Repeat("x", maxRequestBodyBytes+100)
        req := httptest.NewRequest("POST", "/settings", strings.NewReader(largeBody))

        _, err := readBody(req)
        if err == nil {
                t.Error("expected error when reading body > 1MB, got nil")
        }
}

// TestHandleSettingsRejectsDelete verifies that non-GET/POST methods are rejected.
func TestHandleSettingsRejectsDelete(t *testing.T) {
        s := New(":0", &noopStoreAdapter{}, func() map[string]any { return nil }, func(s, ns, r string) (string, error) { return "", nil }, func() bool { return true })

        req := httptest.NewRequest("DELETE", "/settings", nil)
        w := httptest.NewRecorder()
        s.handleSettings(w, req)

        if w.Code != http.StatusMethodNotAllowed {
                t.Errorf("expected 405 for DELETE /settings, got %d", w.Code)
        }
}

// TestHandleInvestigateMissingService verifies that POST /admin/investigate
// without a service field returns 400.
func TestHandleInvestigateMissingService(t *testing.T) {
        s := New(":0", &noopStoreAdapter{}, func() map[string]any { return nil }, func(s, ns, r string) (string, error) { return "", nil }, func() bool { return true })

        body := `{"namespace":"sim"}`
        req := httptest.NewRequest("POST", "/admin/investigate", strings.NewReader(body))
        w := httptest.NewRecorder()
        s.handleInvestigate(w, req)

        if w.Code != http.StatusBadRequest {
                t.Errorf("expected 400 for missing service, got %d", w.Code)
        }
}

// noopStoreAdapter is a minimal Store for testing.
type noopStoreAdapter struct{}

func (n *noopStoreAdapter) ListRecentIncidents(ctx context.Context, limit int) ([]any, error) {
        return nil, nil
}
func (n *noopStoreAdapter) GetIncident(ctx context.Context, id uuid.UUID) (any, error) {
        return nil, nil
}
func (n *noopStoreAdapter) FindIncidentByPrefix(ctx context.Context, prefix string) (any, error) {
        return nil, nil
}

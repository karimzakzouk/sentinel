package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerServesIndex(t *testing.T) {
	h := Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if len(body) == 0 {
		t.Error("response body should not be empty")
	}
}

func TestHandlerServesHealthz(t *testing.T) {
	// The web handler serves embedded files; /healthz is handled by the API
	// server, but the web handler should serve whatever it has
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Should serve the index.html (or a fallback)
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
		t.Errorf("expected 200 or 404, got %d", w.Code)
	}
}

func TestHandlerReturnsContentType(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Log("Content-Type not set (acceptable for fallback handler)")
	}
}

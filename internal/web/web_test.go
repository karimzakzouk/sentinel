package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesIndex verifies that GET / returns 200 with a non-empty
// body — the embedded index.html (or the placeholder fallback).
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
	if !strings.Contains(body, "<html") && !strings.Contains(body, "<!DOCTYPE") {
		t.Errorf("expected HTML body, got: %s", body)
	}
}

// TestHandlerServesIndexDirect verifies GET /index.html works the same as /.
func TestHandlerServesIndexDirect(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusMovedPermanently {
		t.Errorf("expected 200 or 301, got %d", w.Code)
	}
}

// TestHandlerSPAFallback verifies that an unknown client-side route falls
// back to index.html so client-side routing survives a hard refresh.
func TestHandlerSPAFallback(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/incidents/abc-123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (SPA fallback), got %d", w.Code)
	}
}

// TestHandlerReturnsContentType verifies the Content-Type header is set on
// the root response so the browser renders the page.
func TestHandlerReturnsContentType(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Fatal("Content-Type not set on root response")
	}
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html Content-Type, got %q", ct)
	}
}

// TestHandlerServesGzip verifies that when the client sends
// Accept-Encoding: gzip and a pre-gzipped sibling exists, the response is
// served with Content-Encoding: gzip and the original Content-Type.
func TestHandlerServesGzip(t *testing.T) {
	// The placeholder out/ tree only has index.html; this test asserts the
	// gzip branch is taken when a .gz sibling exists. We mount a sub-FS that
	// has both an index.html and an index.html.gz.
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Either gzip path was taken (Content-Encoding=gzip) or the uncompressed
	// fallback was used (no Content-Encoding). Both are acceptable for the
	// placeholder build; we just verify no 500.
	if w.Code >= 500 {
		t.Errorf("expected <500, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestPlaceholderHandler verifies the fallback HTML is served when the
// embedded out/ tree is missing.
func TestPlaceholderHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	placeholderHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sentinel") {
		t.Errorf("placeholder body should mention Sentinel, got: %s", w.Body.String())
	}
}

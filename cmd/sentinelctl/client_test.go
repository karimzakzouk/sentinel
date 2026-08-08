package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient wraps a Client around a httptest.Server. The server's URL
// becomes the Client's baseURL; the timeout is short so a hung handler
// surfaces quickly.
func newTestClient(t *testing.T, srv *httptest.Server, token string, jsonOut bool) *Client {
	t.Helper()
	return NewClient(srv.URL, token, jsonOut, 5*time.Second)
}

// TestClient_Get verifies a GET returns the response body on 200.
func TestClient_Get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/incidents" {
			t.Errorf("path = %q, want /incidents", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	body, err := c.Get(context.Background(), "/incidents")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want {\"ok\":true}", string(body))
	}
}

// TestClient_Post verifies PostJSON sends a POST with the marshaled body
// and returns the response.
func TestClient_Post(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"service":"demo"`) {
			t.Errorf("body = %q, want service=demo", string(b))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"cluster_id":"abc123"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	body, err := c.PostJSON(context.Background(), "/admin/investigate",
		map[string]string{"service": "demo", "namespace": "default"}, true)
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if !strings.Contains(string(body), "abc123") {
		t.Errorf("body = %q, want cluster_id=abc123", string(body))
	}
}

// TestClient_BearerAuth verifies the bearer token is attached when
// authRequired is true and a token is configured.
func TestClient_BearerAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/admin/investigate" {
			if auth != "Bearer secret-token" {
				t.Errorf("admin auth header = %q, want Bearer secret-token", auth)
			}
		} else {
			if auth != "" {
				t.Errorf("read path %q got auth header %q, want empty", r.URL.Path, auth)
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "secret-token", false)
	if _, err := c.Get(context.Background(), "/incidents"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.PostJSON(context.Background(), "/admin/investigate", map[string]string{}, true); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
}

// TestClient_NotFound verifies a 404 surfaces as an error mentioning the
// status code.
func TestClient_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"incident not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	_, err := c.Get(context.Background(), "/incidents/missing")
	if err == nil {
		t.Fatal("Get: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %q, want 404", err.Error())
	}
}

// TestClient_ServerError verifies a 500 surfaces as an error.
func TestClient_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	_, err := c.Get(context.Background(), "/incidents")
	if err == nil {
		t.Fatal("Get: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %q, want 500", err.Error())
	}
}

// TestClient_UnauthorizedNoToken verifies the 401 path produces a hint about
// the missing token rather than the generic "incorrect token" message.
func TestClient_UnauthorizedNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	_, err := c.PostJSON(context.Background(), "/admin/investigate", nil, true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "SENTINEL_API_TOKEN") {
		t.Errorf("err = %q, want hint about SENTINEL_API_TOKEN", err.Error())
	}
}

// TestClient_ContextCancel verifies a canceled context surfaces as an error
// (the underlying http.Client returns context.Canceled).
func TestClient_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "/incidents")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "connection failed") {
		t.Errorf("err = %q, want context deadline or connection failed", err.Error())
	}
}

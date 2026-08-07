// Package main — HTTP client wrapper for sentinelctl.
//
// The Client talks to Sentinel's HTTP API (internal/api). Read endpoints
// (GET /incidents, GET /metrics, etc.) need no auth; admin endpoints
// (POST /admin/*, POST /settings, POST /settings/github-app*) require a
// bearer token when the server has SENTINEL_API_TOKEN set.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the HTTP client for Sentinel's API.
type Client struct {
	baseURL string
	token   string
	jsonOut bool
	http    *http.Client
}

// NewClient returns a Client bound to baseURL with the given bearer token
// and JSON-output flag. The HTTP timeout caps every request so a hung
// Sentinel pod cannot hang the CLI forever.
func NewClient(baseURL, token string, jsonOut bool, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		jsonOut: jsonOut,
		http:    &http.Client{Timeout: timeout},
	}
}

// Do sends an HTTP request and returns the response body. When authRequired
// is true and a token is configured, the request carries an
// "Authorization: Bearer <token>" header. Errors are wrapped with the
// baseURL so a connection failure tells the user which server was unreachable.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, authRequired bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authRequired && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w (is Sentinel running at %s?)", err, c.baseURL)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		if c.token == "" {
			return nil, fmt.Errorf("API returned 401 Unauthorized — the server has SENTINEL_API_TOKEN set; pass --token or set SENTINEL_API_TOKEN")
		}
		return nil, fmt.Errorf("API returned 401 Unauthorized — token is incorrect or expired")
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("API returned 403 Forbidden — token lacks required scope")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// Get issues an unauthenticated GET.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, path, nil, false)
}

// PostJSON issues a POST with a JSON-marshaled payload. When authRequired
// is true the bearer token is attached (used for /admin/* and POST /settings).
// A nil payload sends an empty body.
func (c *Client) PostJSON(ctx context.Context, path string, payload any, authRequired bool) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		switch v := payload.(type) {
		case json.RawMessage:
			body = bytes.NewReader(v)
		case []byte:
			body = bytes.NewReader(v)
		default:
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("marshal payload: %w", err)
			}
			body = bytes.NewReader(b)
		}
	}
	return c.Do(ctx, http.MethodPost, path, body, authRequired)
}

// DoRaw builds a request with a custom method and pre-encoded body, for
// endpoints that need exact control over the payload (e.g. raw JSON sent
// to /settings without re-marshaling).
func (c *Client) DoRaw(ctx context.Context, method, path string, body []byte, authRequired bool) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authRequired && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

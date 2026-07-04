// Package main — HTTP client for sentinelctl.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)


type client struct {
        baseURL string
        token   string
        jsonOut bool
        http    *http.Client
}

// do sends an HTTP request and returns the response body. The authRequired
// flag controls whether the bearer token is attached — admin endpoints
// (/admin/*, POST /settings) need it; read endpoints don't.
// or SENTINEL_API_TOKEN.
// 401" message with no hint about the fix.

func (c *client) do(method, path string, body io.Reader, authRequired bool) ([]byte, error) {
        req, err := http.NewRequest(method, c.baseURL+path, body)
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
                return nil, fmt.Errorf("connection failed: %w\nIs Sentinel running at %s?", err, c.baseURL)
        }
        defer resp.Body.Close()
        respBody, _ := io.ReadAll(resp.Body)

        if resp.StatusCode == 401 {
                if c.token == "" {
                        return nil, fmt.Errorf("API returned 401 Unauthorized.\n\n" +
                                "The Sentinel server has SENTINEL_API_TOKEN set. Provide the token via:\n" +
                                "  --token <token>           (flag)\n" +
                                "  SENTINEL_API_TOKEN=<tok>  (env var)")
                }
                return nil, fmt.Errorf("API returned 401 Unauthorized — token is incorrect or expired")
        }
        if resp.StatusCode == 403 {
                return nil, fmt.Errorf("API returned 403 Forbidden — token lacks required scope")
        }
        if resp.StatusCode >= 400 {
                return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
        }
        return respBody, nil
}

func (c *client) get(path string) ([]byte, error) {
        return c.do("GET", path, nil, false)
}

// postAuth sends a POST with an optional bearer token. authRequired=true
// attaches the token (used for /admin/* and POST /settings); false sends
// without (currently no POST endpoints are unauthenticated, but kept for
// future use).replaced the unused post() wrapper with this single
// method — golangci-lint flagged the unused post() function.

func (c *client) postAuth(path string, payload any, authRequired bool) ([]byte, error) {
        var body io.Reader
        if payload != nil {
                b, _ := json.Marshal(payload)
                body = bytes.NewReader(b)
        }
        return c.do("POST", path, body, authRequired)
}

// --- Commands ---


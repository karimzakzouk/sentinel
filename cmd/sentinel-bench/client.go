// Package main — Sentinel API client for sentinel-bench.
//
// This is a deliberately thin client — it talks to Sentinel's HTTP API
// only, does not import internal/* packages (except internal/logging via
// the runner, not here). It exists so the runner can be built and shipped
// as a standalone binary.
//
// Endpoints used:
//   - POST /admin/investigate  — trigger an investigation
//   - GET  /incidents          — list recent incidents
//   - GET  /incidents/{id}     — get one incident
//
// The Incident struct is a strict subset of internal/schemas.Incident —
// the runner only needs the fields that drive the assertions.
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
	http    *http.Client
}

// NewClient returns a Client bound to baseURL. The bearer token is
// attached on /admin/* requests; read endpoints (GET /incidents, etc.)
// are unauthenticated. The HTTP timeout caps every request so a hung
// Sentinel pod cannot hang the runner.
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

// do sends an HTTP request and returns the response body. When
// authRequired is true and a token is configured, the request carries an
// "Authorization: Bearer <token>" header. Errors are wrapped with the
// baseURL so a connection failure tells the user which server was
// unreachable.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, authRequired bool) ([]byte, error) {
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
			return nil, fmt.Errorf("API returned 401 — the server has SENTINEL_API_TOKEN set; pass --token or set SENTINEL_API_TOKEN")
		}
		return nil, fmt.Errorf("API returned 401 — token is incorrect or expired")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// InvestigateRequest is the body for POST /admin/investigate.
type InvestigateRequest struct {
	Service   string `json:"service"`
	Namespace string `json:"namespace"`
	Reason    string `json:"reason"`
}

// InvestigateResponse is the body returned by POST /admin/investigate.
type InvestigateResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	ClusterID string `json:"cluster_id"`
}

// Trigger calls POST /admin/investigate. Returns the cluster_id Sentinel
// assigned to the new incident (use it as the incident ID prefix for the
// subsequent GET /incidents/{id} poll).
func (c *Client) Trigger(ctx context.Context, service, namespace, reason string) (*InvestigateResponse, error) {
	body, err := json.Marshal(InvestigateRequest{
		Service:   service,
		Namespace: namespace,
		Reason:    reason,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal trigger body: %w", err)
	}
	respBody, err := c.do(ctx, http.MethodPost, "/admin/investigate", bytes.NewReader(body), true)
	if err != nil {
		return nil, err
	}
	var out InvestigateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse trigger response: %w", err)
	}
	return &out, nil
}

// Incident is a strict subset of internal/schemas.Incident — the runner
// only needs the fields that drive the four assertions.
type Incident struct {
	IncidentID      string `json:"incident_id"`
	Status          string `json:"status"`
	PRRepo          string `json:"pr_repo,omitempty"`
	PRNumber        *int   `json:"pr_number,omitempty"`
	PRBranch        string `json:"pr_branch,omitempty"`
	PRURL           string `json:"pr_url,omitempty"`
	AutoMergeStatus string `json:"auto_merge_status,omitempty"`
	SandboxVerified bool   `json:"sandbox_verified,omitempty"`
	SandboxSummary  string `json:"sandbox_summary,omitempty"`
	FixType         string `json:"fix_type,omitempty"`
}

// ListIncidents calls GET /incidents?limit=N. Returns the most recent
// incidents (newest first) for the runner to find the one it just
// triggered.
func (c *Client) ListIncidents(ctx context.Context, limit int) ([]Incident, error) {
	if limit <= 0 {
		limit = 50
	}
	path := fmt.Sprintf("/incidents?limit=%d", limit)
	body, err := c.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, err
	}
	var out []Incident
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse incidents list: %w", err)
	}
	return out, nil
}

// GetIncident calls GET /incidents/{id}. The id may be a full UUID or a
// short prefix (>=4 chars); Sentinel resolves both.
func (c *Client) GetIncident(ctx context.Context, id string) (*Incident, error) {
	body, err := c.do(ctx, http.MethodGet, "/incidents/"+id, nil, false)
	if err != nil {
		return nil, err
	}
	var out Incident
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse incident: %w", err)
	}
	return &out, nil
}

// IsTerminal reports whether the incident status is a terminal state —
// the runner stops polling once it sees one of these.
func (i Incident) IsTerminal() bool {
	switch i.Status {
	case "verified", "resolved", "failed", "rejected", "escalated":
		return true
	}
	return false
}

// HasPR reports whether Sentinel opened a PR for this incident.
func (i Incident) HasPR() bool {
	return i.PRNumber != nil && *i.PRNumber > 0
}

// AutoMergeApproved reports whether Sentinel's own auto-merge policy
// approved the PR. Sentinel writes the field as "approved", "rejected",
// "pending", or "" (never reached the auto-merge gate).
func (i Incident) AutoMergeApproved() bool {
	return i.AutoMergeStatus == "approved"
}

// CIGreen reports whether the PR's CI passed. We use SandboxVerified as
// the proxy: Sentinel's sandbox runs lint + tests + a reproducer; if the
// sandbox verified the fix, CI on the actual PR is presumed green (the
// auto-merge policy checks the same gate before merging).
func (i Incident) CIGreen() bool {
	return i.SandboxVerified
}

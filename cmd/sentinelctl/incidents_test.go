package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// incidentJSON is the canonical Incident shape returned by the server. Built
// once per test using a closure that overrides fields as needed.
type incidentJSON func(override func(map[string]any))

// marshalIncident returns the JSON bytes for a single incident with the
// given field overrides applied to a default body.
func marshalIncident(t *testing.T, override func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"incident_id": "91cce5a5-1234-5678-9abc-def012345678",
		"status":      "investigating",
		"created_at":  "2025-01-02T15:04:05Z",
		"cluster": map[string]any{
			"cluster_id": "abc-123",
			"service":    "demo-app",
			"namespace":  "sim",
		},
		"state": map[string]any{
			"root_cause": map[string]any{
				"statement":  "bad deploy",
				"confidence": 0.87,
			},
		},
	}
	if override != nil {
		override(body)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestIncidents_ListFormatsAsTable verifies the list view renders a tabbed
// header + one row per incident, with the incident ID truncated to 8 chars
// and the timestamp rendered as "2006-01-02 15:04:05".
func TestIncidents_ListFormatsAsTable(t *testing.T) {
	b1 := marshalIncident(t, nil)
	b2 := marshalIncident(t, func(m map[string]any) {
		m["incident_id"] = "deadbeef-aaaa-bbbb-cccc-dddddddddddd"
		m["status"] = "resolved"
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/incidents" {
			t.Errorf("path = %q, want /incidents", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[" + string(b1) + "," + string(b2) + "]"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	var buf bytes.Buffer
	if err := c.ListIncidents(context.Background(), &buf, IncidentFilters{Limit: 50}); err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	out := buf.String()
	headerRe := regexp.MustCompile(`ID\s+SERVICE\s+NAMESPACE\s+STATUS\s+CONFIDENCE\s+CREATED`)
	if !headerRe.MatchString(out) {
		t.Errorf("output missing tab header\n--- output ---\n%s", out)
	}
	for _, want := range []string{"91cce5a5", "deadbeef", "demo-app", "sim", "investigating", "resolved", "2025-01-02 15:04:05", "87%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestIncidents_GetFormatsDetail verifies the detail view renders the
// incident ID, service, status, created time, root cause statement, and
// confidence.
func TestIncidents_GetFormatsDetail(t *testing.T) {
	body := marshalIncident(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/incidents/") {
			t.Errorf("path = %q, want /incidents/<id>", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	var buf bytes.Buffer
	if err := c.GetIncident(context.Background(), &buf, "91cce5a5"); err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Incident: 91cce5a5-1234-5678-9abc-def012345678", "Service:  demo-app", "Status:   investigating", "Created:  2025-01-02 15:04:05", "Root Cause:", "bad deploy", "Confidence: 87%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestIncidents_TriggerSendsPOST verifies Trigger POSTs to /admin/investigate
// with the service/namespace/reason and prints the returned cluster ID.
func TestIncidents_TriggerSendsPOST(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"triggered","service":"demo-app","cluster_id":"abc-123"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok", false)
	var buf bytes.Buffer
	if err := c.Trigger(context.Background(), &buf, "demo-app", "sim", "manual trigger via CLI"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if gotPath != "/admin/investigate" {
		t.Errorf("path = %q, want /admin/investigate", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody["service"] != "demo-app" || gotBody["namespace"] != "sim" || gotBody["reason"] != "manual trigger via CLI" {
		t.Errorf("body = %+v, want service=demo-app namespace=sim reason=manual...", gotBody)
	}
	out := buf.String()
	if !strings.Contains(out, "demo-app/sim") {
		t.Errorf("output = %q, want demo-app/sim", out)
	}
	if !strings.Contains(out, "abc-123") {
		t.Errorf("output = %q, want cluster id abc-123", out)
	}
}

// TestIncidents_PauseSendsPOST verifies Pause POSTs to /admin/pause with the
// duration_min and reason and prints a confirmation.
func TestIncidents_PauseSendsPOST(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"paused","duration_min":60,"reason":"paused via CLI"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok", false)
	var buf bytes.Buffer
	if err := c.Pause(context.Background(), &buf, 60, "paused via CLI"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if gotPath != "/admin/pause" {
		t.Errorf("path = %q, want /admin/pause", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody["duration_min"].(float64) != 60 {
		t.Errorf("duration_min = %v, want 60", gotBody["duration_min"])
	}
	if !strings.Contains(buf.String(), "paused for 60 minutes") {
		t.Errorf("output = %q, want 'paused for 60 minutes'", buf.String())
	}
}

// TestIncidents_StatsFormats verifies Stats renders the active/resolved/
// failed/total counts from /metrics-summary.
func TestIncidents_StatsFormats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics-summary" {
			t.Errorf("path = %q, want /metrics-summary", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"active":3,"resolved_24h":12,"failed":1,"total":16}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	var buf bytes.Buffer
	if err := c.Stats(context.Background(), &buf); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Sentinel Stats", "Active incidents:    3", "Resolved (24h):      12", "Failed:              1", "Total:               16"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestIncidents_JSONOutputBypassesFormatting verifies --json writes the raw
// response body for the list view.
func TestIncidents_JSONOutputBypassesFormatting(t *testing.T) {
	raw := `[{"incident_id":"abc","status":"resolved"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, raw)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", true)
	var buf bytes.Buffer
	if err := c.ListIncidents(context.Background(), &buf, IncidentFilters{Limit: 50}); err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if strings.TrimSpace(buf.String()) != strings.TrimSpace(raw) {
		t.Errorf("output = %q, want raw JSON %q", buf.String(), raw)
	}
}

// TestIncidents_EmptyList verifies an empty list prints "No incidents found."
func TestIncidents_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", false)
	var buf bytes.Buffer
	if err := c.ListIncidents(context.Background(), &buf, IncidentFilters{Limit: 50}); err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "No incidents found." {
		t.Errorf("output = %q, want 'No incidents found.'", buf.String())
	}
}

// TestParseIncidentFilters verifies the filter parser picks up --status,
// --service, --last, and --limit in both space and = forms.
func TestParseIncidentFilters(t *testing.T) {
	f := ParseIncidentFilters([]string{"--status", "investigating", "--service=demo-app", "--last", "7d", "--limit=100"})
	if f.Status != "investigating" {
		t.Errorf("Status = %q, want investigating", f.Status)
	}
	if f.Service != "demo-app" {
		t.Errorf("Service = %q, want demo-app", f.Service)
	}
	if f.Last != 7*24*time.Hour {
		t.Errorf("Last = %v, want 168h", f.Last)
	}
	if f.Limit != 500 {
		t.Errorf("Limit = %d, want 500 (auto-bumped because --last is set)", f.Limit)
	}
}

// TestFilterIncidents verifies the client-side filter on status, service, and
// time. The time filter uses created_at relative to "now", so the test pins
// a future timestamp to guarantee inclusion and a past one for exclusion.
func TestFilterIncidents(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(1 * time.Hour).Format(time.RFC3339)
	past := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	incidents := []map[string]any{
		{"status": "investigating", "created_at": future, "cluster": map[string]any{"service": "a"}},
		{"status": "resolved", "created_at": future, "cluster": map[string]any{"service": "b"}},
		{"status": "investigating", "created_at": past, "cluster": map[string]any{"service": "a"}},
	}
	got := filterIncidents(incidents, IncidentFilters{Status: "investigating", Service: "a", Last: 7 * 24 * time.Hour})
	if len(got) != 1 {
		t.Fatalf("filterIncidents = %d rows, want 1", len(got))
	}
	if got[0]["status"] != "investigating" {
		t.Errorf("filtered row status = %v, want investigating", got[0]["status"])
	}
}

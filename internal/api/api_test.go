package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
)

func newServer(t *testing.T) (*Server, model.Incident, model.Incident) {
	t.Helper()
	ctx := context.Background()
	l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	t0 := time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)
	resolved := model.Incident{ID: model.NewID(), State: model.IncidentResolved, Title: "old", OpenedAt: t0}
	open := model.Incident{ID: model.NewID(), State: model.IncidentOpen, Title: "new", OpenedAt: t0.Add(-time.Hour),
		Assessment: &model.Assessment{Class: "deploy.bad-image", Decision: "rollout.undo"}}
	sig := model.Signal{ID: model.NewID(), Type: "pod.image-pull-error"}
	if _, err := l.Append(ctx,
		ledger.RecordDraft("/test", model.KindIncident, "opened", resolved.ID, resolved.ID, resolved),
		ledger.RecordDraft("/test", model.KindIncident, "opened", open.ID, open.ID, open),
		ledger.RecordDraft("/test", model.KindSignal, "observed", sig.ID, open.ID, sig),
		ledger.Draft{Type: "sentinel.capability.reported", Source: "/test", Data: capability.Report{
			Cluster: capability.Cluster{Platform: "kind"}, Features: []capability.Feature{{Name: "detect.api", Enabled: true}}}},
	); err != nil {
		t.Fatal(err)
	}
	return &Server{Ledger: l, Version: "test", Verified: Verification{Events: 4, At: t0}}, open, resolved
}

func get(t *testing.T, s *Server, host, path string, out any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, rec.Body.String())
		}
	}
	return rec
}

func TestSummary(t *testing.T) {
	s, _, _ := newServer(t)
	var sum Summary
	if res := get(t, s, "localhost:8090", "/api/summary", &sum); res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	if sum.Mode != "observe" || sum.Incidents.Open != 1 || sum.Incidents.Total != 2 || sum.Ledger.HeadSeq != 4 ||
		sum.Cluster == nil || sum.Cluster.Platform != "kind" || len(sum.Features) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestIncidentsListOpenOnesFirst(t *testing.T) {
	s, open, resolved := newServer(t)
	var out struct{ Incidents []model.Incident }
	get(t, s, "127.0.0.1:8090", "/api/incidents", &out)
	if len(out.Incidents) != 2 || out.Incidents[0].ID != open.ID || out.Incidents[1].ID != resolved.ID {
		t.Fatalf("incidents = %+v", out.Incidents)
	}
}

func TestAnIncidentsHistory(t *testing.T) {
	s, open, _ := newServer(t)
	var d IncidentDetail
	get(t, s, "[::1]:8090", "/api/incidents/"+string(open.ID), &d)
	if d.Incident.ID != open.ID || d.Incident.Assessment == nil || len(d.Events) != 2 ||
		d.Events[0].Type != "sentinel.incident.opened" || d.Events[1].Type != "sentinel.signal.observed" {
		t.Fatalf("detail = %+v", d)
	}
	if res := get(t, s, "localhost", "/api/incidents/nope", nil); res.Code != http.StatusNotFound {
		t.Fatalf("unknown incident: status %d", res.Code)
	}
	if res := get(t, s, "localhost", "/api/nope", nil); res.Code != http.StatusNotFound {
		t.Fatalf("unknown route: status %d", res.Code)
	}
}

// A name an attacker controls, pointed at 127.0.0.1, must not reach the API.
func TestOnlyLoopbackHostsAreServed(t *testing.T) {
	s, _, _ := newServer(t)
	for host, want := range map[string]int{
		"localhost:8090": http.StatusOK, "127.0.0.1:8090": http.StatusOK, "[::1]:8090": http.StatusOK,
		"evil.example:8090": http.StatusForbidden, "10.0.0.5:8090": http.StatusForbidden, "localhost.evil.example": http.StatusForbidden,
	} {
		if res := get(t, s, host, "/api/summary", nil); res.Code != want {
			t.Errorf("host %s: status %d, want %d", host, res.Code, want)
		}
	}
}

// With the UI mounted, its routes and the API's must coexist, and writes
// must be refused.
func TestTheUIAndTheAPIShareTheServer(t *testing.T) {
	s, _, _ := newServer(t)
	s.UI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ui")) })
	if res := get(t, s, "localhost", "/incidents/x", nil); res.Code != http.StatusOK || res.Body.String() != "ui" {
		t.Fatalf("UI route: %d %q", res.Code, res.Body.String())
	}
	if res := get(t, s, "localhost", "/api/nope", nil); res.Code != http.StatusNotFound {
		t.Fatalf("unknown API route: %d", res.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/summary", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d, want 405", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _, _ := newServer(t)
	res := get(t, s, "localhost", "/api/summary", nil)
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if res.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}

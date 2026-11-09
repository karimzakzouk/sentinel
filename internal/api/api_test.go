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
	"github.com/karimzakzouk/sentinel/internal/capture"
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

func TestActionsListEachActionAtItsLatestStage(t *testing.T) {
	s, open, _ := newServer(t)
	ctx := context.Background()
	web := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	taken := model.Action{ID: model.NewID(), IncidentID: open.ID, Type: "rollout.undo", Target: web, State: model.ActionExecuting}
	shadow := model.Action{ID: model.NewID(), IncidentID: open.ID, Type: "scale.restore", Target: web, Shadow: true, State: model.ActionProposed}
	for _, step := range []struct {
		verb string
		a    model.Action
	}{{"requested", taken}, {"proposed", shadow}, {"executed", taken}, {"verified", taken}} {
		if _, err := s.Ledger.Append(ctx, ledger.RecordDraft("/test", model.KindAction, step.verb, step.a.ID, open.ID, step.a)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := s.Ledger.Append(ctx, ledger.Draft{Type: "sentinel.actionable.other", Source: "/test", Data: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	var out struct{ Actions []ActionSummary }
	if res := get(t, s, "localhost", "/api/actions", &out); res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	if len(out.Actions) != 2 || out.Actions[0].Action.ID != taken.ID || out.Actions[0].Stage != "verified" ||
		!out.Actions[0].Since.Before(out.Actions[0].At) || out.Actions[1].Stage != "proposed" {
		t.Fatalf("actions = %+v", out.Actions)
	}
}

func TestTrackRecordsSayWhatTheyShow(t *testing.T) {
	s, open, _ := newServer(t)
	key := model.TrackKey{Action: "rollout.undo", FaultClass: "deploy.bad-image", Service: "Deployment.apps/shop/web"}
	rec := model.TrackRecord{Key: key, Alpha: 29, Beta: 1, Level: model.AutonomyAutonomous}
	if _, err := s.Ledger.Append(context.Background(),
		ledger.RecordDraft("/test", model.KindTrackRecord, "updated", model.ID("track/"+key.String()), open.ID, rec)); err != nil {
		t.Fatal(err)
	}
	var out struct{ TrackRecords []TrackSummary }
	get(t, s, "localhost", "/api/trackrecords", &out)
	if len(out.TrackRecords) != 1 || out.TrackRecords[0].Successes != 28 || out.TrackRecords[0].Failures != 0 ||
		out.TrackRecords[0].Lower < 0.9 || out.TrackRecords[0].Key != key {
		t.Fatalf("track records = %+v", out.TrackRecords)
	}
}

func TestExperimentsListTheLabsVerdictsNewestFirst(t *testing.T) {
	s, open, resolved := newServer(t)
	for _, inc := range []model.Incident{resolved, open} {
		e := model.Experiment{ID: model.ExperimentID(inc.ID), IncidentID: inc.ID, Purpose: "verify-fix", Phase: "Passed", Runs: 15}
		if _, err := s.Ledger.Append(context.Background(), ledger.RecordDraft("/test", model.KindExperiment, "passed", e.ID, inc.ID, e)); err != nil {
			t.Fatal(err)
		}
	}
	var out struct{ Experiments []model.Experiment }
	get(t, s, "localhost", "/api/experiments", &out)
	if len(out.Experiments) != 2 || out.Experiments[0].IncidentID != open.ID || out.Experiments[0].Runs != 15 {
		t.Fatalf("experiments = %+v", out.Experiments)
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

// A workload's captured requests are listed newest first, and say so when
// capture is not installed.
func TestAWorkloadsRequestsAreListedNewestFirst(t *testing.T) {
	s, _, _ := newServer(t)
	if res := get(t, s, "localhost:8090", "/api/traffic/shop/Deployment/cart", nil); res.Code != http.StatusNotFound {
		t.Fatalf("without capture: %d", res.Code)
	}
	cart := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "cart"}
	s.Requests = func(ref model.ObjectRef, _ time.Time) []capture.Request {
		if ref != cart {
			return nil
		}
		return []capture.Request{{Served: true, Method: "GET", Path: "/a"}, {Served: true, Method: "GET", Path: "/b"}}
	}
	var out struct{ Requests []capture.Request }
	if res := get(t, s, "localhost:8090", "/api/traffic/shop/Deployment/cart", &out); res.Code != http.StatusOK ||
		len(out.Requests) != 2 || out.Requests[0].Path != "/b" {
		t.Fatalf("requests %d %+v", res.Code, out.Requests)
	}
	if res := get(t, s, "localhost:8090", "/api/traffic/shop/Pod/cart", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("a pod: %d", res.Code)
	}
	if res := get(t, s, "localhost:8090", "/api/traffic/shop/Deployment/other", nil); res.Code != http.StatusOK ||
		!json.Valid(res.Body.Bytes()) || string(res.Body.Bytes()) == "" || res.Body.String() == `{"requests":null}`+"\n" {
		t.Fatalf("none: %d %s", res.Code, res.Body.String())
	}
}

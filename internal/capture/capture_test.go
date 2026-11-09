package capture

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

const traces = `{"resourceSpans": [{
  "resource": {"attributes": [
    {"key": "k8s.namespace.name", "value": {"stringValue": "shop"}},
    {"key": "k8s.deployment.name", "value": {"stringValue": "cart"}}]},
  "scopeSpans": [{"spans": [
    {"name": "GET /cart/{id}", "kind": 2, "startTimeUnixNano": "1762700000000000000", "endTimeUnixNano": "1762700000120000000",
     "attributes": [
       {"key": "http.request.method", "value": {"stringValue": "GET"}},
       {"key": "url.path", "value": {"stringValue": "/cart/12345?token=s3cret"}},
       {"key": "http.response.status_code", "value": {"intValue": "200"}}]},
    {"name": "POST", "kind": 3, "startTimeUnixNano": "1762700000010000000", "endTimeUnixNano": "1762700000090000000",
     "attributes": [
       {"key": "http.request.method", "value": {"stringValue": "POST"}},
       {"key": "url.full", "value": {"stringValue": "https://api.payments.example/v1/charges"}},
       {"key": "server.port", "value": {"intValue": "443"}},
       {"key": "http.response.status_code", "value": {"intValue": "502"}}]}]}]},
 {"resource": {"attributes": [
    {"key": "k8s.namespace.name", "value": {"stringValue": "other"}},
    {"key": "k8s.deployment.name", "value": {"stringValue": "web"}}]},
  "scopeSpans": [{"spans": [
    {"kind": 2, "startTimeUnixNano": "1762700000000000000", "attributes": [
       {"key": "http.request.method", "value": {"stringValue": "GET"}}, {"key": "url.path", "value": {"stringValue": "/"}}]}]}]}]}`

// The agent's traces become requests of the workloads they name: a path's
// identifiers masked and its query dropped, a call's host kept, and a
// namespace that did not turn capture on kept out.
func TestTheReceiverKeepsRequestsOfNamespacesThatTurnedCaptureOn(t *testing.T) {
	store := &Store{Enabled: func(ns string) bool { return ns == "shop" }}
	rc := &Receiver{Store: store, Token: "t0ken"}
	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(traces))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rc.ServeHTTP(w, req)
		return w.Code
	}
	if code := post("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d", code)
	}
	if code := post("t0ken"); code != http.StatusOK {
		t.Fatalf("traces: %d", code)
	}
	cart := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "cart"}
	got := store.Recent(cart, time.Time{})
	if len(got) != 2 {
		t.Fatalf("kept %+v", got)
	}
	if s := got[0]; !s.Served || s.Path != "/cart/12345" || s.Status != 200 || s.Duration != 120*time.Millisecond {
		t.Fatalf("served %+v", s)
	}
	if c := got[1]; c.Served || c.Host != "api.payments.example" || c.Port != 443 || c.Path != "/v1/charges" || c.Status != 502 {
		t.Fatalf("call %+v", c)
	}
	if other := store.Recent(model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "other", Name: "web"}, time.Time{}); len(other) != 0 {
		t.Fatalf("kept another namespace's %+v", other)
	}
	if store.LastSeen().IsZero() {
		t.Fatal("no request seen")
	}
}

// What a Lab replays are the reads production answered, most frequent first.
func TestReplayableAreReadsThatWorked(t *testing.T) {
	got := Replayable([]Request{
		{Served: true, Method: "GET", Path: "/cart/12345", Status: 200},
		{Served: true, Method: "GET", Path: "/cart/12345", Status: 200},
		{Served: true, Method: "GET", Path: "/reset/[REDACTED:token]", Status: 200, Redacted: true},
		{Served: true, Method: "GET", Path: "/", Status: 200},
		{Served: true, Method: "POST", Path: "/checkout", Status: 200},
		{Served: true, Method: "GET", Path: "/broken", Status: 503},
		{Served: false, Method: "GET", Path: "/v1/rates", Host: "rates.example", Status: 200},
	}, 5)
	if strings.Join(got, ",") != "GET /cart/12345,GET /" {
		t.Fatalf("replayable %q", got)
	}
}

// A secret in a path is redacted, and the request is not replayed.
func TestASecretInAPathIsRedacted(t *testing.T) {
	q, ok := requestOf(span{Kind: kindServer, Attributes: []attribute{
		attr("http.request.method", "GET"), attr("url.path", "/hooks/ghp_"+strings.Repeat("a1B2", 9)+"/run")}})
	if !ok || !q.Redacted || strings.Contains(q.Path, "ghp_") {
		t.Fatalf("request %+v", q)
	}
	if got := Replayable([]Request{q}, 5); len(got) != 0 {
		t.Fatalf("replayable %q", got)
	}
}

func attr(key, value string) attribute {
	a := attribute{Key: key}
	a.Value.String = &value
	return a
}

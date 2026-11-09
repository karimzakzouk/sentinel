package capture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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
		{Served: true, Method: "GET", Path: "/cart/12345", Port: 8080, Status: 200},
		{Served: true, Method: "GET", Path: "/cart/12345", Port: 8080, Status: 200},
		{Served: true, Method: "GET", Path: "/reset/[REDACTED:token]", Status: 200, Redacted: true},
		{Served: true, Method: "GET", Path: "/", Status: 200},
		{Served: true, Method: "GET", Path: "/a b", Status: 200},
		{Served: true, Method: "HEAD", Path: "/", Status: 200},
		{Served: true, Method: "POST", Path: "/checkout", Status: 200},
		{Served: true, Method: "GET", Path: "/broken", Status: 503},
		{Served: false, Method: "GET", Path: "/v1/rates", Host: "rates.example", Status: 200},
	}, 5)
	if want := []Read{{Path: "/cart/12345", Port: 8080}, {Path: "/"}}; !slices.Equal(got, want) {
		t.Fatalf("replayable %+v", got)
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
		t.Fatalf("replayable %+v", got)
	}
}

func attr(key, value string) attribute {
	a := attribute{Key: key}
	a.Value.String = &value
	return a
}

func intAttr(key string, value int) attribute {
	a := attribute{Key: key}
	a.Value.Int = json.Number(strconv.Itoa(value))
	return a
}

// gRPC calls are kept under either generation of the conventions, their
// codes mapped to HTTP statuses; a datastore's spans, whose statements
// carry data, are not.
func TestGRPCCallsAreKeptAndDatastoreStatementsAreNot(t *testing.T) {
	old, ok := requestOf(span{Kind: kindClient, Attributes: []attribute{attr("rpc.system", "grpc"), attr("rpc.service", "hipstershop.CurrencyService"),
		attr("rpc.method", "Convert"), intAttr("rpc.grpc.status_code", 14), attr("server.address", "currencyservice"), intAttr("server.port", 7000)}})
	if !ok || old.Method != "gRPC" || old.Path != "/hipstershop.CurrencyService/Convert" || old.Status != 503 || old.GRPC != "UNAVAILABLE" ||
		old.Host != "currencyservice" || old.Port != 7000 {
		t.Fatalf("old conventions %+v", old)
	}
	current, ok := requestOf(span{Kind: kindServer, Attributes: []attribute{attr("rpc.system.name", "grpc"),
		attr("rpc.method", "grpc.health.v1.Health/Check"), attr("rpc.status_code", "OK")}})
	if !ok || current.Path != "/grpc.health.v1.Health/Check" || current.Status != 200 || current.GRPC != "OK" {
		t.Fatalf("current conventions %+v", current)
	}
	if got := Replayable([]Request{current}, 5); len(got) != 0 {
		t.Fatalf("a gRPC call is replayable: %+v", got)
	}
	if q, ok := requestOf(span{Kind: kindClient, Attributes: []attribute{attr("db.system.name", "redis"),
		attr("db.query.text", "HMGET 9d336a7e-70c1-4102-bd4b-2e70643bce47 data"), attr("server.address", "redis-cart")}}); ok {
		t.Fatalf("kept a datastore's statement: %+v", q)
	}
}

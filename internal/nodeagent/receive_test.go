package nodeagent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTheReceiverKeepsBatchesOfWatchedNamespaces(t *testing.T) {
	var hooked Batch
	store := &Store{
		Allowed: func(ns string) bool { return ns == "shop" },
		OnBatch: func(b Batch) { hooked = b },
	}
	rc := &Receiver{Store: store, Token: "t0ken"}
	body := `{"node":"n1","lines":[
		{"at":"2024-01-02T03:04:05Z","namespace":"shop","pod":"web","container":"web","text":"err"},
		{"at":"2024-01-02T03:04:06Z","namespace":"other","pod":"db","container":"db","text":"nope"}
	],"cgroups":[{"namespace":"shop","pod":"web","memoryPeak":9,"pressure":"p"}]}`
	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rc.ServeHTTP(w, req)
		return w.Code
	}
	if code := post("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code := post("t0ken"); code != http.StatusOK {
		t.Fatalf("batch: %d", code)
	}
	got := store.Recent("shop", "web", "web", time.Time{})
	if len(got) != 1 || got[0].Text != "err" {
		t.Fatalf("kept %+v", got)
	}
	if other := store.Recent("other", "db", "db", time.Time{}); len(other) != 0 {
		t.Fatalf("kept other %+v", other)
	}
	if len(hooked.Lines) != 1 || hooked.Lines[0].Text != "err" || len(hooked.Cgroups) != 1 || hooked.Node != "n1" {
		t.Fatalf("hook %+v", hooked)
	}
	if store.LastSeen().IsZero() {
		t.Fatal("no line seen")
	}
}

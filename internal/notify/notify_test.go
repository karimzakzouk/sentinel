package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func TestSlackPostsTheNotice(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body.Text
	}))
	defer srv.Close()
	s := NewSlack(srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	web := model.ObjectRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	s.Notify(context.Background(), Notice{Kind: KindApproval, Incident: "inc-1", Workload: web,
		Title: "Sentinel asks to roll back shop/web", Detail: "sentinel approve shop/sentinel-1-rollout-undo"})
	select {
	case text := <-got:
		for _, want := range []string{":raised_hand:", "*Sentinel asks to roll back shop/web*", "sentinel approve", "inc-1"} {
			if !strings.Contains(text, want) {
				t.Errorf("text %q lacks %q", text, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice arrived")
	}
}

func TestSlackKeepsToItsRate(t *testing.T) {
	calls := make(chan struct{}, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls <- struct{}{} }))
	defer srv.Close()
	s := NewSlack(srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for range 20 {
		s.Notify(context.Background(), Notice{Kind: KindEscalation, Title: "x"})
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(calls); n != 6 {
		t.Fatalf("%d notices sent at once, want the burst of 6", n)
	}
}

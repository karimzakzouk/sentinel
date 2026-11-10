package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/redact"
)

// A temp dir of fake kubelet log files is tailed, redacted and posted as a
// JSON batch; optional cgroup memory.peak and pressure ride along.
func TestTheAgentTailsFakePodLogsAndPostsABatch(t *testing.T) {
	root := t.TempDir()
	podDir := filepath.Join(root, "shop_web-abc_uid-1", "web")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"2024-01-02T03:04:05.000000000Z stdout F password=s3cretValueHereXXXX failed",
		"2024-01-02T03:04:06.000000000Z stdout F all good",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(podDir, "0.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cgroupRoot := t.TempDir()
	cg := filepath.Join(cgroupRoot, "kubepods-poduid_1.slice")
	if err := os.MkdirAll(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cg, "memory.peak"), []byte("1048576\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cg, "memory.pressure"), []byte("some avg10=1.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got Batch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" || r.Header.Get("Authorization") != "Bearer t0ken" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		LogRoot: root, CgroupRoot: cgroupRoot, Endpoint: srv.URL, Token: "t0ken",
		Node: "node-a", Namespaces: map[string]bool{"shop": true}, FromStart: true,
	}
	if err := pollOnce(context.Background(), cfg, redact.New(), &Sender{Endpoint: srv.URL, Token: "t0ken"}, map[string]*tailState{}); err != nil {
		t.Fatal(err)
	}
	if got.Node != "node-a" || len(got.Lines) != 2 {
		t.Fatalf("batch %+v", got)
	}
	if strings.Contains(got.Lines[0].Text, "s3cret") {
		t.Fatalf("secret survived redaction: %q", got.Lines[0].Text)
	}
	if got.Lines[0].Namespace != "shop" || got.Lines[0].Pod != "web-abc" || got.Lines[0].Container != "web" {
		t.Fatalf("line meta %+v", got.Lines[0])
	}
	if !strings.Contains(got.Lines[0].Text, "failed") || got.Lines[1].Text != "all good" {
		t.Fatalf("texts %q %q", got.Lines[0].Text, got.Lines[1].Text)
	}
	if len(got.Cgroups) != 1 || got.Cgroups[0].MemoryPeak != 1048576 || !strings.Contains(got.Cgroups[0].Pressure, "avg10") {
		t.Fatalf("cgroups %+v", got.Cgroups)
	}
}

func TestTheAgentSkipsOtherNamespacesAndSeeksToEnd(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "other_db_uid-2", "db")
	watch := filepath.Join(root, "shop_api_uid-3", "api")
	for _, d := range []string{other, watch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(other, "0.log"), []byte("2024-01-02T03:04:05.000000000Z stdout F secret\n"), 0o644)
	_ = os.WriteFile(filepath.Join(watch, "0.log"), []byte("2024-01-02T03:04:05.000000000Z stdout F old\n"), 0o644)

	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	states := map[string]*tailState{}
	cfg := Config{LogRoot: root, Endpoint: srv.URL, Token: "t", Namespaces: map[string]bool{"shop": true}}
	sender := &Sender{Endpoint: srv.URL, Token: "t"}
	if err := pollOnce(context.Background(), cfg, redact.New(), sender, states); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("first sight from end should post nothing, got %d posts", posts)
	}
	if err := os.WriteFile(filepath.Join(watch, "0.log"), []byte(
		"2024-01-02T03:04:05.000000000Z stdout F old\n"+
			"2024-01-02T03:04:06.000000000Z stdout F new line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got Batch
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()
	cfg.Endpoint = srv2.URL
	sender.Endpoint = srv2.URL
	if err := pollOnce(context.Background(), cfg, redact.New(), sender, states); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Text != "new line" {
		t.Fatalf("expected only the new line, got %+v", got.Lines)
	}
}

func TestStripCRIPrefix(t *testing.T) {
	at, text := stripCRIPrefix("2024-01-02T03:04:05.123456789Z stderr F boom")
	if at.IsZero() || text != "boom" {
		t.Fatalf("%v %q", at, text)
	}
	if _, text := stripCRIPrefix("plain"); text != "plain" {
		t.Fatalf("%q", text)
	}
}

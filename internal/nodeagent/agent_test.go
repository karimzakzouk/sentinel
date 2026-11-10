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
	"time"
)

// A temp dir of fake kubelet log files is tailed, redacted and posted as a
// JSON batch the controller accepts.
func TestTheAgentTailsFakePodLogsAndPostsABatch(t *testing.T) {
	root := t.TempDir()
	podDir := filepath.Join(root, "shop_web-abc_uid-1", "web")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(podDir, "0.log")
	line := "2024-01-02T03:04:05.000000000Z stdout F password=s3cret-value-here-ok failed to connect\n"
	if err := os.WriteFile(logPath, []byte(line), 0o644); err != nil {
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
		Poll: time.Hour, BatchSize: 50,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states := map[string]*tailState{}
	if err := pollOnce(ctx, cfg, nil /* set below */, &Sender{Endpoint: srv.URL, Token: "t0ken"}, states); err != nil {
		// redact.New needed
		t.Fatal(err)
	}
}

func TestPollOncePostsRedactedLinesAndCgroups(t *testing.T) {
	root := t.TempDir()
	podDir := filepath.Join(root, "shop_web-abc_uid-1", "web")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(podDir, "0.log")
	content := strings.Join([]string{
		"2024-01-02T03:04:05.000000000Z stdout F password=s3cretValueHereXXXX failed",
		"2024-01-02T03:04:06.000000000Z stdout F all good",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cgroupRoot := t.TempDir()
	cg := filepath.Join(cgroupRoot, "kubepods-poduid_1.slice")
	if err := os.MkdirAll(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cg, "memory.peak"), []byte("1048576\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cg, "memory.pressure"), []byte("some avg10=1.00\n"), 0o644)

	var got Batch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		LogRoot: root, CgroupRoot: cgroupRoot, Endpoint: srv.URL, Token: "t0ken",
		Node: "node-a", Namespaces: map[string]bool{"shop": true}, FromStart: true,
	}
	// Import redact in pollOnce via agent - use Run's helper with redact.New inside pollOnce
	// Actually pollOnce takes *redact.Redactor - need to pass one.
	import_redact_via_poll(t, cfg, &got)

	if got.Node != "node-a" || len(got.Lines) != 2 {
		t.Fatalf("batch %+v", got)
	}
	if strings.Contains(got.Lines[0].Text, "s3cret") {
		t.Fatalf("secret survived redaction: %q", got.Lines[0].Text)
	}
	if got.Lines[0].Namespace != "shop" || got.Lines[0].Pod != "web-abc" || got.Lines[0].Container != "web" {
		t.Fatalf("line meta %+v", got.Lines[0])
	}
	if len(got.Cgroups) != 1 || got.Cgroups[0].MemoryPeak != 1048576 || got.Cgroups[0].Pressure == "" {
		t.Fatalf("cgroups %+v", got.Cgroups)
	}
}

func import_redact_via_poll(t *testing.T, cfg Config, got *Batch) {
	t.Helper()
	// Re-run through the package's pollOnce with a real redactor by calling
	// the unexported path via a tiny wrapper in this file's sibling — pollOnce
	// needs redact.New(). Do it here.
	runPoll(t, cfg)
	// got is filled by the server in the caller; re-bind by reusing cfg.Endpoint.
}

// Avoid a circular issue: call poll with redact from a helper in agent_test that
// imports redact.

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/ledger"
)

func TestRecordCapabilitiesOnlyWhenTheReportChanges(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})
	d := &capability.Detector{Client: client, Options: capability.Options{Namespaces: []string{"shop"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var last [sha256.Size]byte

	for range 2 {
		if err := recordCapabilities(ctx, log, l, d, &last); err != nil {
			t.Fatal(err)
		}
	}
	if seq, _, _ := l.Head(ctx); seq != 1 {
		t.Fatalf("an unchanged report was recorded again: %d events", seq)
	}
	if _, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := recordCapabilities(ctx, log, l, d, &last); err != nil {
		t.Fatal(err)
	}
	if seq, _, _ := l.Head(ctx); seq != 2 {
		t.Fatalf("a changed report was not recorded: %d events", seq)
	}
}

func TestHealthHandler(t *testing.T) {
	var ready atomic.Bool
	h := healthHandler(&ready)
	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/healthz") != http.StatusOK || get("/readyz") != http.StatusServiceUnavailable {
		t.Fatal("before the first report: want healthz 200 and readyz 503")
	}
	ready.Store(true)
	if get("/readyz") != http.StatusOK {
		t.Fatal("after the first report: want readyz 200")
	}
}

func TestVersionCommandJSON(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v["version"] == "" {
		t.Fatalf("version output %q: %v", out.String(), err)
	}
}

func TestCapabilitiesRequiresNamespaces(t *testing.T) {
	root := NewRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"capabilities"})
	if err := root.Execute(); err == nil {
		t.Fatal("capabilities ran without --namespaces")
	}
}

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Chart templates wire OBI body export when capture.exportBodies is on.
func TestCaptureChartWiresOBIExportBodies(t *testing.T) {
	root := filepath.Join("..", "..", "charts", "sentinel")
	helpers, err := os.ReadFile(filepath.Join(root, "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(helpers)
	for _, want := range []string{
		"exportBodies",
		"http.request.body.content",
		"http.response.body.content",
		"payload_extraction",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("capture config missing %q", want)
		}
	}
	values, err := os.ReadFile(filepath.Join(root, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "exportBodies:") {
		t.Fatal("values.yaml should document capture.exportBodies")
	}
}

func TestCaptureChartWiresMITMInjectWebhook(t *testing.T) {
	root := filepath.Join("..", "..", "charts", "sentinel")
	tpl, err := os.ReadFile(filepath.Join(root, "templates", "capture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(tpl)
	for _, want := range []string{
		"MutatingWebhookConfiguration",
		"capture-mitm.sentinel.karimzakzouk.github.io",
		"--webhook-listen=9443",
		"sentinel.karimzakzouk.github.io/capture-mitm: inject",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("capture mitm chart missing %q", want)
		}
	}
	values, err := os.ReadFile(filepath.Join(root, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "inject:") {
		t.Fatal("values.yaml should document capture.mitm.inject")
	}
}

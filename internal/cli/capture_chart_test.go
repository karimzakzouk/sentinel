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

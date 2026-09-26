package executors

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyBuildLogs(t *testing.T) {
	cases := []struct {
		name      string
		logs      string
		wantInfra bool
	}{
		{"rust compile error", "error[E0308]: mismatched types\n --> src/lib.rs:4:9", false},
		{"maven failure", "[ERROR] BUILD FAILURE\n[ERROR] Tests run: 3, Failures: 1", false},
		{"npm build error", "npm ERR! code ELIFECYCLE\nnpm ERR! errno 1", false},
		{"go compile", "./src/db.go:12:5: undefined: tokenStore", false},
		{"missing file", "COPY failed: file not found in build context: no such file or directory", false},
		{"edition problem", "error: failed to parse manifest\nCaused by: feature `edition2024` is required", false},
		{"registry auth", "failed to authorize: docker.io/unauthorized: authentication required", true},
		{"network", "dial tcp: lookup registry-1.docker.io: i/o timeout", true},
		{"kind mount quirk", "unlinkat //product_uuid: device or resource busy", true},
		{"rate limit", "toomanyrequests: too many requests", true},
		{"empty logs default infra", "", true},
		{"unknown logs default infra", "something completely novel happened", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyBuildLogs(tc.logs); got != tc.wantInfra {
				t.Errorf("ClassifyBuildLogs(%q) = %v (infra), want %v", tc.logs, got, tc.wantInfra)
			}
		})
	}
}

func TestClassifyBuildLogsCompileWinsOverInfra(t *testing.T) {
	// A compile error inside the layer build is the fix's fault even when
	// the log tail also shows a registry retry.
	logs := "error: could not compile `app`\nwarn: retrying after connection reset by peer"
	if ClassifyBuildLogs(logs) {
		t.Error("compile marker must win over infra marker")
	}
}

func TestBuildErrorMessage(t *testing.T) {
	be := &BuildError{Image: "x:y", Infra: false, Logs: "boom"}
	if !strings.Contains(be.Error(), "compile") {
		t.Errorf("expected compile in message, got %q", be.Error())
	}
	be.Infra = true
	if !strings.Contains(be.Error(), "infrastructure") {
		t.Errorf("expected infrastructure in message, got %q", be.Error())
	}
	var target error = be
	var parsed *BuildError
	if !errors.As(target, &parsed) {
		t.Error("errors.As should unwrap BuildError")
	}
}

package executors

import (
	"testing"
)

// TestSyntaxValidators_NoSilentPass verifies the v3.11.0 fix for the triple-fault:
//   1. distroless has no python3/gofmt/node binaries
//   2. readOnlyRootFilesystem:true makes /tmp unwritable
//   3. The old validators swallowed os.CreateTemp failures with `return nil`
//
// The pure-Go validators must:
//   - Actually detect broken syntax (not silently return nil)
//   - Not depend on os.CreateTemp or external binaries
//   - Run in-process (no exec.Command)
//
// Regression test for the audit's CRITICAL #1 + #10.
func TestSyntaxValidators_NoSilentPass(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		content  string
		wantFail bool
	}{
		// Python
		{"python_valid", "app/main.py", "def foo():\n    return 1\n", false},
		{"python_unclosed_brace", "app/main.py", "def foo():\n    d = {1: 2\n", true},
		{"python_unclosed_paren", "app/main.py", "def foo():\n    bar(a, b\n", true},
		{"python_unclosed_string", "app/main.py", "x = 'unterminated\n", true},
		{"python_empty", "app/main.py", "", true},

		// Go — uses real go/parser, so this is a real syntax check.
		{"go_valid", "main.go", "package main\n\nfunc main() {}\n", false},
		{"go_missing_paren", "main.go", "package main\n\nfunc main( {}\n", true},
		{"go_unclosed_brace", "main.go", "package main\n\nfunc main() {\n", true},
		{"go_empty", "main.go", "", true},

		// JS
		{"js_valid", "app/index.js", "function foo() { return 1; }\n", false},
		{"js_unclosed_brace", "app/index.js", "function foo() { return 1;\n", true},
		{"js_unclosed_paren", "app/index.js", "foo(a, b;\n", true},

		// JSON — uses encoding/json, real check.
		{"json_valid", "config.json", `{"a": 1}`, false},
		{"json_missing_brace", "config.json", `{"a": 1`, true},
		{"json_trailing_comma", "config.json", `{"a": 1,}`, true},

		// YAML — uses gopkg.in/yaml.v3.
		{"yaml_valid", "config.yaml", "key: value\n", false},
		{"yaml_bad_indent", "config.yaml", "key:\n  a: 1\n b: 2\n", true},

		// Unknown ext — should pass (skip validation).
		{"unknown_ext", "README.md", "anything goes", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSyntax(tt.path, tt.content)
			if tt.wantFail && err == nil {
				t.Errorf("validateSyntax(%q) returned nil, want error (silent-pass bug regression)", tt.path)
			}
			if !tt.wantFail && err != nil {
				t.Errorf("validateSyntax(%q) returned error: %v, want nil", tt.path, err)
			}
		})
	}
}

// TestSyntaxValidators_RunInProcess verifies the validators don't shell out.
// If they did, they'd fail in distroless (the original bug). We can't directly
// observe exec.Command from a test, but we can verify the validators succeed
// even when the test environment has no python3/gofmt/node on PATH (the test
// env is a Go-only container). If the validators tried to exec, they'd fail
// with "executable not found" — which would surface as an error here, failing
// the test. So a passing test is sufficient evidence of in-process validation.
func TestSyntaxValidators_RunInProcess(t *testing.T) {
	// These would all fail if we shelled out to python3/gofmt/node.
	if err := validateSyntax("a.py", "x = 1\n"); err != nil {
		t.Errorf("python validation required external binary: %v", err)
	}
	if err := validateSyntax("a.go", "package main\n"); err != nil {
		t.Errorf("go validation required external binary: %v", err)
	}
	if err := validateSyntax("a.js", "var x = 1;\n"); err != nil {
		t.Errorf("js validation required external binary: %v", err)
	}
}

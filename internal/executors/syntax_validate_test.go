package executors

import (
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/syntaxvalidate"
)

// TestSyntaxValidators_NoSilentPass verifies the pure-Go validators
// actually detect broken syntax (don't silently return nil). Catches
// Python, Go, JS, JSON, YAML, and C-family brace-balance.
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
		{"python_comment_with_braces", "app/main.py", "# this { has } a ( paren\nx = 1\n", false},

		// Go — uses real go/parser.
		{"go_valid", "main.go", "package main\n\nfunc main() {}\n", false},
		{"go_missing_paren", "main.go", "package main\n\nfunc main( {}\n", true},
		{"go_unclosed_brace", "main.go", "package main\n\nfunc main() {\n", true},
		{"go_empty", "main.go", "", true},

		// JS
		{"js_valid", "app/index.js", "function foo() { return 1; }\n", false},
		{"js_unclosed_brace", "app/index.js", "function foo() { return 1;\n", true},
		{"js_unclosed_paren", "app/index.js", "foo(a, b;\n", true},
		{"js_block_comment_unterminated", "app/index.js", "/* never closed\n", true},

		// JSON
		{"json_valid", "config.json", `{"a": 1}`, false},
		{"json_missing_brace", "config.json", `{"a": 1`, true},
		{"json_trailing_comma", "config.json", `{"a": 1,}`, true},

		// YAML
		{"yaml_valid", "config.yaml", "key: value\n", false},
		{"yaml_bad_indent", "config.yaml", "key:\n  a: 1\n b: 2\n", true},

		// C-family — brace balance.
		{"java_unclosed_brace", "App.java", "class App { void foo() {\n", true},
		{"java_valid", "App.java", "class App { void foo() {} }\n", false},
		{"rs_unclosed_paren", "app.rs", "fn main() { foo(a, b; }\n", true},
		{"rb_unclosed_brace", "app.rb", "def foo\n  x = {a: 1\nend\n", true},

		// Unknown ext — should pass (skip validation).
		{"unknown_ext", "README.md", "anything goes", false},
		{"unknown_ext_broken", "notes.txt", "this { has unbalanced", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := syntaxvalidate.Validate(tt.path, tt.content)
			if tt.wantFail && err == nil {
				t.Errorf("syntaxvalidate.Validate(%q) returned nil, want error (silent-pass bug regression)", tt.path)
			}
			if !tt.wantFail && err != nil {
				t.Errorf("syntaxvalidate.Validate(%q) returned error: %v, want nil", tt.path, err)
			}
		})
	}
}

// TestSyntaxValidators_RunInProcess verifies the validators don't shell
// out. If they did, they'd fail in distroless (the original v5 bug).
func TestSyntaxValidators_RunInProcess(t *testing.T) {
	if err := syntaxvalidate.Validate("a.py", "x = 1\n"); err != nil {
		t.Errorf("python validation required external binary: %v", err)
	}
	if err := syntaxvalidate.Validate("a.go", "package main\n"); err != nil {
		t.Errorf("go validation required external binary: %v", err)
	}
	if err := syntaxvalidate.Validate("a.js", "var x = 1;\n"); err != nil {
		t.Errorf("js validation required external binary: %v", err)
	}
}

// TestDetectLanguage covers the language-detector used by testrunner.go's
// default-command fallback.
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"app/main.py", "python"},
		{"app/main.PY", "python"}, // case-insensitive ext
		{"foo/bar.go", "go"},
		{"index.js", "javascript"},
		{"index.jsx", "javascript"},
		{"App.tsx", "typescript"},
		{"App.ts", "typescript"},
		{"App.java", "java"},
		{"App.kt", "kotlin"},
		{"App.kts", "kotlin"},
		{"main.rs", "rust"},
		{"app.rb", "ruby"},
		{"App.cs", "csharp"},
		{"app.php", "php"},
		{"app.swift", "swift"},
		{"App.scala", "scala"},
		{"App.sc", "scala"},
		{"main.c", "c"},
		{"header.h", "c"},
		{"main.cpp", "cpp"},
		{"main.cc", "cpp"},
		{"main.cxx", "cpp"},
		{"header.hpp", "cpp"},
		{"header.hxx", "cpp"},
		{"README.md", ""},
		{"Makefile", ""},
		{"noext", ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := detectLanguage(c.path); got != c.want {
				t.Errorf("detectLanguage(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestFileExt verifies the local fileExt helper (mimics filepath.Ext but
// without importing path/filepath).
func TestFileExt(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"a.py", ".py"},
		{"/foo/bar/main.go", ".go"},
		{"noext", ""},
		{"trailing/", ""},
		{".bashrc", ".bashrc"},
		{"a.tar.gz", ".gz"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := fileExt(c.path); got != c.want {
				t.Errorf("fileExt(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestValidateBraceBalance_Empty verifies the empty-content guard.
func TestValidateBraceBalance_Empty(t *testing.T) {
	if err := validateBraceBalance("", []rune{'{', '}'}); err == nil {
		t.Error("expected error for empty content")
	}
	if err := validateBraceBalance("   \n  ", []rune{'{', '}'}); err == nil {
		t.Error("expected error for whitespace-only content")
	}
}

// TestValidateBraceBalance_Strings verifies string literals are skipped.
func TestValidateBraceBalance_Strings(t *testing.T) {
	// An "unbalanced" brace inside a string literal should NOT fail.
	if err := validateBraceBalance(`x = "this { is fine"`, []rune{'{', '}'}); err != nil {
		t.Errorf("brace inside string literal should be skipped, got: %v", err)
	}
	if err := validateBraceBalance(`x = 'unterminated`, []rune{'{', '}'}); err == nil {
		t.Error("unterminated single-quoted string should fail")
	}
	if err := validateBraceBalance("x = `unterminated", []rune{'{', '}'}); err == nil {
		t.Error("unterminated backtick template literal should fail")
	}
}

// TestValidateBraceBalance_LineComments verifies # and // comments are skipped.
func TestValidateBraceBalance_LineComments(t *testing.T) {
	// An "unbalanced" brace inside a line comment should NOT fail.
	if err := validateBraceBalance("# this { has a brace\nx = 1\n", []rune{'{', '}'}); err != nil {
		t.Errorf("brace inside # comment should be skipped, got: %v", err)
	}
	if err := validateBraceBalance("// this { has a brace\nx = 1;\n", []rune{'{', '}'}); err != nil {
		t.Errorf("brace inside // comment should be skipped, got: %v", err)
	}
}

// TestValidateBraceBalance_PythonTripleQuoted verifies triple-quoted
// strings are skipped (Python docstrings).
func TestValidateBraceBalance_PythonTripleQuoted(t *testing.T) {
	content := `def foo():
    """docstring with { brace"""
    return 1
`
	if err := validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'}); err != nil {
		t.Errorf("brace inside triple-quoted docstring should be skipped, got: %v", err)
	}
}

// TestValidatePython_NoParenInComment verifies a paren that appears only
// in a comment does not need to be balanced.
func TestValidatePython_NoParenInComment(t *testing.T) {
	if err := validatePython("# see foo(bar)\nx = 1\n"); err != nil {
		t.Errorf("paren in comment should be skipped, got: %v", err)
	}
}

// TestValidateGo_RealASTErrors verifies the Go validator catches more than
// just brace imbalance (real AST errors).
func TestValidateGo_RealASTErrors(t *testing.T) {
	cases := []string{
		"package main\n\nfunc main() {\n  x :=\n}\n", // missing expr
		"package main\n\nfunc )\n",                   // bad func decl
		"package main\n\nimport \"\n",                // unterminated string
	}
	for _, c := range cases {
		t.Run("case", func(t *testing.T) {
			if err := validateGo(c); err == nil {
				t.Errorf("validateGo should reject %q", c)
			} else if !strings.Contains(err.Error(), "go syntax error") {
				t.Errorf("validateGo error should mention 'go syntax error', got: %v", err)
			}
		})
	}
}

package syntaxvalidate

import (
	"strings"
	"testing"
)

// TestValidate_NoSilentPass verifies the validators actually detect
// broken syntax (don't silently return nil). Catches Python, Go, JS,
// JSON, YAML, and C-family brace-balance. This is the regression test
// for the original v5 "silent-pass" bug — a broken Python file slipped
// through validation, the executor wrote the broken file to the branch,
// and CI failed downstream with a much more confusing error.
//
// Migrated from internal/executors/syntax_validate_test.go (deleted in
// commit 52d5953). The original tests lived with the implementation; the
// shared-package refactor moved the implementation but lost the tests.
// This file restores them at the new home.
func TestValidate_NoSilentPass(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		content  string
		wantFail bool
	}{
		// Python — brace balance.
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

		// JS / TS — brace balance.
		{"js_valid", "app/index.js", "function foo() { return 1; }\n", false},
		{"js_unclosed_brace", "app/index.js", "function foo() { return 1;\n", true},
		{"js_unclosed_paren", "app/index.js", "foo(a, b;\n", true},
		{"js_block_comment_unterminated", "app/index.js", "/* never closed\n", true},

		// JSON — encoding/json.
		{"json_valid", "config.json", `{"a": 1}`, false},
		{"json_missing_brace", "config.json", `{"a": 1`, true},
		{"json_trailing_comma", "config.json", `{"a": 1,}`, true},

		// YAML — gopkg.in/yaml.v3.
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
			err := Validate(tt.path, tt.content)
			if tt.wantFail && err == nil {
				t.Errorf("Validate(%q) returned nil, want error (silent-pass regression)", tt.path)
			}
			if !tt.wantFail && err != nil {
				t.Errorf("Validate(%q) returned error: %v, want nil", tt.path, err)
			}
		})
	}
}

// TestValidate_RunInProcess verifies the validators don't shell out.
// If they did, they'd fail in distroless (the original v5 bug — the
// Python validator called `python3 -m py_compile` which doesn't exist
// in a distroless container).
func TestValidate_RunInProcess(t *testing.T) {
	if err := Validate("a.py", "x = 1\n"); err != nil {
		t.Errorf("python validation required external binary: %v", err)
	}
	if err := Validate("a.go", "package main\n"); err != nil {
		t.Errorf("go validation required external binary: %v", err)
	}
	if err := Validate("a.js", "var x = 1;\n"); err != nil {
		t.Errorf("js validation required external binary: %v", err)
	}
}

// TestValidateBraceBalance_Empty verifies the empty-content guard.
// Empty content should fail — an empty file is never a valid fix.
func TestValidateBraceBalance_Empty(t *testing.T) {
	if err := validateBraceBalance("", "Python", []rune{'{', '}'}); err == nil {
		t.Error("expected error for empty content")
	}
	if err := validateBraceBalance("   \n  ", "Python", []rune{'{', '}'}); err == nil {
		t.Error("expected error for whitespace-only content")
	}
}

// TestValidateBraceBalance_Strings verifies string literals are skipped.
// An "unbalanced" brace inside a string literal must NOT trigger a
// validation error.
func TestValidateBraceBalance_Strings(t *testing.T) {
	pairs := []rune{'{', '}'}
	if err := validateBraceBalance(`x = "this { is fine"`, "Python", pairs); err != nil {
		t.Errorf("brace inside string literal should be skipped, got: %v", err)
	}
	if err := validateBraceBalance(`x = 'unterminated`, "Python", pairs); err == nil {
		t.Error("unterminated single-quoted string should fail")
	}
	if err := validateBraceBalance("x = `unterminated", "TypeScript", pairs); err == nil {
		t.Error("unterminated backtick template literal should fail")
	}
}

// TestValidateBraceBalance_LineComments verifies # and // comments are
// skipped. An "unbalanced" brace inside a line comment must NOT fail.
func TestValidateBraceBalance_LineComments(t *testing.T) {
	pairs := []rune{'{', '}'}
	if err := validateBraceBalance("# this { has a brace\nx = 1\n", "Python", pairs); err != nil {
		t.Errorf("brace inside # comment should be skipped, got: %v", err)
	}
	if err := validateBraceBalance("// this { has a brace\nx = 1;\n", "TypeScript", pairs); err != nil {
		t.Errorf("brace inside // comment should be skipped, got: %v", err)
	}
}

// TestValidateBraceBalance_PythonTripleQuoted verifies triple-quoted
// strings are skipped (Python docstrings). A brace inside a docstring
// must NOT trigger a balance error.
func TestValidateBraceBalance_PythonTripleQuoted(t *testing.T) {
	content := `def foo():
    """docstring with { brace"""
    return 1
`
	pairs := []rune{'{', '}', '(', ')', '[', ']'}
	if err := validateBraceBalance(content, "Python", pairs); err != nil {
		t.Errorf("brace inside triple-quoted docstring should be skipped, got: %v", err)
	}
}

// TestValidate_PythonNoParenInComment verifies a paren that appears
// only in a comment does not need to be balanced.
func TestValidate_PythonNoParenInComment(t *testing.T) {
	if err := Validate("a.py", "# see foo(bar)\nx = 1\n"); err != nil {
		t.Errorf("paren in comment should be skipped, got: %v", err)
	}
}

// TestValidateGo_RealASTErrors verifies the Go validator catches more
// than just brace imbalance — real AST errors from go/parser.
func TestValidateGo_RealASTErrors(t *testing.T) {
	cases := []string{
		"package main\n\nfunc main() {\n  x :=\n}\n", // missing expr
		"package main\n\nfunc )\n",                   // bad func decl
		"package main\n\nimport \"\n",                // unterminated string
	}
	for i, c := range cases {
		t.Run("case", func(t *testing.T) {
			if err := validateGo(c); err == nil {
				t.Errorf("validateGo should reject case %d: %q", i, c)
			} else if !strings.Contains(err.Error(), "go syntax error") {
				t.Errorf("validateGo error should mention 'go syntax error', got: %v", err)
			}
		})
	}
}

// TestValidate_BlockCommentSameLineCode verifies that code appearing
// AFTER a closed block comment on the SAME line is still parsed.
//
// This catches a real bug in validateBraceBalance: when it sees `/*`,
// it finds the matching `*/`, then sets inLineComment = true. But
// inLineComment only resets on `\n`, so everything between `*/` and
// the next newline is wrongly skipped. If a closing brace sits after
// the block comment on the same line, it gets skipped, and the
// validator falsely reports "unclosed bracket".
//
// Example that SHOULD pass:
//
//	function foo() { /* ok */ return 1; }
//
// Before the fix: returns "unclosed bracket '{'" because `}` is
// skipped after the block comment. After the fix: returns nil.
func TestValidate_BlockCommentSameLineCode(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			"js_block_comment_then_close_brace_same_line",
			"a.js",
			"function foo() { /* ok */ return 1; }\n",
		},
		{
			"js_block_comment_inline",
			"a.js",
			"var x = 1; /* comment */ var y = 2;\n",
		},
		{
			"java_block_comment_same_line",
			"App.java",
			"class App { /* c */ void foo() {} }\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate(c.path, c.content); err != nil {
				t.Errorf("Validate(%q) = %v, want nil — code after a closed block comment on the same line must be parsed", c.path, err)
			}
		})
	}
}

// TestValidate_LineNumberInError verifies the validator reports a line
// number in its error message. This is what the fix-proposer feeds back
// to the LLM on retry — without a line number, the LLM has no way to
// know which line to fix.
func TestValidate_LineNumberInError(t *testing.T) {
	content := "def foo():\n    return 1\n    d = {1: 2\n" // unclosed { on line 3
	err := Validate("a.py", content)
	if err == nil {
		t.Fatal("expected error for unclosed brace")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("error should contain 'line' for LLM feedback, got: %v", err)
	}
}

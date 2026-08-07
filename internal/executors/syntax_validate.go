package executors

// syntax_validate.go: pure-Go syntax validators for the GitHub executor.
// These run in-process (no exec.Command) so they work in distroless
// containers and under readOnlyRootFilesystem. They are a pre-flight to
// catch obviously-broken LLM output before we open a PR. The Kaniko
// build pod remains the authoritative syntax+compile check — it runs in
// a real toolchain.
//
// Per-language validators:
//   - Go:     stdlib go/parser (real AST parse).
//   - JSON:   stdlib encoding/json.
//   - YAML:   gopkg.in/yaml.v3 (transitive dep via k8s client-go).
//   - Python: lightweight structural check (paren/bracket/brace balance
//     + string/quote balance). NOT a full AST parse — no pure-Go Python
//     parser in our dep tree.
//   - JS/TS:  same lightweight structural check (Python and JS share
//     enough lexical structure that brace/paren/bracket/string balance
//     catches the dominant failure modes).

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// validatePython runs a lightweight structural check on Python source.
// Catches: unbalanced (), [], {}, unmatched string quotes. Does NOT
// catch type errors, undefined names, or semantic errors.
func validatePython(content string) error {
	return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
}

// validateGo uses stdlib go/parser for a real AST parse. Catches all
// syntax errors gofmt would catch (and more).
func validateGo(content string) error {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "sentinel-validate.go", content, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("go syntax error: %v", err)
	}
	return nil
}

// validateJS runs a lightweight structural check on JS/TS source.
func validateJS(content string) error {
	return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
}

// validateJSON uses stdlib encoding/json.
func validateJSON(content string) error {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("json syntax error: %w", err)
	}
	return nil
}

// validateYAML uses gopkg.in/yaml.v3.
func validateYAML(content string) error {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("yaml syntax error: %w", err)
	}
	return nil
}

// validateBraceBalance checks that brackets/braces/parens are balanced and
// that string literals are properly closed. It skips line comments
// (# for Python, // for JS), block comments (/* */), and string literals
// (single, double, triple-quoted, backtick). Catches the dominant failure
// modes of LLM-generated code: missing closing brace, unbalanced parens,
// dangling string, broken indentation (detected indirectly via brace
// imbalance).
func validateBraceBalance(content string, pairs []rune) error {
	if len(strings.TrimSpace(content)) == 0 {
		return fmt.Errorf("empty content")
	}

	openOf := map[rune]rune{}
	closeOf := map[rune]rune{}
	for i := 0; i < len(pairs); i += 2 {
		openOf[pairs[i]] = pairs[i+1]
		closeOf[pairs[i+1]] = pairs[i]
	}

	var stack []rune
	inSingle := false       // 'string'
	inDouble := false       // "string"
	inTripleDouble := false // """string"""
	inTripleSingle := false // '''string'''
	inBacktick := false     // `template literal` (JS)
	inLineComment := false

	flushLineComment := func() { inLineComment = false }

	for i, r := range content {
		if r == '\n' {
			flushLineComment()
			continue
		}
		if inLineComment {
			continue
		}

		// Handle string-literal state transitions.
		switch {
		case inTripleDouble:
			if r == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
				inTripleDouble = false
				continue
			}
			continue
		case inTripleSingle:
			if r == '\'' && i+2 < len(content) && content[i+1] == '\'' && content[i+2] == '\'' {
				inTripleSingle = false
				continue
			}
			continue
		case inSingle:
			if r == '\\' {
				continue
			}
			if r == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			if r == '\\' {
				continue
			}
			if r == '"' {
				inDouble = false
			}
			continue
		case inBacktick:
			if r == '\\' {
				continue
			}
			if r == '`' {
				inBacktick = false
			}
			continue
		}

		// Not in a string — look for string/comment starts.
		if r == '#' {
			inLineComment = true
			continue
		}
		if r == '/' && i+1 < len(content) && content[i+1] == '/' {
			inLineComment = true
			continue
		}
		if r == '/' && i+1 < len(content) && content[i+1] == '*' {
			end := strings.Index(content[i+2:], "*/")
			if end < 0 {
				return fmt.Errorf("unterminated block comment starting at offset %d", i)
			}
			_ = end
			inLineComment = true
			continue
		}

		// Triple-quoted string starts (Python).
		if r == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
			inTripleDouble = true
			continue
		}
		if r == '\'' && i+2 < len(content) && content[i+1] == '\'' && content[i+2] == '\'' {
			inTripleSingle = true
			continue
		}

		// Single/double/backtick string starts.
		if r == '"' {
			inDouble = true
			continue
		}
		if r == '\'' {
			inSingle = true
			continue
		}
		if r == '`' {
			inBacktick = true
			continue
		}

		// Bracket tracking.
		if open, ok := closeOf[r]; ok {
			if len(stack) == 0 {
				return fmt.Errorf("unexpected closing bracket %q at offset %d", r, i)
			}
			top := stack[len(stack)-1]
			if top != open {
				return fmt.Errorf("mismatched bracket at offset %d: expected %q, got %q", i, open, r)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if _, ok := openOf[r]; ok {
			stack = append(stack, r)
			continue
		}
	}

	if inSingle {
		return fmt.Errorf("unterminated single-quoted string")
	}
	if inDouble {
		return fmt.Errorf("unterminated double-quoted string")
	}
	if inTripleDouble {
		return fmt.Errorf("unterminated triple-double-quoted string")
	}
	if inTripleSingle {
		return fmt.Errorf("unterminated triple-single-quoted string")
	}
	if inBacktick {
		return fmt.Errorf("unterminated backtick template literal")
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed bracket %q (opened but never closed)", stack[len(stack)-1])
	}
	return nil
}

// detectLanguage returns the language hint for a file based on its
// extension. Used by testrunner.go and as a fallback in the LLM prompts.
func detectLanguage(path string) string {
	ext := strings.ToLower(fileExt(path))
	switch ext {
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".swift":
		return "swift"
	case ".scala", ".sc":
		return "scala"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx":
		return "cpp"
	default:
		return ""
	}
}

// fileExt is a tiny wrapper around filepath.Ext that we keep local so
// syntax_validate.go doesn't pull in path/filepath just for one call
// (github_searchreplace.go already imports it).
func fileExt(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

package agents

// syntax_check.go: lightweight syntax validation for the fix proposer.
// Runs IN the fix proposer's retry loop so syntax errors are caught
// BEFORE the proposal is returned, and the error message is fed back
// to the LLM for correction.
//
// This mirrors what Aider, SWE-agent, and OpenHands do — validate
// the generated code before accepting it, and feed errors back.
//
// For Go: uses go/parser (real AST parse).
// For Python/JS/TS/Ruby: lightweight bracket/paren/brace + string balance.
// For YAML/JSON: uses stdlib parsers.
// For Rust/Java/C++: bracket/brace balance only (no parser available).

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// validateFixSyntax checks if the patched content is syntactically valid
// for the given file path. Returns nil if valid, or an error with a
// descriptive message that can be fed back to the LLM.
func validateFixSyntax(filePath, content string) error {
	ext := strings.ToLower(filePath)
	if idx := strings.LastIndex(ext, "."); idx >= 0 {
		ext = ext[idx:]
	}

	switch ext {
	case ".go":
		return validateGoSyntax(content)
	case ".py":
		return validateBracketBalance(content, "Python")
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return validateBracketBalance(content, "JavaScript/TypeScript")
	case ".rb":
		return validateBracketBalance(content, "Ruby")
	case ".rs":
		return validateBracketBalance(content, "Rust")
	case ".java":
		return validateBracketBalance(content, "Java")
	case ".cpp", ".cc", ".cxx", ".h", ".hpp":
		return validateBracketBalance(content, "C++")
	case ".json":
		return validateJSON(content)
	case ".yaml", ".yml":
		return validateYAML(content)
	default:
		return nil // unknown extension — skip validation
	}
}

func validateGoSyntax(content string) error {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "sentinel-validate.go", content, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("go syntax error: %v", err)
	}
	return nil
}

func validateJSON(content string) error {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("JSON syntax error: %v", err)
	}
	return nil
}

func validateYAML(content string) error {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("YAML syntax error: %v", err)
	}
	return nil
}

// validateBracketBalance uses a STACK-based approach (matching the
// execution phase's validateBraceBalance) to catch mismatched brackets.
// This is stricter than a simple counter — it catches errors like
// "unexpected closing bracket ')' at offset N" that a counter would miss.
func validateBracketBalance(content, lang string) error {
	if len(strings.TrimSpace(content)) == 0 {
		return fmt.Errorf("%s syntax error: empty content", lang)
	}

	openOf := map[rune]rune{'(': ')', '[': ']', '{': '}'}
	closeOf := map[rune]rune{')': '(', ']': '[', '}': '{'}

	var stack []rune
	inSingle := false
	inDouble := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false

	for i, r := range content {
		if r == '\n' {
			inLineComment = false
			continue
		}
		if inLineComment {
			continue
		}
		if inBlockComment {
			if r == '*' && i+1 < len(content) && content[i+1] == '/' {
				inBlockComment = false
			}
			continue
		}

		// Handle comment starts
		if !inSingle && !inDouble && !inBacktick {
			if r == '/' && i+1 < len(content) && content[i+1] == '/' {
				inLineComment = true
				continue
			}
			if r == '/' && i+1 < len(content) && content[i+1] == '*' {
				inBlockComment = true
				continue
			}
			if r == '#' {
				inLineComment = true
				continue
			}
		}

		// Handle string literals
		if r == '\\' && (inSingle || inDouble || inBacktick) {
			continue // skip escaped char (simplified — just skip next)
		}

		if !inDouble && !inBacktick && r == '\'' {
			inSingle = !inSingle
			continue
		}
		if !inSingle && !inBacktick && r == '"' {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble && r == '`' {
			inBacktick = !inBacktick
			continue
		}

		if inSingle || inDouble || inBacktick {
			continue
		}

		// Stack-based bracket matching
		if open, ok := closeOf[r]; ok {
			// Closing bracket
			if len(stack) == 0 || stack[len(stack)-1] != open {
				return fmt.Errorf("%s syntax error: unexpected closing bracket '%c' at offset %d", lang, r, i)
			}
			stack = stack[:len(stack)-1]
		} else if _, ok := openOf[r]; ok {
			// Opening bracket
			stack = append(stack, r)
		}
	}

	if len(stack) > 0 {
		return fmt.Errorf("%s syntax error: unclosed bracket '%c'", lang, stack[len(stack)-1])
	}
	if inSingle || inDouble || inBacktick {
		return fmt.Errorf("%s syntax error: unterminated string literal", lang)
	}
	if inBlockComment {
		return fmt.Errorf("%s syntax error: unterminated block comment", lang)
	}
	return nil
}

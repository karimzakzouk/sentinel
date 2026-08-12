package syntaxvalidate

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

func Validate(filePath, content string) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".py":
		return validateBraceBalance(content, "Python", []rune{'{', '}', '(', ')', '[', ']'})
	case ".go":
		return validateGo(content)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs":
		return validateBraceBalance(content, "TypeScript", []rune{'{', '}', '(', ')', '[', ']'})
	case ".json":
		return validateJSON(content)
	case ".yaml", ".yml":
		return validateYAML(content)
	case ".java", ".kt", ".kts", ".rs", ".rb", ".cs", ".php", ".swift",
		".scala", ".sc", ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx":
		return validateBraceBalance(content, ext, []rune{'{', '}', '(', ')', '[', ']'})
	default:
		return nil
	}
}

func validateGo(content string) error {
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
		return fmt.Errorf("json syntax error: %v", err)
	}
	return nil
}

func validateYAML(content string) error {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return fmt.Errorf("yaml syntax error: %v", err)
	}
	return nil
}

func validateBraceBalance(content, lang string, pairs []rune) error {
	if len(strings.TrimSpace(content)) == 0 {
		return fmt.Errorf("%s: empty content", lang)
	}

	openOf := map[rune]rune{}
	closeOf := map[rune]rune{}
	for i := 0; i < len(pairs); i += 2 {
		openOf[pairs[i]] = pairs[i+1]
		closeOf[pairs[i+1]] = pairs[i]
	}

	var stack []rune
	inSingle := false
	inDouble := false
	inTripleDouble := false
	inTripleSingle := false
	inBacktick := false
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
				return fmt.Errorf("%s: unterminated block comment at offset %d", lang, i)
			}
			_ = end
			inLineComment = true
			continue
		}

		if r == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
			inTripleDouble = true
			continue
		}
		if r == '\'' && i+2 < len(content) && content[i+1] == '\'' && content[i+2] == '\'' {
			inTripleSingle = true
			continue
		}

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

		if open, ok := closeOf[r]; ok {
			if len(stack) == 0 {
				lineNum := strings.Count(content[:i], "\n") + 1
				lineStart := strings.LastIndex(content[:i], "\n") + 1
				lineEnd := i
				if idx := strings.Index(content[i:], "\n"); idx >= 0 {
					lineEnd = i + idx
				}
				lineContent := strings.TrimSpace(content[lineStart:lineEnd])
				return fmt.Errorf("%s: unexpected '%c' at line %d. Content: %s", lang, r, lineNum, lineContent)
			}
			top := stack[len(stack)-1]
			if top != open {
				lineNum := strings.Count(content[:i], "\n") + 1
				return fmt.Errorf("%s: mismatched bracket at line %d: expected %q, got %q", lang, lineNum, open, r)
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
		return fmt.Errorf("%s: unterminated single-quoted string", lang)
	}
	if inDouble {
		return fmt.Errorf("%s: unterminated double-quoted string", lang)
	}
	if inTripleDouble {
		return fmt.Errorf("%s: unterminated triple-double-quoted string", lang)
	}
	if inTripleSingle {
		return fmt.Errorf("%s: unterminated triple-single-quoted string", lang)
	}
	if inBacktick {
		return fmt.Errorf("%s: unterminated backtick template literal", lang)
	}
	if len(stack) > 0 {
		return fmt.Errorf("%s: unclosed bracket %q", lang, stack[len(stack)-1])
	}
	return nil
}

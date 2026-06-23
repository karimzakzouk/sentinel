package agents

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// UniversalErrorParser extracts error information from ANY language's stack trace.
// It doesn't hardcode Python/Go/Java patterns — it uses universal heuristics
// that work across all languages.
//
// Universal patterns:
//   - Error type: any word ending in "Error", "Exception", or being "panic", "fatal"
//   - File:line: any "file.ext:line" or `File "path", line N` or `at path(N)` pattern
//   - Error detail: the text after the error type on the same line

type ParsedError struct {
	ErrorType string // e.g. "KeyError", "panic", "NullPointerException", "TypeError"
	ErrorDetail string // e.g. "'DATABASE_URLL'", "runtime error: index out of range"
	SourceFile string // e.g. "app/main.py", "src/handler.go", "com/acme/Foo.java"
	SourceLine string // e.g. "11", "42", "1337"
	Language string // detected language: "python", "go", "java", "javascript", "rust", "unknown"
	RawLine string // the full error line
}

// Universal patterns — match any language
var (
	// Error type: "KeyError:", "NullPointerException:", "panic:", "fatal error:", "TypeError:"
	// Matches any word ending in Error or Exception, plus panic/fatal/crash
	errorTypeRe = regexp.MustCompile(`(?i)(\w+(?:Error|Exception)):\s*(.+)?`)
	panicRe     = regexp.MustCompile(`(?i)^(?:panic|fatal error|fatal):\s*(.+)?`)
	sigRe       = regexp.MustCompile(`(?i)(SIGABRT|SIGSEGV|SIGBUS|SIGKILL|SIGTERM)`)

	// File:line patterns — universal across languages
	// Python:  File "/app/app/main.py", line 11, in <module>
	// Go:      main.go:42 +0x1a3   OR   at main.foo (main.go:42)
	// Java:    at com.acme.Foo.bar(Foo.java:42)
	// Node:    at Object.<anonymous> (/app/index.js:10:5)
	// Rust:    thread 'main' panicked at 'src/main.rs:13:5'
	// Generic: path/to/file.ext:line
	pythonFileRe = regexp.MustCompile(`File "([^"]+\.\w+)", line (\d+)`)
	genericFileRe = regexp.MustCompile(`(?:at\s+)?\(?(?:[\w./-]+/)?([\w-]+\.\w+):(\d+)`)
	javaFileRe    = regexp.MustCompile(`at\s+[\w.]+\(([\w]+\.java):(\d+)\)`)
)

// ParseError universally parses an error from any language's logs.
func ParseError(logs string) *ParsedError {
	lines := strings.Split(logs, "\n")

	// Scan from bottom up — the error type is usually the LAST line of a traceback
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		pe := parseErrorLine(line, logs)
		if pe != nil {
			return pe
		}
	}

	// If no error type found, try to find a file:line anywhere
	for _, line := range lines {
		pe := parseFileLine(line, logs)
		if pe != nil {
			return pe
		}
	}

	return nil
}

func parseErrorLine(line, fullLogs string) *ParsedError {
	// Try error type pattern (KeyError:, ValueError:, NullPointerException:, etc.)
	if m := errorTypeRe.FindStringSubmatch(line); len(m) >= 2 {
		pe := &ParsedError{
			ErrorType: m[1],
			RawLine:   line,
		}
		if len(m) >= 3 {
			pe.ErrorDetail = strings.TrimSpace(m[2])
		}
		pe.Language = detectLanguage(pe.ErrorType, fullLogs)
		// Find the source file from the full traceback
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	// Try panic pattern (Go, Rust)
	if m := panicRe.FindStringSubmatch(line); len(m) >= 1 {
		pe := &ParsedError{
			ErrorType: "panic",
			RawLine:   line,
		}
		if len(m) >= 2 {
			pe.ErrorDetail = strings.TrimSpace(m[1])
		}
		pe.Language = detectLanguage("panic", fullLogs)
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	// Try signal pattern
	if m := sigRe.FindStringSubmatch(line); len(m) >= 1 {
		pe := &ParsedError{
			ErrorType: m[1],
			RawLine:   line,
			Language:  "unknown",
		}
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	return nil
}

func parseFileLine(line, fullLogs string) *ParsedError {
	file, fileLine := extractFileAndLine(line)
	if file == "" {
		return nil
	}
	return &ParsedError{
		ErrorType:  "unknown",
		SourceFile: file,
		SourceLine: fileLine,
		Language:   detectLanguage("", fullLogs),
		RawLine:    line,
	}
}

// extractFileAndLine finds the first source file + line number in the logs.
// Works across all languages.
func extractFileAndLine(logs string) (string, string) {
	// Try Python format: File "path", line N
	if m := pythonFileRe.FindStringSubmatch(logs); len(m) >= 3 {
		return stripContainerPath(m[1]), m[2]
	}
	// Try Java format: at Class.method(File.java:N)
	if m := javaFileRe.FindStringSubmatch(logs); len(m) >= 3 {
		return m[1], m[2]
	}
	// Try generic format: file.ext:N
	if m := genericFileRe.FindStringSubmatch(logs); len(m) >= 3 {
		return m[1], m[2]
	}
	return "", ""
}

// stripContainerPath removes Docker/container paths like /app/, /usr/local/, etc.
func stripContainerPath(path string) string {
	// Common container WORKDIR prefixes
	prefixes := []string{"/app/", "/usr/local/lib/", "/usr/lib/", "/opt/", "/src/", "/home/"}
	for _, p := range prefixes {
		if idx := strings.Index(path, p); idx >= 0 {
			return path[idx+len(p):]
		}
	}
	return path
}

// detectLanguage guesses the language from the error type + log content.
func detectLanguage(errorType, logs string) string {
	logsLower := strings.ToLower(logs)

	// Python: Traceback, .py files, Python-specific errors
	if strings.Contains(logsLower, "traceback") || strings.Contains(logs, ".py") ||
		errorType == "KeyError" || errorType == "IndentationError" || errorType == "SyntaxError" {
		return "python"
	}
	// Go: panic, goroutine, .go files
	if strings.Contains(logsLower, "goroutine") || strings.Contains(logs, ".go") ||
		strings.HasPrefix(strings.ToLower(errorType), "panic") {
		return "go"
	}
	// Java: java.lang., .java files, at Class.method()
	if strings.Contains(logsLower, "java.lang.") || strings.Contains(logs, ".java") ||
		strings.Contains(logsLower, "at com.") || strings.Contains(logsLower, "at org.") {
		return "java"
	}
	// JavaScript/Node: at Object., at Function., .js/.ts files, UnhandledPromiseRejection
	if strings.Contains(logs, ".js") || strings.Contains(logs, ".ts") ||
		strings.Contains(logsLower, "unhandled") || strings.Contains(logsLower, "at object.") ||
		strings.Contains(logsLower, "at function.") {
		return "javascript"
	}
	// Rust: panicked, .rs files
	if strings.Contains(logsLower, "panicked") || strings.Contains(logs, ".rs") {
		return "rust"
	}
	// Ruby: .rb files
	if strings.Contains(logs, ".rb") {
		return "ruby"
	}
	// C/C++: segmentation fault, SIGSEGV, .c/.cpp files
	if strings.Contains(logsLower, "segmentation") || strings.Contains(logs, ".c") ||
		strings.Contains(logs, ".cpp") || strings.Contains(logs, ".h") {
		return "c"
	}
	return "unknown"
}

// GenerateHypothesesUniversal creates hypotheses from ANY language's error logs.
// This replaces the Python-specific generateHypothesesFromLogs.
func GenerateHypothesesUniversal(logs string, state *schemas.AgentState) []schemas.Hypothesis {
	parsed := ParseError(logs)
	if parsed == nil {
		return nil
	}

	var hyps []schemas.Hypothesis

	// Build the primary hypothesis — language-agnostic
	statement := fmt.Sprintf("The application crashes with %s", parsed.ErrorType)
	if parsed.ErrorDetail != "" {
		statement += fmt.Sprintf(" (%s)", parsed.ErrorDetail)
	}
	if parsed.SourceFile != "" && parsed.SourceLine != "" {
		statement += fmt.Sprintf(" at %s:%s", parsed.SourceFile, parsed.SourceLine)
	}

	// Add language-specific context based on the error type
	statement += generateContextForError(parsed)

	srcFiles := []string{}
	if parsed.SourceFile != "" {
		srcFiles = append(srcFiles, parsed.SourceFile)
	}

	hyps = append(hyps, schemas.Hypothesis{
		HID:                "H1",
		Statement:          statement,
		SupportingEvidence: []string{fmt.Sprintf("Pod logs: %s", parsed.RawLine)},
		Confidence:         0.7,
		SourceFiles:        srcFiles,
	})

	// Add a secondary hypothesis (environment/config issue)
	if parsed.ErrorDetail != "" {
		hyps = append(hyps, schemas.Hypothesis{
			HID:        "H2",
			Statement:  fmt.Sprintf("The value '%s' is missing, misconfigured, or not provided", parsed.ErrorDetail),
			Confidence: 0.3,
		})
	}

	log.Printf("[investigator] generated %d hypotheses from %s error context (lang=%s, type=%s)",
		len(hyps), parsed.Language, parsed.Language, parsed.ErrorType)
	return hyps
}

// generateContextForError adds language-specific reasoning to the hypothesis statement.
func generateContextForError(pe *ParsedError) string {
	switch {
	case pe.ErrorType == "KeyError":
		return fmt.Sprintf(". The key or environment variable '%s' is missing or misspelled.", pe.ErrorDetail)
	case pe.ErrorType == "ZeroDivisionError":
		return ". A division by zero occurs — a variable is not checked for zero before division."
	case pe.ErrorType == "ImportError" || pe.ErrorType == "ModuleNotFoundError":
		return ". A required module is missing or the import path is incorrect."
	case pe.ErrorType == "FileNotFoundError":
		return ". A required file is missing or the path is incorrect."
	case pe.ErrorType == "TypeError":
		return ". A type mismatch occurs — check function arguments and return types."
	case pe.ErrorType == "AttributeError":
		return ". An attribute is accessed on an object that doesn't have it — check for null/None."
	case pe.ErrorType == "panic":
		return fmt.Sprintf(". The program panicked: %s. Check for nil pointer dereference, index out of range, or assertion failure.", pe.ErrorDetail)
	case pe.ErrorType == "NullPointerException":
		return ". A null reference is accessed — check for uninitialized objects."
	case pe.ErrorType == "StackOverflowError":
		return ". Infinite recursion detected — check for recursive calls without base case."
	case pe.ErrorType == "SIGSEGV" || pe.ErrorType == "SIGABRT":
		return ". A segmentation fault occurred — check for memory access violations."
	case strings.Contains(pe.ErrorType, "Connection"):
		return ". A connection failed — check network connectivity and service availability."
	case strings.Contains(pe.ErrorType, "Timeout"):
		return ". An operation timed out — check service latency and timeout configuration."
	default:
		if pe.ErrorDetail != "" {
			return fmt.Sprintf(". Error detail: %s", pe.ErrorDetail)
		}
		return ""
	}
}

// ExtractSearchQueriesUniversal pulls search terms from ANY language's error logs.
func ExtractSearchQueriesUniversal(logs string) []string {
	var queries []string

	parsed := ParseError(logs)
	if parsed == nil {
		return queries
	}

	// Always search for the error type
	if parsed.ErrorType != "unknown" && parsed.ErrorType != "" {
		queries = append(queries, parsed.ErrorType)
	}

	// Search for the error detail (the variable name, key, etc.)
	if parsed.ErrorDetail != "" {
		// Clean up: strip quotes, brackets
		detail := strings.Trim(parsed.ErrorDetail, "'\"`[]()")
		// If it's a single word, search for it
		if !strings.Contains(detail, " ") && len(detail) > 2 {
			queries = append(queries, detail)
		}
	}

	// Search for the source file
	if parsed.SourceFile != "" {
		queries = append(queries, parsed.SourceFile)
	}

	// Dedup
	seen := map[string]bool{}
	var unique []string
	for _, q := range queries {
		if !seen[q] && q != "" {
			seen[q] = true
			unique = append(unique, q)
		}
	}

	log.Printf("[investigator] extracted %d search queries from %s logs: %v", len(unique), parsed.Language, unique)
	return unique
}

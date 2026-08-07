package agents

// error_parser.go: universal multi-language error parser. Extracts error type,
// detail, source file:line, function, and detected language from any
// language's stack trace (Python, Go, Java, JavaScript/Node, Rust, Ruby, C/C++).
//
// Strategy:
//   - Pass 1 (bottom-up): scan for explicit error-type or panic lines.
//     Preferred over signal-based errors so a Go "panic:" wins over the
//     "[signal SIGSEGV...]" line that follows it.
//   - Pass 2 (bottom-up): if no error type, scan for signal-based errors
//     (SIGSEGV, SIGABRT, ...).
//   - Pass 3 (top-down): if still nothing, scan for any file:line.
//
// extractFileAndLine prefers user code over stdlib paths (site-packages,
// /usr/local/lib, etc.) so a Python web-app investigation doesn't false-fire
// on uvicorn's internal frames.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// ParsedError is an alias for schemas.ParsedError. The canonical type lives
// in internal/schemas so the executors package doesn't have to import agents.
type ParsedError = schemas.ParsedError

// Universal error-type patterns. \w+ prefix avoids matching standalone
// "error" inside phrases like "runtime error".
var (
	errorTypeRe = regexp.MustCompile(`(?i)(\w+(?:Error|Exception))(?::\s*(.+)?)?`)

	// Node.js: "UnhandledPromiseRejectionWarning: Error: connect ECONNREFUSED"
	// where the actual error type is the bare "Error" after the colon.
	standaloneErrorRe = regexp.MustCompile(`(?i)(?:^|\s)(Error|Exception):\s*(.+)`)

	panicRe = regexp.MustCompile(`(?i)(?:^(?:panic|fatal error|fatal):\s*(.+)?|panicked at\s+'?(.+)?'?)`)
	sigRe   = regexp.MustCompile(`(?i)(SIGABRT|SIGSEGV|SIGBUS|SIGKILL|SIGTERM)`)

	rubyErrorRe = regexp.MustCompile(`\((\w+(?:Error|Exception))\)`)

	// File:line patterns. Language-specific first, generic last.
	pythonFileRe  = regexp.MustCompile(`File "([^"]+\.\w+)", line (\d+)`)
	javaFileRe    = regexp.MustCompile(`at\s+[\w.]+\(([\w]+\.java):(\d+)\)`)
	nodeFileRe    = regexp.MustCompile(`at\s+(?:[\w.<>\s]+\s*\()?\s*([^\s)]+\.js):(\d+)`)
	codeExtRe     = regexp.MustCompile(`\.(py|go|js|ts|tsx|jsx|java|rs|rb|c|cpp|cc|h|hpp|kt|swift|php|scala|cs)$`)
	genericFileRe = regexp.MustCompile(`(?:at\s+)?\(?(?:[\w./-]+/)?([\w-]+\.\w+):(\d+)`)

	httpReqRe           = regexp.MustCompile(`(?i)(?:GET|POST|PUT|DELETE|PATCH)\s+(/[^\s"]+)`)
	httpReqWithStatusRe = regexp.MustCompile(`(?i)(?:GET|POST|PUT|DELETE|PATCH)\s+(/[^\s"]+).*?(\d{3})`)
)

// ParseError parses an error from any language's logs. Returns nil if no
// error is found.
func ParseError(logs string) *ParsedError {
	lines := strings.Split(logs, "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if pe := parseErrorTypeLine(line, logs); pe != nil {
			return pe
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if pe := parseSignalLine(line, logs); pe != nil {
			return pe
		}
	}

	for _, line := range lines {
		if pe := parseFileLine(line, logs); pe != nil {
			return pe
		}
	}
	return nil
}

// parseErrorTypeLine checks a line for error-type patterns (KeyError:,
// NullPointerException, panic:, panicked at, (NoMethodError), standalone Error:).
// Does NOT check for signals — those are handled in parseSignalLine.
func parseErrorTypeLine(line, fullLogs string) *ParsedError {
	if m := errorTypeRe.FindStringSubmatch(line); len(m) >= 2 && isValidErrorTypeMatch(line, m[1]) {
		pe := &ParsedError{
			ErrorType: m[1],
			RawLine:   line,
			Function:  extractFunction(line),
		}
		if len(m) >= 3 {
			pe.ErrorDetail = strings.TrimSpace(m[2])
		}
		pe.Language = detectLanguage(pe.ErrorType, fullLogs)
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	if m := rubyErrorRe.FindStringSubmatch(line); len(m) >= 2 {
		pe := &ParsedError{
			ErrorType: m[1],
			RawLine:   line,
			Function:  extractFunction(line),
			Language:  detectLanguage(m[1], fullLogs),
		}
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	// Panic (Go, Rust) — checked before standaloneErrorRe so "panic: runtime
	// error: ..." is classified as "panic", not "error".
	if m := panicRe.FindStringSubmatch(line); len(m) >= 1 {
		pe := &ParsedError{
			ErrorType: "panic",
			RawLine:   line,
			Function:  extractFunction(line),
		}
		if len(m) >= 2 && m[1] != "" {
			pe.ErrorDetail = strings.TrimSpace(m[1])
		} else if len(m) >= 3 && m[2] != "" {
			pe.ErrorDetail = strings.TrimSpace(m[2])
		}
		pe.Language = detectLanguage("panic", fullLogs)
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}

	if m := standaloneErrorRe.FindStringSubmatch(line); len(m) >= 3 {
		pe := &ParsedError{
			ErrorType:   m[1],
			ErrorDetail: strings.TrimSpace(m[2]),
			RawLine:     line,
			Function:    extractFunction(line),
			Language:    detectLanguage(m[1], fullLogs),
		}
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}
	return nil
}

func parseSignalLine(line, fullLogs string) *ParsedError {
	if m := sigRe.FindStringSubmatch(line); len(m) >= 1 {
		pe := &ParsedError{
			ErrorType: m[1],
			RawLine:   line,
			Function:  extractFunction(line),
			Language:  detectLanguage(m[1], fullLogs),
		}
		pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
		return pe
	}
	return nil
}

// isValidErrorTypeMatch guards against errorTypeRe matching a substring of a
// larger word. The match is accepted only if it's at a word boundary on the
// left side (preceded by start-of-line, whitespace, quote, paren, dot, or
// colon). The colon case handles Node.js warnings like
// "UnhandledPromiseRejectionWarning: Error: ..." where "Error" is preceded
// by ": ".
func isValidErrorTypeMatch(line, matched string) bool {
	idx := strings.Index(line, matched)
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	prev := line[idx-1]
	return prev == ' ' || prev == '\t' || prev == '"' || prev == '\'' ||
		prev == '(' || prev == '[' || prev == '.' || prev == ':'
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
		Function:   extractFunction(line),
	}
}

// extractFileAndLine finds the first user-code file + line number in logs.
// Prefers user code over stdlib paths (site-packages, /usr/local/lib) so a
// Python web app's traceback doesn't return a uvicorn internal frame.
func extractFileAndLine(logs string) (string, string) {
	pythonMatches := pythonFileRe.FindAllStringSubmatch(logs, -1)
	for _, m := range pythonMatches {
		if len(m) >= 3 && !strings.Contains(m[1], "site-packages/") && !strings.Contains(m[1], "dist-packages/") {
			return stripContainerPath(m[1]), m[2]
		}
	}
	if len(pythonMatches) > 0 && len(pythonMatches[0]) >= 3 {
		return stripContainerPath(pythonMatches[0][1]), pythonMatches[0][2]
	}

	if m := javaFileRe.FindStringSubmatch(logs); len(m) >= 3 {
		return m[1], m[2]
	}

	// Prefer Node.js paths with a "/" prefix (user code) over bare module names.
	nodeMatches := nodeFileRe.FindAllStringSubmatch(logs, -1)
	for _, m := range nodeMatches {
		if len(m) >= 3 && strings.Contains(m[1], "/") {
			return stripContainerPath(m[1]), m[2]
		}
	}
	if len(nodeMatches) > 0 && len(nodeMatches[0]) >= 3 {
		return stripContainerPath(nodeMatches[0][1]), nodeMatches[0][2]
	}

	// Generic file.ext:N — only if the extension is a known code extension.
	// Prevents matching "127.0.0.1:5432" as file="0.1", line="5432".
	for _, m := range genericFileRe.FindAllStringSubmatch(logs, -1) {
		if len(m) >= 3 && codeExtRe.MatchString(m[1]) {
			return stripContainerPath(m[1]), m[2]
		}
	}
	return "", ""
}

// stripContainerPath removes common container WORKDIR prefixes (/app/,
// /usr/local/lib/, /usr/lib/, /opt/, /src/, /home/).
func stripContainerPath(path string) string {
	for _, p := range []string{"/app/", "/usr/local/lib/", "/usr/lib/", "/opt/", "/src/", "/home/"} {
		if idx := strings.Index(path, p); idx >= 0 {
			return path[idx+len(p):]
		}
	}
	return path
}

// detectLanguage guesses the language from the error type and log content.
// Rust's "panicked" (past tense) distinguishes it from Go's "panic:" (present).
func detectLanguage(errorType, logs string) string {
	logsLower := strings.ToLower(logs)

	if strings.Contains(logsLower, "traceback") || strings.Contains(logs, ".py") ||
		errorType == "KeyError" || errorType == "IndentationError" || errorType == "SyntaxError" {
		return "python"
	}
	if strings.Contains(logsLower, "panicked") || strings.Contains(logs, ".rs") {
		return "rust"
	}
	if strings.Contains(logsLower, "goroutine") || strings.Contains(logs, ".go") ||
		strings.HasPrefix(strings.ToLower(errorType), "panic") {
		return "go"
	}
	if strings.Contains(logsLower, "java.lang.") || strings.Contains(logs, ".java") ||
		strings.Contains(logsLower, "at com.") || strings.Contains(logsLower, "at org.") {
		return "java"
	}
	if strings.Contains(logs, ".js") || strings.Contains(logs, ".ts") ||
		strings.Contains(logsLower, "unhandled") || strings.Contains(logsLower, "at object.") ||
		strings.Contains(logsLower, "at function.") {
		return "javascript"
	}
	if strings.Contains(logs, ".rb") {
		return "ruby"
	}
	if strings.Contains(logsLower, "segmentation") || strings.Contains(logs, ".c") ||
		strings.Contains(logs, ".cpp") || strings.Contains(logs, ".h") {
		return "c"
	}
	return "unknown"
}

// GenerateHypothesesUniversal creates hypotheses from any language's error
// logs. Returns nil if no error can be parsed.
func GenerateHypothesesUniversal(logs string, _ *schemas.AgentState) []schemas.Hypothesis {
	parsed := ParseError(logs)
	if parsed == nil {
		return nil
	}

	var hyps []schemas.Hypothesis

	statement := fmt.Sprintf("The application crashes with %s", parsed.ErrorType)
	if parsed.ErrorDetail != "" {
		statement += fmt.Sprintf(" (%s)", parsed.ErrorDetail)
	}
	if parsed.SourceFile != "" && parsed.SourceLine != "" {
		statement += fmt.Sprintf(" at %s:%s", parsed.SourceFile, parsed.SourceLine)
	}
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

	if parsed.ErrorDetail != "" {
		hyps = append(hyps, schemas.Hypothesis{
			HID:        "H2",
			Statement:  fmt.Sprintf("The value '%s' is missing, misconfigured, or not provided", parsed.ErrorDetail),
			Confidence: 0.3,
		})
	}

	logging.Info("generated hypotheses from error context",
		"count", len(hyps), "language", parsed.Language, "error_type", parsed.ErrorType)
	return hyps
}

// generateContextForError adds language-specific reasoning to the hypothesis.
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

// ExtractSearchQueriesUniversal pulls search terms from any language's error
// logs. Returns the error type, error detail (if a single word), and source
// file path, deduplicated.
func ExtractSearchQueriesUniversal(logs string) []string {
	var queries []string
	parsed := ParseError(logs)
	if parsed == nil {
		return queries
	}
	if parsed.ErrorType != "unknown" && parsed.ErrorType != "" {
		queries = append(queries, parsed.ErrorType)
	}
	if parsed.ErrorDetail != "" {
		detail := strings.Trim(parsed.ErrorDetail, "'\"`[]()")
		if !strings.Contains(detail, " ") && len(detail) > 2 {
			queries = append(queries, detail)
		}
	}
	if parsed.SourceFile != "" {
		queries = append(queries, parsed.SourceFile)
	}

	seen := map[string]bool{}
	var unique []string
	for _, q := range queries {
		if !seen[q] && q != "" {
			seen[q] = true
			unique = append(unique, q)
		}
	}
	logging.Info("extracted search queries from logs",
		"count", len(unique), "language", parsed.Language, "queries", unique)
	return unique
}

// ExtractTriggerEndpoint pulls the HTTP request path that triggered the error
// from pod logs. Returns "" if no HTTP request is found (non-web service).
// Prefers 4xx/5xx requests (the failing request) over 2xx requests.
func ExtractTriggerEndpoint(logs string) string {
	path, _ := ExtractTriggerEndpointWithMethod(logs)
	return path
}

// ExtractTriggerEndpointWithMethod returns both the path AND the HTTP method.
// Filters to safe methods for replay (GET/HEAD only) — POST/DELETE/PUT have
// side effects and are not safe to replay in the sandbox without explicit
// operator opt-in.
func ExtractTriggerEndpointWithMethod(logs string) (path, method string) {
	// First, find a request with a 4xx/5xx status code.
	for _, m := range httpReqWithStatusRe.FindAllStringSubmatch(logs, -1) {
		if len(m) >= 3 {
			status := m[2]
			if len(status) == 3 && (status[0] == '4' || status[0] == '5') {
				method = extractMethod(m[0])
				path = m[1]
				return
			}
		}
	}

	// Fall back to any HTTP request (no status code visible or all 2xx).
	if m := httpReqRe.FindStringSubmatch(logs); len(m) >= 2 {
		method = extractMethod(m[0])
		path = m[1]
		return
	}
	return "", ""
}

func extractMethod(logLine string) string {
	lower := strings.ToLower(logLine)
	for _, verb := range []string{"get", "post", "put", "delete", "patch", "head"} {
		if strings.HasPrefix(lower, verb) {
			return strings.ToUpper(verb)
		}
	}
	return "GET"
}

// extractFunction extracts the function/method name from a stack-trace frame
// line. Works across all languages by finding the identifier positioned
// predictably relative to the file:line reference.
//
//	Python:  File "app.py", line 70, in get_config   -> after "in "
//	Go:      main.get_config(main.go:70)              -> before "("
//	Java:    at com.acme.Foo.get_config(Foo.java:70)  -> before "("
//	JS:      at get_config (/app/index.js:70:5)        -> before "("
//	Rust:    at get_config::handler (src/main.rs:70:5) -> before "("
//	Ruby:    /app/app.rb:70:in `get_config'           -> after "in `"
//	C#:      at get_config() in /app/Program.cs:line 70 -> before "()"
func extractFunction(rawLine string) string {
	if rawLine == "" {
		return ""
	}
	if m := inFunctionRe.FindStringSubmatch(rawLine); m != nil {
		return m[1]
	}
	if m := callFunctionRe.FindStringSubmatch(rawLine); m != nil {
		return m[1]
	}
	if m := rustFunctionRe.FindStringSubmatch(rawLine); m != nil {
		return m[1]
	}
	return ""
}

// Pre-compiled regexes for function extraction. Position-based (not
// language-specific) — where the function name sits relative to file:line.
var (
	// "in <name>" — Python, Ruby, C#.
	inFunctionRe = regexp.MustCompile(`\bin\s+` + "`?" + `([\w.<>]+)` + "`?" + `'?'`)

	// "<name>(" — Go, Java, JS, Rust, C#. Captures the last identifier before "(".
	callFunctionRe = regexp.MustCompile(`(?:at\s+)?([\w.<>]+)\s*\(`)

	// "<name>::" — Rust module::function paths.
	rustFunctionRe = regexp.MustCompile(`([\w]+::[\w:]+)`)
)

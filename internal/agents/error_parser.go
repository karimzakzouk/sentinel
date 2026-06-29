package agents

import (
        "fmt"
        "log"
        "regexp"
        "strings"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// UniversalErrorParser extracts error information from ANY language's stack trace.
// It doesn't hardcode Python/Go/Java patterns — it uses universal heuristics
// that work across all languages.
//
// Universal patterns:
//   - Error type: any word ending in "Error", "Exception", or being "panic", "fatal"
//   - File:line: any "file.ext:line" or `File "path", line N` or `at path(N)` pattern
//   - Error detail: the text after the error type on the same line

// ParsedError is an alias for schemas.ParsedError. Kept for backwards
// compatibility — new code should use schemas.ParsedError directly.
//
// The canonical type lives in internal/schemas so the executors package
// doesn't have to import agents (fixes LEAK-1 from the architecture audit).
type ParsedError = schemas.ParsedError

// Universal patterns — match any language
var (
        // Error type: "KeyError:", "NullPointerException:", "panic:", "fatal error:", "TypeError:"
        // Matches any word ending in Error or Exception, plus panic/fatal/crash.

        // "StackOverflowError" and "OutOfMemoryError" that appear without colons.
        // The \w+ prefix is REQUIRED to avoid matching standalone "error" inside
        // phrases like "runtime error" (which would shadow the real "panic:" line).
        errorTypeRe = regexp.MustCompile(`(?i)(\w+(?:Error|Exception))(?::\s*(.+)?)?`)

        // "UnhandledPromiseRejectionWarning: Error: connect ECONNREFUSED" where the
        // actual error type is the bare "Error" after the colon. Requires a colon
        // to avoid false positives on prose like "an error occurred".
        standaloneErrorRe = regexp.MustCompile(`(?i)(?:^|\s)(Error|Exception):\s*(.+)`)

        // (past tense, mid-line) in addition to Go's "panic:" / "fatal error:".
        panicRe = regexp.MustCompile(`(?i)(?:^(?:panic|fatal error|fatal):\s*(.+)?|panicked at\s+'?(.+)?'?)`)
        sigRe   = regexp.MustCompile(`(?i)(SIGABRT|SIGSEGV|SIGBUS|SIGKILL|SIGTERM)`)

        rubyErrorRe = regexp.MustCompile(`\((\w+(?:Error|Exception))\)`)

        // File:line patterns — universal across languages
        // Python:  File "/app/app/main.py", line 11, in <module>
        // Go:      main.go:42 +0x1a3   OR   at main.foo (main.go:42)
        // Java:    at com.acme.Foo.bar(Foo.java:42)
        // Node:    at Object.<anonymous> (/app/index.js:10:5)
        // Rust:    thread 'main' panicked at 'src/main.rs:13:5'
        // Generic: path/to/file.ext:line
        pythonFileRe = regexp.MustCompile(`File "([^"]+\.\w+)", line (\d+)`)
        javaFileRe    = regexp.MustCompile(`at\s+[\w.]+\(([\w]+\.java):(\d+)\)`)

        //   "at Foo (/path/file.js:line:col)"  — with function name
        //   "at /path/file.js:line:col"         — without function name (user code)
        // The function name part is optional to handle bare-path frames.
        nodeFileRe = regexp.MustCompile(`at\s+(?:[\w.<>\s]+\s*\()?\s*([^\s)]+\.js):(\d+)`)

        // matching IP:port pairs (e.g. "127.0.0.1:5432") and other noise.
        codeExtRe = regexp.MustCompile(`\.(py|go|js|ts|tsx|jsx|java|rs|rb|c|cpp|cc|h|hpp|kt|swift|php|scala|cs)$`)
        genericFileRe   = regexp.MustCompile(`(?:at\s+)?\(?(?:[\w./-]+/)?([\w-]+\.\w+):(\d+)`)
)

// ParseError universally parses an error from any language's logs.
//

//   - Pass 1 (bottom-up): look for explicit error types (KeyError:, panic:,
//     panicked at, (NoMethodError)). These take top priority.
//   - Pass 2 (bottom-up): if no explicit error type, look for signal-based
//     errors (SIGSEGV, SIGABRT, etc.).
//   - Pass 3 (top-down): if still nothing, look for any file:line.
//
// Why split passes 1 and 2: a bottom-up single-pass would return SIGSEGV for a
// Go panic whose line 2 is "[signal SIGSEGV:...]", even though the actual
// error is "panic: ..." on line 1. By scanning the whole log for panic/error
// patterns FIRST, we prefer the real error type over the signal it raised.
func ParseError(logs string) *ParsedError {
        lines := strings.Split(logs, "\n")

        // Pass 1: scan bottom-up for an explicit error-type or panic line.
        for i := len(lines) - 1; i >= 0; i-- {
                line := strings.TrimSpace(lines[i])
                if line == "" {
                        continue
                }
                pe := parseErrorTypeLine(line, logs)
                if pe != nil {
                        return pe
                }
        }

        // Pass 2: no error type found — scan for signal-based errors.
        for i := len(lines) - 1; i >= 0; i-- {
                line := strings.TrimSpace(lines[i])
                if line == "" {
                        continue
                }
                pe := parseSignalLine(line, logs)
                if pe != nil {
                        return pe
                }
        }

        // Pass 3: still nothing — try to find a file:line anywhere.
        for _, line := range lines {
                pe := parseFileLine(line, logs)
                if pe != nil {
                        return pe
                }
        }

        return nil
}

// parseErrorTypeLine checks a line for error-type patterns (KeyError:,
// NullPointerException, panic:, panicked at, (NoMethodError), standalone Error:).
// Does NOT check for signals — those are handled in a separate pass.
//

// "panic: runtime error: ..." is classified as "panic" (not "error" from the
// "runtime error:" substring). standaloneErrorRe is a fallback for Node.js
// warnings that don't have a panic prefix.
func parseErrorTypeLine(line, fullLogs string) *ParsedError {
        // Error type pattern (KeyError:, ValueError:, NullPointerException:, etc.)
        if m := errorTypeRe.FindStringSubmatch(line); len(m) >= 2 && isValidErrorTypeMatch(line, m[1]) {
                pe := &ParsedError{
                        ErrorType: m[1],
                        RawLine:   line,
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
                        Language:  detectLanguage(m[1], fullLogs),
                }
                pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
                return pe
        }

        // Panic pattern (Go, Rust) — checked BEFORE standaloneErrorRe so that
        // "panic: runtime error: ..." is classified as "panic", not "error".
        if m := panicRe.FindStringSubmatch(line); len(m) >= 1 {
                pe := &ParsedError{
                        ErrorType: "panic",
                        RawLine:   line,
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

        // Checked LAST so it only fires when no more-specific pattern matched.
        if m := standaloneErrorRe.FindStringSubmatch(line); len(m) >= 3 {
                pe := &ParsedError{
                        ErrorType:  m[1],
                        ErrorDetail: strings.TrimSpace(m[2]),
                        RawLine:    line,
                        Language:   detectLanguage(m[1], fullLogs),
                }
                pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
                return pe
        }

        return nil
}

// parseSignalLine checks a line for signal-based errors (SIGSEGV, SIGABRT, etc.)
func parseSignalLine(line, fullLogs string) *ParsedError {
        if m := sigRe.FindStringSubmatch(line); len(m) >= 1 {
                pe := &ParsedError{
                        ErrorType: m[1],
                        RawLine:   line,
                        Language:  detectLanguage(m[1], fullLogs),
                }
                pe.SourceFile, pe.SourceLine = extractFileAndLine(fullLogs)
                return pe
        }
        return nil
}

// isValidErrorTypeMatch guards against errorTypeRe matching a substring of a
// larger word. With the optional-prefix v3.3.1 change, the regex would match
// "Error" inside "UnhandledPromiseRejectionWarning" (as a substring of a larger
// token) — but that's not the actual error type. We accept the match only if
// it's at a word boundary on the left side (preceded by start-of-line,
// whitespace, quote, paren, dot, or colon).
//
// The colon case handles Node.js warnings like:
//   "UnhandledPromiseRejectionWarning: Error: connect ECONNREFUSED"
// where "Error" is preceded by ": " (colon-space).
func isValidErrorTypeMatch(line, matched string) bool {
        idx := strings.Index(line, matched)
        if idx < 0 {
                return false
        }
        if idx == 0 {
                return true
        }
        prev := line[idx-1]
        return prev == ' ' || prev == '\t' || prev == '"' || prev == '\'' || prev == '(' || prev == '[' || prev == '.' || prev == ':'
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
//

// Java, Node.js) before falling back to the generic pattern. The generic
// pattern is now restricted to known code extensions to avoid false matches
// on IP:port pairs like "127.0.0.1:5432".
func extractFileAndLine(logs string) (string, string) {

        // used FindStringSubmatch (first match) which always returned the first
        // frame — for web apps (FastAPI/Flask + uvicorn), that's always
        // site-packages/uvicorn/... This caused a false "shared library bug"
        // detection on every Python web-app investigation.
        // Now: scan ALL Python File "..." matches and prefer the first one
        // that's NOT in site-packages (i.e. the user's actual code).
        pythonMatches := pythonFileRe.FindAllStringSubmatch(logs, -1)
        for _, m := range pythonMatches {
                if len(m) >= 3 && !strings.Contains(m[1], "site-packages/") && !strings.Contains(m[1], "dist-packages/") {
                        return stripContainerPath(m[1]), m[2]
                }
        }
        // Fall back to first Python match if all are site-packages
        if len(pythonMatches) > 0 && len(pythonMatches[0]) >= 3 {
                return stripContainerPath(pythonMatches[0][1]), pythonMatches[0][2]
        }
        // Try Java format: at Class.method(File.java:N)
        if m := javaFileRe.FindStringSubmatch(logs); len(m) >= 3 {
                return m[1], m[2]
        }

        // Prefer paths with a "/" prefix (user code like /app/db.js) over bare
        // module names (Node internals like net.js). Scan all matches and pick
        // the first one that has a path separator.
        nodeMatches := nodeFileRe.FindAllStringSubmatch(logs, -1)
        for _, m := range nodeMatches {
                if len(m) >= 3 && strings.Contains(m[1], "/") {
                        return stripContainerPath(m[1]), m[2]
                }
        }
        // Fall back to first Node.js match if no path-prefixed one exists.
        if len(nodeMatches) > 0 && len(nodeMatches[0]) >= 3 {
                return stripContainerPath(nodeMatches[0][1]), nodeMatches[0][2]
        }
        // Try generic format: file.ext:N — but only if the extension is a known
        // code extension. This prevents matching "127.0.0.1:5432" as file="0.1",
        // line="5432".
        for _, m := range genericFileRe.FindAllStringSubmatch(logs, -1) {
                if len(m) >= 3 && codeExtRe.MatchString(m[1]) {
                        return stripContainerPath(m[1]), m[2]
                }
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
//

// which meant "panic" + "panicked" logs were classified as Go even when the
// logs were Rust ("thread 'main' panicked at"). Rust's distinctive marker is
// the past-tense "panicked" (Go uses present-tense "panic:").
func detectLanguage(errorType, logs string) string {
        logsLower := strings.ToLower(logs)

        // Python: Traceback, .py files, Python-specific errors
        if strings.Contains(logsLower, "traceback") || strings.Contains(logs, ".py") ||
                errorType == "KeyError" || errorType == "IndentationError" || errorType == "SyntaxError" {
                return "python"
        }

        // Go uses "panic:" (present tense). .rs files are unambiguous.
        if strings.Contains(logsLower, "panicked") || strings.Contains(logs, ".rs") {
                return "rust"
        }
        // Go: panic (present tense), goroutine, .go files
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

// httpReqRe extracts HTTP request lines from pod logs. Matches patterns like:
//   GET /items?index=10 HTTP/1.1
//   POST /api/users HTTP/1.1
//   "GET /divide?a=10&b=0"
// Works across all web server log formats (uvicorn, gunicorn, express, gin, etc.)

// triggered the error.
var httpReqRe = regexp.MustCompile(`(?i)(?:GET|POST|PUT|DELETE|PATCH)\s+(/[^\s"]+)`)

// httpReqWithStatusRe extracts HTTP request lines WITH their status code.

// Matches formats like:
//   GET /path HTTP/1.1" 500
//   GET /path 500
//   "GET /path HTTP/1.1" 200
var httpReqWithStatusRe = regexp.MustCompile(`(?i)(?:GET|POST|PUT|DELETE|PATCH)\s+(/[^\s"]+).*?(\d{3})`)

// ExtractTriggerEndpoint pulls the HTTP request path that triggered the error
// from the pod logs. Returns "" if no HTTP request is found (non-web service).

// failing request) over 2xx requests (health checks, successful calls).
// Previously returned the FIRST request, which was often a health check.
//
// Example: logs containing 'GET /health 200 ... GET /items?index=10 500'
// now correctly returns "/items?index=10" (the 500), not "/health" (the 200).
func ExtractTriggerEndpoint(logs string) string {
        path, _ := ExtractTriggerEndpointWithMethod(logs)
        return path
}

// ExtractTriggerEndpointWithMethod returns both the path AND the HTTP method.

// always GET, returning 405 on POST endpoints which falsely passed Level 2
// (because 405 < 500 was treated as success).
// Also: filters to safe methods for replay — GET/HEAD only. POST/DELETE/PUT
// have side effects (data mutation, deletion) and are not safe to replay
// in the sandbox without explicit operator opt-in. If the only failing
// endpoint is a POST, we fall back to GET on the same path (which will likely
// return 405 = the route exists but method not allowed, which is now a real
// failure under the new Level 2 logic — so the LLM is told the route exists
// but isn't testable via GET, and must reason about the fix differently).
func ExtractTriggerEndpointWithMethod(logs string) (path, method string) {

        // ineffassign). The method is always set by extractMethod() before return,
        // or we return ("", "") if no match — callers check path == "" first.
        // First, find a request with a 4xx/5xx status code.
        matches := httpReqWithStatusRe.FindAllStringSubmatch(logs, -1)
        for _, m := range matches {
                if len(m) >= 3 {
                        status := m[2]
                        if len(status) == 3 && (status[0] == '4' || status[0] == '5') {
                                // Extract the method from the original match
                                method = extractMethod(m[0])
                                path = m[1]
                                return
                        }
                }
        }

        // Fall back to any HTTP request (no status code visible or all 2xx)
        m := httpReqRe.FindStringSubmatch(logs)
        if len(m) >= 2 {
                method = extractMethod(m[0])
                path = m[1]
                return
        }
        return "", ""
}

// extractMethod pulls the leading HTTP verb (GET/POST/...) from a log line.
func extractMethod(logLine string) string {
        lower := strings.ToLower(logLine)
        for _, verb := range []string{"get", "post", "put", "delete", "patch", "head"} {
                if strings.HasPrefix(lower, verb) {
                        return strings.ToUpper(verb)
                }
        }
        return "GET"
}

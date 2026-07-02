// Package errorparse provides shared error-detection patterns used by both
// the detection layer (fast log scanning) and the agents layer (rich parsing).
//
// Two consumers:
//   - internal/detection: uses IsErrorLine() for fast "does this look like
//     an error?" checks on every log line
//   - internal/agents: uses ParseError() for structured extraction
//     (error type, file, line, language) from stack traces
//
// The regexes here are the common set. Each consumer may have additional
// patterns specific to its use case.
package errorparse

import "regexp"

// ErrorTypeRegexes match the error-type prefix of a log line.
// Used by detection to quickly identify error lines.
var ErrorTypeRegexes = []*regexp.Regexp{
	regexp.MustCompile(`Traceback \(most recent call last\)`),
	regexp.MustCompile(`^\w+Error:`),
	regexp.MustCompile(`^\w+Exception:`),
	regexp.MustCompile(`panic:`),
	regexp.MustCompile(`fatal error:`),
	regexp.MustCompile(`goroutine \d+ \[running\]:`),
	regexp.MustCompile(`TypeError:`),
	regexp.MustCompile(`ReferenceError:`),
	regexp.MustCompile(`UnhandledPromiseRejection`),
	regexp.MustCompile(`SyntaxError:`),
	regexp.MustCompile(`java\.lang\.\w+Exception`),
	regexp.MustCompile(`NullPointerException`),
	regexp.MustCompile(`StackOverflowError`),
	regexp.MustCompile(`OutOfMemoryError`),
	regexp.MustCompile(`SIGABRT|SIGSEGV|SIGBUS`),
}

// ExtractTypePatterns extract the error type name from a log line.
// Returns the captured group (error type) or the fallback.
var ExtractTypePatterns = []struct {
	Re       *regexp.Regexp
	Fallback string
}{
	{regexp.MustCompile(`(\w+Error):`), ""},
	{regexp.MustCompile(`(\w+Exception):`), ""},
	{regexp.MustCompile(`panic:`), "panic"},
	{regexp.MustCompile(`fatal error:`), "fatal_error"},
	{regexp.MustCompile(`(\w+Error)\b`), ""},
	{regexp.MustCompile(`(\w+Exception)\b`), ""},
}

// IsErrorLine returns true if the line matches any known error pattern.
// This is the fast-path check used by the detection layer on every log line.
func IsErrorLine(line string) bool {
	for _, re := range ErrorTypeRegexes {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// ExtractErrorType returns the error type name from a line, or empty string.
// Used by detection to fingerprint + deduplicate errors.
func ExtractErrorType(line string) string {
	for _, p := range ExtractTypePatterns {
		m := p.Re.FindStringSubmatch(line)
		if m != nil {
			if len(m) > 1 && m[1] != "" {
				return m[1]
			}
			return p.Fallback
		}
	}
	return ""
}

package errorparse

import "regexp"

// ErrorTypeRegexes match the error-type prefix of a log line. The detection
// layer uses these for the fast "is this an error?" check.
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

// ExtractTypePatterns extract the error type name from a log line. When a
// pattern has a capture group, the captured value is returned; otherwise the
// Fallback string is returned.
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

// IsErrorLine reports whether the line matches any known error pattern. This
// is the fast-path scan the detection layer runs on every log line.
func IsErrorLine(line string) bool {
	for _, re := range ErrorTypeRegexes {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// ExtractErrorType returns the error type name from a line, or the empty
// string if no pattern matches. The detection layer uses this to fingerprint
// and deduplicate errors.
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

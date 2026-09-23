package errorparse

import "regexp"

// ErrorTypeRegexes match the error-type prefix of a log line. The detection
// layer uses these for the fast "is this an error?" check.
var ErrorTypeRegexes = []*regexp.Regexp{
	// Python
	regexp.MustCompile(`Traceback \(most recent call last\)`),
	regexp.MustCompile(`^\w+Error:`),
	regexp.MustCompile(`^\w+Exception:`),
	// Go
	regexp.MustCompile(`panic:`),
	regexp.MustCompile(`fatal error:`),
	regexp.MustCompile(`goroutine \d+ \[running\]:`),
	// Node.js / JavaScript
	regexp.MustCompile(`TypeError:`),
	regexp.MustCompile(`ReferenceError:`),
	regexp.MustCompile(`UnhandledPromiseRejection`),
	regexp.MustCompile(`SyntaxError:`),
	// Java
	regexp.MustCompile(`java\.lang\.\w+Exception`),
	regexp.MustCompile(`NullPointerException`),
	regexp.MustCompile(`StackOverflowError`),
	regexp.MustCompile(`OutOfMemoryError`),
	// Rust
	regexp.MustCompile(`thread '.*' panicked`),
	regexp.MustCompile(`panic!`),
	// Ruby
	regexp.MustCompile(`\.rb:\d+:.*Error`),
	// Generic / log-format
	regexp.MustCompile(`SIGABRT|SIGSEGV|SIGBUS`),
	regexp.MustCompile(`FATAL`),
	regexp.MustCompile(`SEGFAULT`),
	regexp.MustCompile(`(?i)segmentation fault`),
	regexp.MustCompile(`OOMKilled`),
	regexp.MustCompile(`^\[ERROR\]`),
	regexp.MustCompile(`level=error`),
	regexp.MustCompile(`level=fatal`),
	// Celery / Python worker log format:
	//   [2026-09-23 19:48:43,408: ERROR/ForkPoolWorker-4] task failed
	//   [2026-09-23 19:48:43,398: ERROR/MainProcess] ...
	regexp.MustCompile(`\bERROR/(MainProcess|ForkPoolWorker|SpawnPoolWorker|BilliardPool|prefork)`),
	// Python network / resolver failures logged mid-line (gaierror is
	// lowercase, so the anchored ^\w+Error: patterns never see it):
	//   err=gaierror: [Errno -2] Name or service not known
	regexp.MustCompile(`\bgaierror\b`),
	regexp.MustCompile(`Name or service not known`),
	regexp.MustCompile(`\bConnectionRefusedError\b`),
	regexp.MustCompile(`(?i)connection refused`),
	// Python structlog console renderer pads the level name:
	//   2026-09-23 19:48:58 [error    ] checkout failed
	regexp.MustCompile(`\[(error|critical|fatal)\s+\]`),
	// Ruby / Sidekiq job failures (Sidekiq logs exceptions at WARN):
	//   WARN: LockError: could not acquire lock 'OrderLock' within 5000ms
	regexp.MustCompile(`could not acquire lock`),
	regexp.MustCompile(`(?i)\bdeadlock\b`),
	regexp.MustCompile(`\bLockError\b`),
	// Spring Boot error lines (severity token between timestamp and
	// logger, e.g. "2026-... ERROR 1 --- [nio-8080-exec-7] ..."):
	regexp.MustCompile(` ERROR \d+ --- `),
	// Java exceptions outside java.lang (NoSuchElementException lives
	// in java.util):
	regexp.MustCompile(`java\.util\.\w+Exception`),
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
	{regexp.MustCompile(`java\.lang\.(\w+)`), ""},
	{regexp.MustCompile(`thread '.*' panicked`), "rust_panic"},
	// Fallbacks for formats the generic patterns cannot see (mid-line,
	// lowercase, or non-"\w+Error" shapes). Order matters: these run only
	// when every generic pattern above has missed the line.
	{regexp.MustCompile(`ERROR/(?:MainProcess|ForkPoolWorker|SpawnPoolWorker|BilliardPool|prefork)`), "celery_task_error"},
	{regexp.MustCompile(`\bgaierror\b`), "gaierror"},
	{regexp.MustCompile(`Name or service not known`), "dns_resolution_failed"},
	{regexp.MustCompile(`\bConnectionRefusedError\b`), "connection_refused"},
	{regexp.MustCompile(`Connection refused`), "connection_refused"},
	{regexp.MustCompile(`could not acquire lock`), "lock_acquire_timeout"},
	{regexp.MustCompile(`(?i)\bdeadlock\b`), "deadlock"},
	{regexp.MustCompile(`\[(?:error|critical|fatal)\s+\]`), "log_error_level"},
	{regexp.MustCompile(` ERROR \d+ --- `), "spring_error"},
	{regexp.MustCompile(`(?i)segmentation fault`), "segfault"},
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

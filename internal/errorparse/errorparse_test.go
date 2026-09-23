package errorparse

import "testing"

// TestIsErrorLine verifies the fast-path error detection against a variety
// of error prefixes from Python, Go, Java, JS, and signals.
func TestIsErrorLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Traceback (most recent call last):", true},
		{"KeyError: 'DATABASE_URL'", true},
		{"TypeError: can only concatenate str", true},
		{"panic: runtime error: index out of range", true},
		{"fatal error: unexpected signal", true},
		{"NullPointerException at com.acme.Foo", true},
		{"java.lang.NullPointerException", true},
		{"SIGSEGV: segmentation violation", true},
		{"ReferenceError: x is not defined", true},
		{"SyntaxError: invalid syntax", true},
		// Mid-line / worker-format errors (from the marketly eval):
		// Celery task failure.
		{"[2026-09-23 19:48:43,408: ERROR/ForkPoolWorker-4] send_email.failed id=notif-inj-341 err=gaierror: [Errno -2] Name or service not known", true},
		{"[2026-09-23 19:48:43,398: ERROR/MainProcess] Task app.tasks.send_email received", true},
		// Sidekiq lock timeout (logged at WARN by Sidekiq).
		{"2026-09-23T19:48:59.227Z pid=1 tid=2x1 WARN: LockError: could not acquire lock 'OrderLock' within 5000ms", true},
		// Ruby / Postgres deadlock wording.
		{"2026-09-23T19:48:59.227Z pid=1 tid=2x1 WARN: Job failed: deadlock detected", true},
		// Python structlog padded level name.
		{"2026-09-23 19:48:58 [error    ] checkout failed                customer=test@example.com error=Client error '409 Conflict'", true},
		// Spring Boot ERROR line (severity between timestamp + logger).
		{"2026-09-23T19:48:58.158Z  ERROR 1 --- [nio-8080-exec-7] o.a.c.c.C.[.[.[/].[dispatcherServlet] : Servlet.service() threw exception [java.util.NoSuchElementException: No value present]", true},
		{"java.util.NoSuchElementException: No value present", true},
		// DNS / connection failures mid-line.
		{"err=gaierror: [Errno -2] Name or service not known", true},
		{"dial tcp 10.0.0.5:5432: connect: connection refused", true},
		{"SMTP send failed: ConnectionRefusedError: [Errno 111] Connection refused", true},
		// Kernel-style lowercase segfault.
		{"Segmentation fault (core dumped)", true},
		// Non-errors must stay non-errors.
		{"  at Object.<anonymous> (/app/index.js:3:10)", false},
		{"INFO: application started", false},
		{"  File \"app.py\", line 42, in main", false},
		{"", false},
		// Structured INFO lines from the same formats must not fire.
		{"2026-09-23 19:48:58 [info     ] checkout started", false},
		{"[2026-09-23 19:48:43,398: INFO/MainProcess] Task received", false},
		{"2026-09-23T19:48:58.158Z  WARN 1 --- [nio-8080-exec-7] .w.s.m.s.DefaultHandlerExceptionResolver : Resolved [MethodArgumentNotValidException]", false},
		{"2026-09-23T19:48:59.227Z pid=1 tid=2x1 INFO: start", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := IsErrorLine(tt.line)
			if got != tt.want {
				t.Errorf("IsErrorLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestExtractErrorType verifies the error-type extraction used for
// fingerprinting + deduplication of similar log errors.
func TestExtractErrorType(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"KeyError: 'DATABASE_URL'", "KeyError"},
		{"TypeError: bad operand type", "TypeError"},
		{"panic: runtime error", "panic"},
		{"fatal error: unexpected", "fatal_error"},
		{"ValueError: invalid literal", "ValueError"},
		{"NullPointerException at foo", "NullPointerException"},
		// New formats: fallback extraction.
		{"[2026-09-23 19:48:43,408: ERROR/ForkPoolWorker-4] send_email.failed id=notif-inj-341 err=gaierror: [Errno -2] Name or service not known", "celery_task_error"},
		{"2026-09-23T19:48:59.227Z pid=1 tid=2x1 WARN: Job failed: deadlock detected", "deadlock"},
		{"2026-09-23 19:48:58 [error    ] checkout failed customer=test@example.com", "log_error_level"},
		{"2026-09-23T19:48:58.158Z  ERROR 1 --- [nio-8080-exec-7] o.a.c.c.C.[.[.[/].[dispatcherServlet] : Servlet.service() threw exception [java.util.NoSuchElementException: No value present]", "NoSuchElementException"},
		{"Segmentation fault (core dumped)", "segfault"},
		{"no error here", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := ExtractErrorType(tt.line)
			if got != tt.want {
				t.Errorf("ExtractErrorType(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

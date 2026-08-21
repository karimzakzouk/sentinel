#!/usr/bin/env python3
"""Apply Tier 1 fixes to wire.go, detector.go, errorparse.go, and web/out/index.html
while preserving tab indentation (the Edit tool converts tabs to spaces)."""
import re
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")

# --- Fix 5: wire.go — remove discarded NewLokiClient call ---
wire_path = REPO / "cmd/sentinel/wire.go"
wire = wire_path.read_text()
old = """\tif cfg.LokiURL != "" {
\t\t_ = observability.NewLokiClient(cfg.LokiURL, "")
\t\tlogging.Info("observability: loki client initialized", "url", cfg.LokiURL)
\t}"""
new = """\tif cfg.LokiURL != "" {
\t\tlogging.Info("observability: loki URL configured (client not yet wired — see Tier 2 TODO)",
\t\t\t"url", cfg.LokiURL)
\t}"""
assert old in wire, "wire.go: old text not found"
wire = wire.replace(old, new, 1)
wire_path.write_text(wire)
print("OK: wire.go")

# --- Fix 6a: errorparse.go — add Rust/Ruby/generic patterns ---
ep_path = REPO / "internal/errorparse/errorparse.go"
ep = ep_path.read_text()

old_regexes = """var ErrorTypeRegexes = []*regexp.Regexp{
\tregexp.MustCompile(`Traceback \\(most recent call last\\)`),
\tregexp.MustCompile(`^\\w+Error:`),
\tregexp.MustCompile(`^\\w+Exception:`),
\tregexp.MustCompile(`panic:`),
\tregexp.MustCompile(`fatal error:`),
\tregexp.MustCompile(`goroutine \\d+ \\[running\\]:`),
\tregexp.MustCompile(`TypeError:`),
\tregexp.MustCompile(`ReferenceError:`),
\tregexp.MustCompile(`UnhandledPromiseRejection`),
\tregexp.MustCompile(`SyntaxError:`),
\tregexp.MustCompile(`java\\.lang\\.\\w+Exception`),
\tregexp.MustCompile(`NullPointerException`),
\tregexp.MustCompile(`StackOverflowError`),
\tregexp.MustCompile(`OutOfMemoryError`),
\tregexp.MustCompile(`SIGABRT|SIGSEGV|SIGBUS`),
}"""

new_regexes = """var ErrorTypeRegexes = []*regexp.Regexp{
\t// Python
\tregexp.MustCompile(`Traceback \\(most recent call last\\)`),
\tregexp.MustCompile(`^\\w+Error:`),
\tregexp.MustCompile(`^\\w+Exception:`),
\t// Go
\tregexp.MustCompile(`panic:`),
\tregexp.MustCompile(`fatal error:`),
\tregexp.MustCompile(`goroutine \\d+ \\[running\\]:`),
\t// Node.js / JavaScript
\tregexp.MustCompile(`TypeError:`),
\tregexp.MustCompile(`ReferenceError:`),
\tregexp.MustCompile(`UnhandledPromiseRejection`),
\tregexp.MustCompile(`SyntaxError:`),
\t// Java
\tregexp.MustCompile(`java\\.lang\\.\\w+Exception`),
\tregexp.MustCompile(`NullPointerException`),
\tregexp.MustCompile(`StackOverflowError`),
\tregexp.MustCompile(`OutOfMemoryError`),
\t// Rust
\tregexp.MustCompile(`thread '.*' panicked`),
\tregexp.MustCompile(`panic!`),
\t// Ruby
\tregexp.MustCompile(`\\.rb:\\d+:.*Error`),
\t// Generic / log-format
\tregexp.MustCompile(`SIGABRT|SIGSEGV|SIGBUS`),
\tregexp.MustCompile(`FATAL`),
\tregexp.MustCompile(`SEGFAULT`),
\tregexp.MustCompile(`OOMKilled`),
\tregexp.MustCompile(`^\\[ERROR\\]`),
\tregexp.MustCompile(`level=error`),
\tregexp.MustCompile(`level=fatal`),
}"""

assert old_regexes in ep, "errorparse.go: old regexes not found"
ep = ep.replace(old_regexes, new_regexes, 1)

old_extracts = """var ExtractTypePatterns = []struct {
\tRe       *regexp.Regexp
\tFallback string
}{
\t{regexp.MustCompile(`(\\w+Error):`), ""},
\t{regexp.MustCompile(`(\\w+Exception):`), ""},
\t{regexp.MustCompile(`panic:`), "panic"},
\t{regexp.MustCompile(`fatal error:`), "fatal_error"},
\t{regexp.MustCompile(`(\\w+Error)\\b`), ""},
\t{regexp.MustCompile(`(\\w+Exception)\\b`), ""},
}"""

new_extracts = """var ExtractTypePatterns = []struct {
\tRe       *regexp.Regexp
\tFallback string
}{
\t{regexp.MustCompile(`(\\w+Error):`), ""},
\t{regexp.MustCompile(`(\\w+Exception):`), ""},
\t{regexp.MustCompile(`panic:`), "panic"},
\t{regexp.MustCompile(`fatal error:`), "fatal_error"},
\t{regexp.MustCompile(`(\\w+Error)\\b`), ""},
\t{regexp.MustCompile(`(\\w+Exception)\\b`), ""},
\t{regexp.MustCompile(`java\\.lang\\.(\\w+)`), ""},
\t{regexp.MustCompile(`thread '.*' panicked`), "rust_panic"},
}"""

assert old_extracts in ep, "errorparse.go: old extracts not found"
ep = ep.replace(old_extracts, new_extracts, 1)
ep_path.write_text(ep)
print("OK: errorparse.go")

# --- Fix 6b: detector.go — remove duplicate patterns, delegate to errorparse ---
det_path = REPO / "internal/detection/detector.go"
det = det_path.read_text()

# Replace import block: remove "regexp", add errorparse
old_imports = """import (
\t"bufio"
\t"context"
\t"fmt"
\t"regexp"
\t"strings"
\t"sync"
\t"time"

\t"github.com/karimzakzouk/sentinel/internal/logging"

\tcorev1 "k8s.io/api/core/v1"
\tapierrors "k8s.io/apimachinery/pkg/api/errors"
\tmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
\t"k8s.io/apimachinery/pkg/watch"
\t"k8s.io/client-go/kubernetes"
)"""

new_imports = """import (
\t"bufio"
\t"context"
\t"fmt"
\t"strings"
\t"sync"
\t"time"

\t"github.com/karimzakzouk/sentinel/internal/errorparse"
\t"github.com/karimzakzouk/sentinel/internal/logging"

\tcorev1 "k8s.io/api/core/v1"
\tapierrors "k8s.io/apimachinery/pkg/api/errors"
\tmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
\t"k8s.io/apimachinery/pkg/watch"
\t"k8s.io/client-go/kubernetes"
)"""

assert old_imports in det, "detector.go: old imports not found"
det = det.replace(old_imports, new_imports, 1)

# Remove the errorPatterns and extractErrorTypePatterns vars
old_vars = """// errorPatterns matches error lines across languages. Pre-compiled at init
// because the detection layer scans every line of every streaming pod log
// against every pattern — re-compiling per call is measurable CPU waste on
// chatty pods.
var errorPatterns = []*regexp.Regexp{
\t// Python
\tregexp.MustCompile(`Traceback \\(most recent call last\\)`),
\tregexp.MustCompile(`^\\w+Error:`),
\tregexp.MustCompile(`^\\w+Exception:`),
\t// Go
\tregexp.MustCompile(`panic:`),
\tregexp.MustCompile(`fatal error:`),
\tregexp.MustCompile(`goroutine \\d+ \\[running\\]:`),
\t// Node.js / JavaScript
\tregexp.MustCompile(`TypeError:`),
\tregexp.MustCompile(`ReferenceError:`),
\tregexp.MustCompile(`UnhandledPromiseRejection`),
\tregexp.MustCompile(`SyntaxError:`),
\t// Java
\tregexp.MustCompile(`java\\.lang\\.\\w+Exception`),
\tregexp.MustCompile(`NullPointerException`),
\tregexp.MustCompile(`StackOverflowError`),
\t// Rust
\tregexp.MustCompile(`thread '.*' panicked`),
\tregexp.MustCompile(`panic!`),
\t// Ruby
\tregexp.MustCompile(`\\.rb:\\d+:.*Error`),
\t// Generic
\tregexp.MustCompile(`FATAL`),
\tregexp.MustCompile(`SEGFAULT`),
\tregexp.MustCompile(`OOMKilled`),
\tregexp.MustCompile(`^\\[ERROR\\]`),
\tregexp.MustCompile(`level=error`),
\tregexp.MustCompile(`level=fatal`),
}

// extractErrorTypePatterns extract the error-type name from a log line.
// fallback is returned when the regex matches but has no capture group
// (e.g. "panic:" matches but doesn't capture a name).
var extractErrorTypePatterns = []struct {
\tre       *regexp.Regexp
\tfallback string
}{
\t{regexp.MustCompile(`(\\w+Error):`), ""},
\t{regexp.MustCompile(`(\\w+Exception):`), ""},
\t{regexp.MustCompile(`panic:`), "panic"},
\t{regexp.MustCompile(`fatal error:`), "fatal_error"},
\t{regexp.MustCompile(`(\\w+Error)\\s`), ""},
\t{regexp.MustCompile(`java\\.lang\\.(\\w+)`), ""},
\t{regexp.MustCompile(`thread '.*' panicked`), "rust_panic"},
}

// ErrorFingerprint uniquely identifies an error for deduplication."""

new_vars = """// ErrorFingerprint uniquely identifies an error for deduplication."""

assert old_vars in det, "detector.go: old vars block not found"
det = det.replace(old_vars, new_vars, 1)

# Replace the isErrorLine + extractErrorType methods
old_methods = """// isErrorLine reports whether the line matches any known error pattern.
func (d *Detector) isErrorLine(line string) bool {
\tfor _, p := range errorPatterns {
\t\tif p.MatchString(line) {
\t\t\treturn true
\t\t}
\t}
\treturn false
}

// extractErrorType returns the error-type name from a line, or
// "unknown_error" if no pattern matches.
func (d *Detector) extractErrorType(line string) string {
\tfor _, p := range extractErrorTypePatterns {
\t\tif m := p.re.FindStringSubmatch(line); len(m) > 1 {
\t\t\treturn m[1]
\t\t} else if p.re.MatchString(line) {
\t\t\tif p.fallback != "" {
\t\t\t\treturn p.fallback
\t\t\t}
\t\t\treturn "error"
\t\t}
\t}
\treturn "unknown_error"
}"""

new_methods = """// isErrorLine reports whether the line matches any known error pattern.
// Delegates to the shared errorparse package to avoid pattern drift.
func (d *Detector) isErrorLine(line string) bool {
\treturn errorparse.IsErrorLine(line)
}

// extractErrorType returns the error-type name from a line, or
// "unknown_error" if no pattern matches.
func (d *Detector) extractErrorType(line string) string {
\tif t := errorparse.ExtractErrorType(line); t != "" {
\t\treturn t
\t}
\treturn "unknown_error"
}"""

assert old_methods in det, "detector.go: old methods not found"
det = det.replace(old_methods, new_methods, 1)
det_path.write_text(det)
print("OK: detector.go")

# --- Fix 7: web/out/index.html placeholder ---
out_path = REPO / "internal/web/out/index.html"
out_path.write_text("""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sentinel</title>
  <style>
    body { background:#09090B; color:#A1A1AA; font-family:ui-monospace,SFMono-Regular,monospace; padding:40px; max-width:600px; margin:0 auto; }
    h1 { color:#06B6D4; font-size:1.5rem; }
    code { background:#27272A; padding:2px 6px; border-radius:4px; color:#FAFAFA; }
  </style>
</head>
<body>
  <h1>Sentinel</h1>
  <p>Web UI not built. Run <code>make web &amp;&amp; make build</code> to include the dashboard.</p>
  <p>API endpoints: <code>/healthz</code>, <code>/incidents</code>, <code>/metrics</code></p>
</body>
</html>
""")
print("OK: internal/web/out/index.html")

print("\n=== All fixes applied with tab preservation ===")

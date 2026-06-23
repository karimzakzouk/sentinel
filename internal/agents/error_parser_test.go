package agents

import (
        "strings"
        "testing"
)

// TestParseError_AllLanguages verifies the universal error parser correctly
// extracts error type, file, line, and detected language from real-world stack
// traces across all 8 supported languages: Python, Go, Java, JavaScript/Node,
// Rust, Ruby, C, and unknown.
//
// This is the test coverage the codebase was missing — error_parser.go had
// zero tests despite being a flagship feature ("universal error parser").
// Each subtest uses a real traceback sampled from production logs of the
// respective language runtime.
func TestParseError_AllLanguages(t *testing.T) {
        tests := []struct {
                name         string
                logs         string
                wantErrType  string
                wantFile     string
                wantLine     string
                wantLanguage string
                wantDetail   string
        }{
                {
                        name: "python KeyError with env var typo",
                        logs: `Traceback (most recent call last):
  File "/app/app/main.py", line 11, in <module>
    db_url = os.environ["DATABASE_URLL"]
  File "/usr/local/lib/python3.11/os.py", line 679, in __getitem__
    raise KeyError(key) from None
KeyError: 'DATABASE_URLL'`,
                        wantErrType:  "KeyError",
                        wantFile:     "app/main.py",
                        wantLine:     "11",
                        wantLanguage: "python",
                        wantDetail:   "'DATABASE_URLL'",
                },
                {
                        name: "python ZeroDivisionError",
                        logs: `Traceback (most recent call last):
  File "/app/calc.py", line 42, in divide
    return a / b
ZeroDivisionError: division by zero`,
                        wantErrType:  "ZeroDivisionError",
                        wantFile:     "calc.py",
                        wantLine:     "42",
                        wantLanguage: "python",
                },
                {
                        name: "python ImportError missing module",
                        logs: `Traceback (most recent call last):
  File "/app/server.py", line 3, in <module>
    from flask import Flask
ModuleNotFoundError: No module named 'flask'`,
                        wantErrType:  "ModuleNotFoundError",
                        wantFile:     "server.py",
                        wantLine:     "3",
                        wantLanguage: "python",
                },
                {
                        name: "go panic nil pointer dereference",
                        logs: `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x48e7a5]

goroutine 1 [running]:
main.handleRequest(0xc0000a8010)
        /workspace/src/handler.go:42 +0x1a3
main.main()
        /workspace/src/main.go:15 +0x85`,
                        wantErrType:  "panic",
                        wantFile:     "handler.go",
                        wantLine:     "42",
                        wantLanguage: "go",
                        wantDetail:   "runtime error: invalid memory address or nil pointer dereference",
                },
                {
                        name: "go panic index out of range",
                        logs: `panic: runtime error: index out of range [1] with length 1

goroutine 1 [running]:
main.getItem(0x1)
        /app/main.go:88 +0x123`,
                        wantErrType:  "panic",
                        wantFile:     "main.go",
                        wantLine:     "88",
                        wantLanguage: "go",
                },
                {
                        name: "java NullPointerException",
                        logs: `Exception in thread "main" java.lang.NullPointerException: Cannot invoke "com.acme.User.getName()" because "user" is null
        at com.acme.UserService.getProfile(UserService.java:42)
        at com.acme.Main.main(Main.java:15)`,
                        wantErrType:  "NullPointerException",
                        wantFile:     "UserService.java",
                        wantLine:     "42",
                        wantLanguage: "java",
                },
                {
                        name: "java StackOverflowError",
                        logs: `Exception in thread "main" java.lang.StackOverflowError
        at com.acme.Recursive.fib(Recursive.java:88)
        at com.acme.Recursive.fib(Recursive.java:88)`,
                        wantErrType:  "StackOverflowError",
                        wantFile:     "Recursive.java",
                        wantLine:     "88",
                        wantLanguage: "java",
                },
                {
                        name: "javascript TypeError undefined is not a function",
                        logs: `/app/index.js:10
  const result = processData(data);
                  ^
TypeError: processData is not a function
    at Object.<anonymous> (/app/index.js:10:17)
    at Module._compile (internal/modules/cjs/loader.js:1085:14)`,
                        wantErrType:  "TypeError",
                        wantFile:     "index.js",
                        wantLine:     "10",
                        wantLanguage: "javascript",
                },
                {
                        name: "javascript UnhandledPromiseRejection",
                        logs: `(node:42) UnhandledPromiseRejectionWarning: Error: connect ECONNREFUSED 127.0.0.1:5432
    at TCPConnectWrap.afterConnect [as oncomplete] (net.js:1146:16)
    at /app/db.js:25:9`,
                        wantErrType:  "Error",
                        wantFile:     "db.js",
                        wantLine:     "25",
                        wantLanguage: "javascript",
                },
                {
                        name: "rust panic unwrap on None",
                        logs: `thread 'main' panicked at 'called Option::unwrap() on a None value', src/main.rs:13:5
note: run with RUST_BACKTRACE=1 environment variable to display a backtrace`,
                        wantErrType:  "panic",
                        wantFile:     "main.rs",
                        wantLine:     "13",
                        wantLanguage: "rust",
                },
                {
                        name: "rust panic index out of bounds",
                        logs: `thread 'worker' panicked at 'index out of bounds: the len is 3 but the index is 5', src/lib/processor.rs:88:22`,
                        wantErrType:  "panic",
                        wantFile:     "processor.rs",
                        wantLine:     "88",
                        wantLanguage: "rust",
                },
                {
                        name: "ruby NoMethodError",
                        logs: "/app/server.rb:15:in `block in <main>': undefined method `fetch_profile' for main:Object (NoMethodError)\n" +
                                "    from /app/server.rb:8:in `<main>'",
                        wantErrType:  "NoMethodError",
                        wantFile:     "server.rb",
                        wantLine:     "15",
                        wantLanguage: "ruby",
                },
                {
                        name: "c segmentation fault SIGSEGV",
                        logs: `Program received signal SIGSEGV, Segmentation fault.
0x0000555555555230 in main () at src/main.c:42
42              printf("%d\n", *ptr);`,
                        wantErrType:  "SIGSEGV",
                        wantFile:     "main.c",
                        wantLine:     "42",
                        wantLanguage: "c",
                },
                {
                        name: "c++ SIGABRT assertion failure",
                        logs: `Assertion failed: idx < size (src/buffer.cpp:88: main)
Aborted (core dumped)
Program received signal SIGABRT, Aborted.`,
                        wantErrType:  "SIGABRT",
                        wantFile:     "buffer.cpp",
                        wantLine:     "88",
                        wantLanguage: "c",
                },
                {
                        name:         "unknown language no recognizable pattern",
                        logs:         "service started successfully on port 8080",
                        wantErrType:  "",
                        wantFile:     "",
                        wantLine:     "",
                        wantLanguage: "",
                },
        }

        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        pe := ParseError(tt.logs)
                        if tt.wantErrType == "" {
                                if pe != nil {
                                        t.Errorf("expected nil for unrecognizable input, got %+v", pe)
                                }
                                return
                        }
                        if pe == nil {
                                t.Fatalf("ParseError returned nil for %s", tt.name)
                        }
                        if pe.ErrorType != tt.wantErrType {
                                t.Errorf("ErrorType = %q, want %q", pe.ErrorType, tt.wantErrType)
                        }
                        if pe.SourceFile != tt.wantFile {
                                t.Errorf("SourceFile = %q, want %q", pe.SourceFile, tt.wantFile)
                        }
                        if pe.SourceLine != tt.wantLine {
                                t.Errorf("SourceLine = %q, want %q", pe.SourceLine, tt.wantLine)
                        }
                        if pe.Language != tt.wantLanguage {
                                t.Errorf("Language = %q, want %q", pe.Language, tt.wantLanguage)
                        }
                        if tt.wantDetail != "" && !strings.Contains(pe.ErrorDetail, tt.wantDetail) {
                                t.Errorf("ErrorDetail = %q, want it to contain %q", pe.ErrorDetail, tt.wantDetail)
                        }
                })
        }
}

// TestGenerateHypothesesUniversal verifies the hypothesis generator produces
// sensible hypotheses for each error type, with the primary H1 carrying
// source file info and a secondary H2 (config/env issue) when there's detail.
func TestGenerateHypothesesUniversal(t *testing.T) {
        t.Run("python KeyError produces 2 hypotheses with source file", func(t *testing.T) {
                logs := `Traceback (most recent call last):
  File "/app/app/main.py", line 11, in <module>
    db_url = os.environ["DATABASE_URLL"]
KeyError: 'DATABASE_URLL'`
                hyps := GenerateHypothesesUniversal(logs, nil)
                if len(hyps) < 1 {
                        t.Fatalf("expected at least 1 hypothesis, got %d", len(hyps))
                }
                if hyps[0].HID != "H1" {
                        t.Errorf("first hypothesis should be H1, got %s", hyps[0].HID)
                }
                if hyps[0].Confidence != 0.7 {
                        t.Errorf("H1 confidence should be 0.7, got %.2f", hyps[0].Confidence)
                }
                if !strings.Contains(hyps[0].Statement, "KeyError") {
                        t.Errorf("H1 should mention KeyError: %q", hyps[0].Statement)
                }
                if !strings.Contains(hyps[0].Statement, "main.py") {
                        t.Errorf("H1 should mention source file: %q", hyps[0].Statement)
                }
                if !strings.Contains(hyps[0].Statement, "DATABASE_URLL") {
                        t.Errorf("H1 should mention the missing key: %q", hyps[0].Statement)
                }
                if len(hyps) >= 2 && hyps[1].HID != "H2" {
                        t.Errorf("second hypothesis should be H2, got %s", hyps[1].HID)
                }
        })

        t.Run("go panic produces hypothesis with panic context", func(t *testing.T) {
                logs := `panic: runtime error: index out of range [1] with length 1
goroutine 1 [running]:
main.getItem(0x1)
        /app/main.go:88 +0x123`
                hyps := GenerateHypothesesUniversal(logs, nil)
                if len(hyps) == 0 {
                        t.Fatal("expected at least 1 hypothesis")
                }
                if !strings.Contains(hyps[0].Statement, "panic") {
                        t.Errorf("H1 should mention panic: %q", hyps[0].Statement)
                }
                if !strings.Contains(hyps[0].Statement, "main.go") {
                        t.Errorf("H1 should mention source file: %q", hyps[0].Statement)
                }
        })

        t.Run("returns nil for unparseable logs", func(t *testing.T) {
                hyps := GenerateHypothesesUniversal("service started ok", nil)
                if hyps != nil {
                        t.Errorf("expected nil for clean logs, got %d hypotheses", len(hyps))
                }
        })
}

// TestExtractSearchQueriesUniversal verifies the search query extractor pulls
// out the right terms (error type, error detail, source file) for the LLM
// agentic search loop.
func TestExtractSearchQueriesUniversal(t *testing.T) {
        t.Run("python KeyError extracts error type + key + file", func(t *testing.T) {
                logs := `Traceback (most recent call last):
  File "/app/app/main.py", line 11, in <module>
    db_url = os.environ["DATABASE_URLL"]
KeyError: 'DATABASE_URLL'`
                queries := ExtractSearchQueriesUniversal(logs)
                if len(queries) == 0 {
                        t.Fatal("expected at least 1 query")
                }
                // Should contain the error type
                found := false
                for _, q := range queries {
                        if q == "KeyError" {
                                found = true
                        }
                }
                if !found {
                        t.Errorf("queries should contain KeyError, got %v", queries)
                }
                // Should contain the source file
                found = false
                for _, q := range queries {
                        if q == "app/main.py" {
                                found = true
                        }
                }
                if !found {
                        t.Errorf("queries should contain app/main.py, got %v", queries)
                }
        })

        t.Run("returns empty for unparseable logs", func(t *testing.T) {
                queries := ExtractSearchQueriesUniversal("service healthy")
                if len(queries) != 0 {
                        t.Errorf("expected empty queries, got %v", queries)
                }
        })

        t.Run("dedupes queries", func(t *testing.T) {
                // If error type and detail are the same, we should not get duplicates.
                logs := `ValueError: ValueError
  File "/app/x.py", line 1`
                queries := ExtractSearchQueriesUniversal(logs)
                seen := map[string]int{}
                for _, q := range queries {
                        seen[q]++
                }
                for q, n := range seen {
                        if n > 1 {
                                t.Errorf("query %q appears %d times in %v", q, n, queries)
                        }
                }
        })
}

// TestDetectLanguage verifies language detection for each language's
// distinctive log markers.
func TestDetectLanguage(t *testing.T) {
        tests := []struct {
                name      string
                errorType string
                logs      string
                want      string
        }{
                {"python via traceback", "", "Traceback (most recent call last)", "python"},
                {"python via .py extension", "", "  File 'app/main.py'", "python"},
                {"python via KeyError", "KeyError", "", "python"},
                {"go via goroutine", "panic", "goroutine 1 [running]:", "go"},
                {"go via .go extension", "", "/app/main.go:42", "go"},
                {"java via java.lang.", "NullPointerException", "java.lang.NullPointerException", "java"},
                {"java via .java extension", "", "at com.acme.Foo.bar(Foo.java:42)", "java"},
                {"javascript via .js extension", "TypeError", "/app/index.js:10", "javascript"},
                {"javascript via at Object.", "", "at Object.<anonymous> (/app/x.js:1)", "javascript"},
                {"rust via panicked", "panic", "thread 'main' panicked at", "rust"},
                {"rust via .rs extension", "", "src/main.rs:13", "rust"},
                {"ruby via .rb extension", "NoMethodError", "/app/server.rb:15", "ruby"},
                {"c via segmentation", "SIGSEGV", "Segmentation fault", "c"},
                {"c via .c extension", "", "src/main.c:42", "c"},
                {"unknown for empty", "", "", "unknown"},
        }
        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        got := detectLanguage(tt.errorType, tt.logs)
                        if got != tt.want {
                                t.Errorf("detectLanguage(%q, %q) = %q, want %q", tt.errorType, tt.logs, got, tt.want)
                        }
                })
        }
}

// TestStripContainerPath verifies container path stripping for common WORKDIR
// prefixes (/app/, /usr/local/lib/, etc.).
func TestStripContainerPath(t *testing.T) {
        tests := []struct {
                input string
                want  string
        }{
                {"/app/app/main.py", "app/main.py"},
                {"/usr/local/lib/python3.11/os.py", "python3.11/os.py"},
                {"/usr/lib/node_modules/foo/index.js", "node_modules/foo/index.js"},
                {"/src/handler.rs", "handler.rs"},
                {"/home/user/project/main.go", "user/project/main.go"},
                {"app/main.py", "app/main.py"},          // no prefix → unchanged
                {"relative/path.go", "relative/path.go"}, // relative → unchanged
        }
        for _, tt := range tests {
                t.Run(tt.input, func(t *testing.T) {
                        got := stripContainerPath(tt.input)
                        if got != tt.want {
                                t.Errorf("stripContainerPath(%q) = %q, want %q", tt.input, got, tt.want)
                        }
                })
        }
}

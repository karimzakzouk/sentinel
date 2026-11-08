package logs

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
)

// Exception is a stack trace parsed from a record.
type Exception struct {
	Language string `json:"language"`
	// Types lists exception types from the outermost to the root cause.
	Types []string `json:"types"`
	// Frames are the root cause's frames, innermost first, as module and
	// function without line numbers or argument values.
	Frames []Frame `json:"frames,omitempty"`
	// Fingerprint groups occurrences of the same error: types and in-app
	// frames, hashed, under a versioned algorithm.
	Fingerprint string `json:"fingerprint"`
}

// Frame is one call in a stack trace.
type Frame struct {
	Function string `json:"function"`
	// InApp marks the application's own code, as opposed to the runtime,
	// the standard library and dependencies.
	InApp bool `json:"inApp,omitempty"`
}

// FingerprintVersion names the fingerprint algorithm. Changing what goes
// into a fingerprint requires a new version, or old and new occurrences of
// one error would stop grouping.
const FingerprintVersion = "fp1"

const maxFrames = 20

// ParseException recognizes a stack trace in a record.
func ParseException(r Record) (Exception, bool) {
	if len(r.Lines) == 0 {
		return Exception{}, false
	}
	first := r.Lines[0]
	var e Exception
	switch {
	case hasAnyPrefix(first, "panic: ", "fatal error: "):
		e = parseGo(r.Lines)
	case strings.HasPrefix(first, "Traceback (most recent call last):"):
		e = parsePython(r.Lines)
	case hasAnyPrefix(first, "Unhandled exception. ", "Unhandled Exception: "):
		e = parseDotnet(r.Lines)
	case strings.HasPrefix(first, "Exception in thread "), javaThrowable(first):
		e = parseJava(r.Lines)
	case jsError(first):
		e = parseNode(r.Lines)
	case len(r.Lines) > 1 && strings.HasPrefix(strings.TrimLeft(r.Lines[1], " \t"), "at "):
		// A logged exception: an entry's message, then frames.
		e = parseLogged(r.Lines)
	default:
		return Exception{}, false
	}
	if len(e.Types) == 0 {
		return Exception{}, false
	}
	if len(e.Frames) > maxFrames {
		e.Frames = e.Frames[:maxFrames]
	}
	e.Fingerprint = fingerprint(e, first)
	return e, true
}

func fingerprint(e Exception, header string) string {
	var frames []string
	for _, f := range e.Frames {
		if f.InApp {
			frames = append(frames, f.Function)
		}
	}
	if len(frames) == 0 {
		for _, f := range e.Frames {
			frames = append(frames, f.Function)
		}
	}
	frames = frames[:min(len(frames), 5)]
	parts := []string{FingerprintVersion, e.Language, strings.Join(e.Types, ">"), strings.Join(frames, ";")}
	if len(frames) == 0 {
		// Without frames, the message is all that tells errors apart.
		parts = append(parts, Mask(header))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return FingerprintVersion + "-" + hex.EncodeToString(sum[:8])
}

// Go: "panic: message", then the panicking goroutine's function lines, each
// followed by an indented file line.
func parseGo(lines []string) Exception {
	head := lines[0]
	typ := Mask(strings.TrimPrefix(head, "panic: "))
	if strings.HasPrefix(head, "fatal error: ") {
		typ = Mask(head)
	}
	e := Exception{Language: "go", Types: []string{typ}}
	goroutines := 0
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, "goroutine "):
			goroutines++
		case goroutines == 1 && l != "" && !indented(l) && !hasAnyPrefix(l, "created by ", "exit status", "[signal"):
			fn := l
			if i := strings.LastIndexByte(l, '('); i > 0 && strings.HasSuffix(l, ")") {
				fn = l[:i]
			}
			e.Frames = append(e.Frames, Frame{Function: fn, InApp: goInApp(fn)})
		}
	}
	return e
}

// goInApp treats package main and module paths outside well-known
// dependencies as the application; the standard library has no dot in its
// first path element.
func goInApp(fn string) bool {
	pkg := fn
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		pkg = fn[:i+strings.IndexByte(fn[i:], '.')]
	} else if i := strings.IndexByte(fn, '.'); i >= 0 {
		pkg = fn[:i]
	}
	if pkg == "main" {
		return true
	}
	first, _, _ := strings.Cut(pkg, "/")
	return strings.Contains(first, ".") &&
		!hasAnyPrefix(pkg, "golang.org/", "google.golang.org/", "k8s.io/", "sigs.k8s.io/", "github.com/golang/", "go.opentelemetry.io/")
}

// Java: a header per exception in the chain ("Exception in thread", "Caused
// by:"), each followed by "at" frames.
func parseJava(lines []string) Exception {
	e := Exception{Language: "java"}
	var frames []Frame
	for _, l := range lines {
		t := strings.TrimLeft(l, " \t")
		switch {
		case strings.HasPrefix(t, "at "):
			frames = append(frames, javaFrame(t[3:]))
		case strings.HasPrefix(t, "..."):
		default:
			h := t
			for _, p := range []string{"Caused by: ", "Suppressed: "} {
				h = strings.TrimPrefix(h, p)
			}
			if strings.HasPrefix(h, "Exception in thread ") {
				if i := strings.LastIndex(h, `" `); i > 0 {
					h = h[i+2:]
				}
			}
			name, _, _ := strings.Cut(h, ": ")
			if javaThrowable(name) || strings.Contains(name, ".") && !strings.Contains(name, " ") {
				e.Types = append(e.Types, name)
				frames = nil
			}
		}
	}
	e.Frames = frames
	return e
}

func javaFrame(s string) Frame {
	fn, _, _ := strings.Cut(s, "(")
	if i := strings.IndexByte(fn, '/'); i >= 0 {
		fn = fn[i+1:] // module prefix: java.base/java.lang.Thread.run
	}
	return Frame{Function: fn, InApp: !hasAnyPrefix(fn, "java.", "javax.", "jdk.", "sun.", "com.sun.", "kotlin.",
		"kotlinx.", "scala.", "org.springframework.", "org.apache.", "io.netty.", "reactor.", "io.grpc.",
		"org.hibernate.", "com.fasterxml.", "org.junit.", "io.micrometer.", "ch.qos.logback.", "org.slf4j.")}
}

// Python: "Traceback" blocks of File lines, outermost first, each ending in
// the exception line.
func parsePython(lines []string) Exception {
	e := Exception{Language: "python"}
	var frames []Frame
	for _, l := range lines {
		t := strings.TrimLeft(l, " ")
		switch {
		case strings.HasPrefix(l, "Traceback "):
			frames = nil
		case strings.HasPrefix(t, `File "`):
			frames = append(frames, pythonFrame(t))
		case l != "" && !indented(l) && !pythonChain(l):
			name, _, _ := strings.Cut(l, ":")
			e.Types = append(e.Types, name)
		}
	}
	for i := len(frames) - 1; i >= 0; i-- {
		e.Frames = append(e.Frames, frames[i])
	}
	return e
}

// pythonFrame parses `File "/app/main.py", line 6, in main`.
func pythonFrame(t string) Frame {
	rest := strings.TrimPrefix(t, `File "`)
	file, rest, _ := strings.Cut(rest, `"`)
	_, fn, _ := strings.Cut(rest, ", in ")
	module := strings.TrimSuffix(path.Base(file), ".py")
	inApp := !strings.Contains(file, "site-packages") && !strings.Contains(file, "dist-packages") &&
		!strings.Contains(file, "/lib/python") && !strings.HasPrefix(file, "<")
	return Frame{Function: module + "." + fn, InApp: inApp}
}

// Node.js: "TypeError: message", then "at function (file:line:column)".
func parseNode(lines []string) Exception {
	name, _, _ := strings.Cut(lines[0], ": ")
	e := Exception{Language: "node", Types: []string{name}}
	for _, l := range lines[1:] {
		t := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(t, "at ") {
			e.Frames = append(e.Frames, nodeFrame(t[3:]))
		}
	}
	return e
}

func nodeFrame(s string) Frame {
	fn, loc := "<anonymous>", s
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		fn, loc = s[:i], s[i+2:len(s)-1]
		if j := strings.Index(fn, " [as "); j > 0 {
			fn = fn[:j]
		}
	}
	file := loc
	for range 2 { // drop :line:column
		if i := strings.LastIndexByte(file, ':'); i > 0 && digits(file[i+1:]) {
			file = file[:i]
		}
	}
	module := strings.TrimSuffix(path.Base(file), path.Ext(file))
	inApp := !strings.Contains(file, "node_modules") && !hasAnyPrefix(file, "node:", "internal/")
	return Frame{Function: module + "." + fn, InApp: inApp}
}

// .NET: "Unhandled exception. Type: message", inner exceptions after
// " ---> ", frames as "at Namespace.Type.Method(args) in file:line n".
func parseDotnet(lines []string) Exception {
	e := Exception{Language: "dotnet"}
	head := strings.TrimPrefix(strings.TrimPrefix(lines[0], "Unhandled exception. "), "Unhandled Exception: ")
	for _, part := range strings.Split(head, " ---> ") {
		name, _, _ := strings.Cut(part, ": ")
		e.Types = append(e.Types, name)
	}
	for _, l := range lines[1:] {
		t := strings.TrimLeft(l, " \t")
		switch {
		case strings.HasPrefix(t, "at "):
			fn, _, _ := strings.Cut(t[3:], "(")
			if i := strings.IndexByte(fn, '['); i > 0 {
				fn = fn[:i] // generic arguments: First[TSource]
			}
			e.Frames = append(e.Frames, Frame{Function: fn, InApp: !hasAnyPrefix(fn, "System.", "Microsoft.", "Grpc.", "Google.Protobuf.")})
		case strings.HasPrefix(t, "---> "):
			name, _, _ := strings.Cut(t[5:], ": ")
			e.Types = append(e.Types, name)
		}
	}
	return e
}

// parseLogged handles an entry's message followed by frames, as loggers
// print an exception they caught. The masked message stands for the type.
func parseLogged(lines []string) Exception {
	e := parseJava(lines[1:])
	e.Types = append([]string{Mask(lines[0])}, e.Types...)
	if strings.Contains(lines[1], " in ") && strings.Contains(lines[1], ":line ") {
		e.Language = "dotnet"
	}
	return e
}

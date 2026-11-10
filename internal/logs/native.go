package logs

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Ruby: "/app/lib/shop.rb:4:in 'Shop#load': message (NoMethodError)", then
// "\tfrom FILE:LINE:in 'METHOD'" for each caller.
var (
	rubyHeader = regexp.MustCompile("^(\\S+\\.rb):(\\d+):in [`'](.+?)': ")
	rubyFrom   = regexp.MustCompile("^\\s+from (\\S+\\.rb):(\\d+):in [`'](.+)'$")
	rubyType   = regexp.MustCompile(`\(([A-Z]\w*(?:::[A-Z]\w*)*)\)$`)
)

func parseRuby(lines []string) Exception {
	e := Exception{Language: "ruby"}
	if m := rubyHeader.FindStringSubmatch(lines[0]); m != nil {
		e.Frames = append(e.Frames, rubyFrame(m[1], m[2], m[3]))
		e.Message = Mask(strings.TrimSpace(rubyType.ReplaceAllString(lines[0][len(m[0]):], "")))
	}
	for _, l := range lines {
		if m := rubyFrom.FindStringSubmatch(l); m != nil {
			e.Frames = append(e.Frames, rubyFrame(m[1], m[2], m[3]))
			continue
		}
		if t := rubyType.FindStringSubmatch(strings.TrimSpace(l)); t != nil && len(e.Types) == 0 {
			e.Types = append(e.Types, t[1])
		}
	}
	return e
}

func rubyFrame(file, line, method string) Frame {
	for _, p := range []string{"block in ", "rescue in ", "ensure in "} {
		method = strings.TrimPrefix(method, p)
	}
	inApp := !strings.Contains(file, "/gems/") && !strings.Contains(file, "/ruby/") && !strings.Contains(file, "vendor/bundle") &&
		!strings.HasPrefix(file, "<internal:")
	return Frame{Function: method, InApp: inApp, File: file, Line: atoi(line)}
}

// PHP: "PHP Fatal error:  Uncaught Error: message in /var/www/src/X.php:12",
// then "Stack trace:" and "#0 FILE(LINE): CALLEE" lines, where each line
// is in the function that made the call.
var (
	phpWhere = regexp.MustCompile(` in (\S+\.php):(\d+)$`)
	phpEntry = regexp.MustCompile(`^#\d+ (\S+\.php)\((\d+)\): (.+)$`)
)

func phpUncaught(first string) bool {
	return strings.Contains(first, "Fatal error:") && strings.Contains(first, "Uncaught ")
}

func parsePHP(lines []string) Exception {
	e := Exception{Language: "php"}
	_, rest, _ := strings.Cut(lines[0], "Uncaught ")
	if name, msg, ok := strings.Cut(rest, ":"); ok && name != "" && !strings.Contains(name, " ") {
		e.Types = append(e.Types, name)
		e.Message = Mask(strings.TrimSpace(phpWhere.ReplaceAllString(msg, "")))
	}
	type entry struct{ file, line, callee string }
	var entries []entry
	for _, l := range lines[1:] {
		if m := phpEntry.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			callee, _, _ := strings.Cut(m[3], "(")
			entries = append(entries, entry{m[1], m[2], callee})
		}
	}
	callee := func(i int) string {
		if i < len(entries) {
			return entries[i].callee
		}
		return "{main}"
	}
	frame := func(file, line, fn string) Frame {
		return Frame{Function: fn, InApp: !strings.Contains(file, "/vendor/"), File: file, Line: atoi(line)}
	}
	if m := phpWhere.FindStringSubmatch(lines[0]); m != nil {
		e.Frames = append(e.Frames, frame(m[1], m[2], callee(0)))
	}
	for i, en := range entries {
		e.Frames = append(e.Frames, frame(en.file, en.line, callee(i+1)))
	}
	return e
}

// Rust: "thread 'main' panicked at src/main.rs:10:5:" and the message on
// the next line (before Rust 1.73: "panicked at 'message', FILE:LINE:COL"),
// then, with RUST_BACKTRACE set, "N: path::to::function" lines, each
// followed by "at FILE:LINE:COL".
var (
	rustAt      = regexp.MustCompile(`panicked at (?:'(.*)', )?(\S+?):(\d+)(?::\d+)?:?$`)
	rustFrame   = regexp.MustCompile(`^\s*\d+: (.+)$`)
	rustFrameAt = regexp.MustCompile(`^\s+at (\S+?):(\d+)(?::\d+)?$`)
	rustHash    = regexp.MustCompile(`::h[0-9a-f]{16}$`)
)

func parseRust(lines []string) Exception {
	e := Exception{Language: "rust"}
	m := rustAt.FindStringSubmatch(lines[0])
	if m == nil {
		return e
	}
	msg := m[1]
	if msg == "" && len(lines) > 1 {
		msg = lines[1]
	}
	e.Types = []string{"panic: " + Mask(msg)}
	e.Message = Mask(msg)
	for i, l := range lines {
		f := rustFrame.FindStringSubmatch(l)
		if f == nil || strings.HasPrefix(strings.TrimSpace(l), "at ") {
			continue
		}
		fn := rustHash.ReplaceAllString(strings.TrimSpace(f[1]), "")
		fr := Frame{Function: fn}
		if i+1 < len(lines) {
			if at := rustFrameAt.FindStringSubmatch(lines[i+1]); at != nil {
				fr.File, fr.Line = at[1], atoi(at[2])
			}
		}
		fr.InApp = !hasAnyPrefix(fn, "std::", "core::", "alloc::", "rust_begin_unwind", "__rust", "<std::", "<core::", "<alloc::",
			"backtrace::", "tokio::", "hyper::", "futures", "axum::", "actix", "tower::") &&
			!strings.Contains(fr.File, "/rustc/") && !strings.Contains(fr.File, ".cargo/registry")
		e.Frames = append(e.Frames, fr)
	}
	if !slices.ContainsFunc(e.Frames, func(f Frame) bool { return f.InApp }) {
		// Without a backtrace, the panic's location is the one frame known.
		e.Frames = append([]Frame{{File: m[2], Line: atoi(m[3]), InApp: true}}, e.Frames...)
	}
	return e
}

// C and C++: an assertion as glibc or musl prints it, or a crash trace from
// a handler that prints one: Abseil's and glog's failure signal handlers,
// backward-cpp. A plain segfault prints none, and is no exception here.
var (
	glibcAssert   = regexp.MustCompile("^(?:\\S+: )?(\\S+\\.(?:c|cc|cpp|cxx|h|hh|hpp)):(\\d+): (.+?): Assertion [`'](.+)' failed\\.$")
	muslAssert    = regexp.MustCompile(`^Assertion failed: (.+) \((\S+\.(?:c|cc|cpp|cxx|h|hh|hpp)): (.+?): (\d+)\)$`)
	signalHeader  = regexp.MustCompile(`^\*\*\* (SIG[A-Z]+) (?:\(@0x[0-9a-f]+\) )?received`)
	signalFrame   = regexp.MustCompile(`^(?:PC: )?@\s+0x[0-9a-f]+\s+(?:\(unknown\)|\d+)?\s*(.*)$`)
	backwardEntry = regexp.MustCompile(`^#\d+\s`)
	backwardSrc   = regexp.MustCompile(`^\s+Source "(\S+)", line (\d+), in (.+?)(?: \[0x[0-9a-f]+\])?$`)
)

func nativeCrash(first string) bool {
	return signalHeader.MatchString(first) || strings.HasPrefix(first, "*** Aborted at ") ||
		strings.HasPrefix(first, "Stack trace (most recent call last)")
}

func parseNative(lines []string) Exception {
	e := Exception{Language: "cpp"}
	lang := func(file string) {
		if path.Ext(file) == ".c" {
			e.Language = "c"
		}
	}
	first := lines[0]
	switch m := glibcAssert.FindStringSubmatch(first); {
	case m != nil:
		lang(m[1])
		e.Types = []string{"assertion failed: " + Mask(m[4])}
		e.Message = Mask(m[4])
		e.Frames = []Frame{{Function: cFunction(m[3]), InApp: true, File: m[1], Line: atoi(m[2])}}
		return e
	}
	if m := muslAssert.FindStringSubmatch(first); m != nil {
		lang(m[2])
		e.Types = []string{"assertion failed: " + Mask(m[1])}
		e.Message = Mask(m[1])
		e.Frames = []Frame{{Function: cFunction(m[3]), InApp: true, File: m[2], Line: atoi(m[4])}}
		return e
	}
	if strings.HasPrefix(first, "Stack trace (most recent call last)") {
		e.Types = []string{"crash"}
		var frames []Frame
		for _, l := range lines[1:] {
			switch {
			case backwardEntry.MatchString(l):
				frames = append(frames, Frame{})
			case len(frames) > 0:
				if m := backwardSrc.FindStringSubmatch(l); m != nil && frames[len(frames)-1].File == "" {
					lang(m[1])
					frames[len(frames)-1] = Frame{Function: cFunction(m[3]), File: m[1], Line: atoi(m[2]),
						InApp: !hasAnyPrefix(m[1], "/usr/include/", "/usr/lib/")}
				}
			}
		}
		for i := len(frames) - 1; i >= 0; i-- { // most recent call last
			if frames[i].Function != "" {
				e.Frames = append(e.Frames, frames[i])
			}
		}
		return e
	}
	e.Types = []string{"crash"}
	for _, l := range lines {
		if m := signalHeader.FindStringSubmatch(l); m != nil {
			e.Types = []string{m[1]}
		}
		m := signalFrame.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || m[1] == "" {
			continue
		}
		fn := cFunction(m[1])
		e.Frames = append(e.Frames, Frame{Function: fn, InApp: !hasAnyPrefix(fn, "__", "_start", "abort", "raise", "gsignal",
			"(unknown)", "absl::", "google::", "std::", "start_thread", "clone", "libc")})
	}
	return e
}

// cFunction is a C or C++ function's qualified name, as __PRETTY_FUNCTION__
// or a demangler prints it: "int shop::Catalog::load(int)" is
// shop::Catalog::load.
func cFunction(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '('); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndexAny(s, " *&"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

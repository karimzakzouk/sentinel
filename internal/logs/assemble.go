// Package logs turns container log lines into records, templates and
// exception fingerprints. Everything here handles untrusted text: callers
// redact lines before they reach this package, and nothing here stores raw
// lines.
package logs

import (
	"strings"
	"time"
)

// Line is one log line and the time the runtime recorded it.
type Line struct {
	Time time.Time
	Text string
}

// ParseLine splits a line fetched with timestamps, as in
// "2026-11-08T21:14:11.123456789Z message", into its time and text. A line
// without a timestamp keeps a zero time.
func ParseLine(s string) Line {
	s = strings.TrimSuffix(s, "\r")
	if i := strings.IndexByte(s, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, s[:i]); err == nil {
			return Line{Time: t, Text: s[i+1:]}
		}
	}
	return Line{Text: s}
}

// Record is one log entry: a single line, or a stack trace with its frames
// and causes.
type Record struct {
	Time  time.Time
	Lines []string
	// Truncated reports lines dropped beyond the record size limits.
	Truncated bool
}

// Text joins the record's lines.
func (r Record) Text() string { return strings.Join(r.Lines, "\n") }

// Record size limits keep one runaway trace from holding unbounded memory.
const (
	maxRecordLines = 500
	maxRecordBytes = 64 << 10
)

// Assembler groups lines into records. Indented lines continue the record
// before them; inside a stack trace, so do the unindented lines each runtime
// prints between frames (causes, goroutine headers, chained tracebacks), and
// blank lines that such a line follows.
type Assembler struct {
	cur     *Record
	size    int
	state   state
	pending []string // blank lines inside a stack trace, kept if the trace goes on
}

type state int

const (
	plain state = iota
	goPanic
	javaStack
	pythonFrames // inside a traceback's frames: the next unindented line is the exception
	pythonDone   // after the exception line: only a chained traceback continues
	nodeStack
	dotnetStack
	phpStack
	rustPanic // after the header: the next line is the panic's message
	rustAfter
	nativeSignal
	backwardStack
)

// Add takes the next line and returns the records it completed.
func (a *Assembler) Add(l Line) []Record {
	text := l.Text
	if a.cur == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		a.start(l)
		return nil
	}
	if strings.TrimSpace(text) == "" {
		if a.state != plain {
			a.pending = append(a.pending, text)
			return nil
		}
		return a.Flush()
	}
	if a.continues(text) {
		for _, p := range a.pending {
			a.append(p)
		}
		a.pending = nil
		a.append(text)
		a.advance(text)
		return nil
	}
	out := a.Flush()
	a.start(l)
	return out
}

// Flush ends the current record and returns it.
func (a *Assembler) Flush() []Record {
	if a.cur == nil {
		return nil
	}
	r := *a.cur
	a.cur, a.size, a.state, a.pending = nil, 0, plain, nil
	return []Record{r}
}

// Assemble groups a batch of lines.
func Assemble(lines []Line) []Record {
	var a Assembler
	var out []Record
	for _, l := range lines {
		out = append(out, a.Add(l)...)
	}
	return append(out, a.Flush()...)
}

func (a *Assembler) start(l Line) {
	a.cur = &Record{Time: l.Time}
	a.size = 0
	a.state = startState(l.Text)
	a.append(l.Text)
}

func (a *Assembler) append(text string) {
	if len(a.cur.Lines) >= maxRecordLines || a.size+len(text) > maxRecordBytes {
		a.cur.Truncated = true
		return
	}
	a.cur.Lines = append(a.cur.Lines, text)
	a.size += len(text)
}

func indented(text string) bool { return text[0] == ' ' || text[0] == '\t' }

func (a *Assembler) continues(text string) bool {
	switch a.state {
	case goPanic:
		// A goroutine dump prints unindented function lines; the trace ends
		// where the next log entry begins.
		return !looksLikeEntryStart(text)
	case pythonFrames:
		return true
	case pythonDone:
		return indented(text) || pythonChain(text)
	case javaStack:
		return indented(text) || hasAnyPrefix(text, "Caused by: ", "Suppressed: ")
	case nodeStack:
		return indented(text) || strings.HasPrefix(text, "Node.js v")
	case phpStack:
		return indented(text) || hasAnyPrefix(text, "Stack trace:", "#", "PHP Stack trace:")
	case rustPanic:
		return true
	case rustAfter:
		return indented(text) || hasAnyPrefix(text, "note: ", "stack backtrace:")
	case nativeSignal:
		return indented(text) || hasAnyPrefix(text, "PC: @", "@ ", "*** ")
	case backwardStack:
		return indented(text) || backwardEntry.MatchString(text)
	}
	return indented(text)
}

// advance updates the state after text joined the record.
func (a *Assembler) advance(text string) {
	trimmed := strings.TrimLeft(text, " \t")
	switch a.state {
	case plain:
		switch {
		case strings.HasPrefix(trimmed, "at ") && strings.Contains(trimmed, "("):
			// A logged exception: the entry's frames follow its message.
			a.state = javaStack
		case strings.HasPrefix(trimmed, "File \""):
			a.state = pythonFrames
		}
	case pythonFrames:
		if !indented(text) {
			a.state = pythonDone
		}
	case pythonDone:
		if strings.HasPrefix(text, "Traceback (most recent call last):") {
			a.state = pythonFrames
		}
	case rustPanic:
		a.state = rustAfter
	}
}

func startState(text string) state {
	switch {
	case hasAnyPrefix(text, "panic: ", "fatal error: "):
		return goPanic
	case strings.HasPrefix(text, "Traceback (most recent call last):"):
		return pythonFrames
	case hasAnyPrefix(text, "Unhandled exception. ", "Unhandled Exception: "):
		return dotnetStack
	case strings.HasPrefix(text, "Exception in thread "), javaThrowable(text):
		return javaStack
	case jsError(text):
		return nodeStack
	case phpUncaught(text):
		return phpStack
	case strings.HasPrefix(text, "thread '") && strings.Contains(text, "' panicked at "):
		return rustPanic
	case signalHeader.MatchString(text), strings.HasPrefix(text, "*** Aborted at "):
		return nativeSignal
	case strings.HasPrefix(text, "Stack trace (most recent call last)"):
		return backwardStack
	}
	return plain
}

func pythonChain(text string) bool {
	return hasAnyPrefix(text, "Traceback (most recent call last):",
		"During handling of the above exception, another exception occurred:",
		"The above exception was the direct cause of the following exception:")
}

// javaThrowable recognizes "java.lang.IllegalStateException: message".
func javaThrowable(text string) bool {
	name, _, _ := strings.Cut(text, ": ")
	if !strings.Contains(name, ".") || strings.ContainsAny(name, " \t") {
		return false
	}
	return hasAnySuffix(name, "Exception", "Error", "Throwable")
}

// jsError recognizes "TypeError: message" and "Error: message".
func jsError(text string) bool {
	name, _, _ := strings.Cut(text, ": ")
	return strings.HasSuffix(name, "Error") && !strings.ContainsAny(name, " .\t") && name[0] >= 'A' && name[0] <= 'Z'
}

// looksLikeEntryStart recognizes the first line of a new log entry: a
// timestamp, a klog header, structured output.
func looksLikeEntryStart(text string) bool {
	if len(text) >= 10 && digits(text[:4]) && text[4] == '-' && digits(text[5:7]) && text[7] == '-' {
		return true
	}
	if len(text) >= 5 && strings.ContainsRune("IWEF", rune(text[0])) && digits(text[1:5]) {
		return true
	}
	return hasAnyPrefix(text, "{", "level=", "time=", "ts=", "msg=")
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, p := range suffixes {
		if strings.HasSuffix(s, p) {
			return true
		}
	}
	return false
}

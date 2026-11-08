package logs

import (
	"strings"
	"testing"
	"time"
)

func lines(text string) []Line {
	var out []Line
	for _, s := range strings.Split(text, "\n") {
		out = append(out, Line{Text: s})
	}
	return out
}

func TestStackTracesBecomeOneRecord(t *testing.T) {
	before, after := "2026-11-08T21:14:10Z INFO starting", "2026-11-08T21:14:12Z INFO next entry"
	for name, trace := range map[string]string{
		"go": goPanicTrace, "java": javaTrace, "python": pythonTrace, "python chained": pythonChainedTrace,
		"node": nodeTrace, "dotnet": dotnetTrace, "logged java": loggedJavaTrace,
	} {
		t.Run(name, func(t *testing.T) {
			recs := Assemble(lines(before + "\n" + trace + "\n" + after))
			if len(recs) != 3 {
				for _, r := range recs {
					t.Logf("record: %q", r.Text())
				}
				t.Fatalf("got %d records, want 3", len(recs))
			}
			if got := recs[1].Text(); got != trace {
				t.Errorf("trace record:\n%s\nwant:\n%s", got, trace)
			}
			if recs[0].Text() != before || recs[2].Text() != after {
				t.Errorf("neighbors: %q, %q", recs[0].Text(), recs[2].Text())
			}
		})
	}
}

func TestPlainLinesStaySeparate(t *testing.T) {
	recs := Assemble(lines("one\ntwo\n\nthree\n  indented continues three"))
	want := []string{"one", "two", "three\n  indented continues three"}
	if len(recs) != len(want) {
		t.Fatalf("records = %d, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if r.Text() != want[i] {
			t.Errorf("record %d = %q, want %q", i, r.Text(), want[i])
		}
	}
}

func TestBlankLinesEndATraceUnlessItGoesOn(t *testing.T) {
	recs := Assemble(lines("Error: boom\n    at f (/app/x.js:1:2)\n\nlistening on 8080"))
	if len(recs) != 2 || recs[1].Text() != "listening on 8080" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecordsAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("panic: boom\n")
	for range 2 * maxRecordLines {
		b.WriteString("\tframe\n")
	}
	recs := Assemble(lines(b.String()))
	if len(recs) != 1 || len(recs[0].Lines) != maxRecordLines || !recs[0].Truncated {
		t.Fatalf("got %d records, first with %d lines, truncated %v", len(recs), len(recs[0].Lines), recs[0].Truncated)
	}
}

func TestParseLine(t *testing.T) {
	l := ParseLine("2026-11-08T21:14:11.123456789Z panic: boom\r")
	if l.Text != "panic: boom" || !l.Time.Equal(time.Date(2026, 11, 8, 21, 14, 11, 123456789, time.UTC)) {
		t.Fatalf("parsed %+v", l)
	}
	if l := ParseLine("no timestamp here"); l.Text != "no timestamp here" || !l.Time.IsZero() {
		t.Fatalf("parsed %+v", l)
	}
}

package logs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/redact"
)

func timestamped(lines ...string) []byte {
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "2026-11-08T21:14:%02d.000000000Z %s\n", i%60, l)
	}
	return []byte(b.String())
}

func TestSummarizeAPythonCrash(t *testing.T) {
	var log []string
	for i := range 5 {
		log = append(log, fmt.Sprintf(`{"level":"info","msg":"cart %d loaded in %dms"}`, i, 10+i))
	}
	log = append(log, `{"level":"error","msg":"redis connect failed","password":"hunter2-Zx8kQ2"}`)
	log = append(log, strings.Split(pythonTrace, "\n")...)
	s := Summarize(timestamped(log...), redact.New())

	if s.Lines != len(log) || s.Records != 7 || s.SHA256 == "" || s.From.IsZero() || !s.To.After(s.From) {
		t.Fatalf("summary header = %+v", s)
	}
	if len(s.Templates) != 3 || s.Templates[0].Template != "cart <num> loaded in <duration>" || s.Templates[0].Count != 5 || s.Templates[0].Error {
		t.Fatalf("templates = %+v", s.Templates)
	}
	for _, tpl := range s.Templates[1:] {
		if !tpl.Error {
			t.Errorf("template %q not marked as an error", tpl.Template)
		}
	}
	if len(s.Exceptions) != 1 || fmt.Sprint(s.Exceptions[0].Types) != "[redis.exceptions.ConnectionError]" ||
		s.Exceptions[0].Sample != "Traceback (most recent call last):" {
		t.Fatalf("exceptions = %+v", s.Exceptions)
	}
	if s.Redactions["secret-value"] != 1 {
		t.Fatalf("redactions = %v", s.Redactions)
	}
	out, _ := json.Marshal(s)
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("a secret reached the summary: %s", out)
	}
}

func TestSummariesAreBounded(t *testing.T) {
	var log []string
	for i := range 200 {
		log = append(log, fmt.Sprintf("distinct%c%c message number %d %s", 'a'+i%26, 'a'+i/26, i, strings.Repeat("x", 500)))
	}
	for i := range 40 {
		log = append(log, fmt.Sprintf("failure%c%c in step %d", 'a'+i%26, 'a'+i/26, i))
	}
	s := Summarize(timestamped(log...), redact.New())
	if len(s.Templates) != maxTemplates+maxErrorTemplates {
		t.Fatalf("templates = %d, want %d frequent and %d more errors", len(s.Templates), maxTemplates, maxErrorTemplates)
	}
	for _, tpl := range s.Templates {
		if len([]rune(tpl.Sample)) > maxSample+1 {
			t.Fatalf("sample of %d runes", len([]rune(tpl.Sample)))
		}
	}
}

// .NET's console logger puts the level and category on a line of their own;
// the message, an error here, and its exception follow.
func TestSummarizeDotnetConsoleErrors(t *testing.T) {
	var log []string
	for range 3 {
		log = append(log, "GetCartAsync called with userId=6b0a5ffa-3e1c-4a5e-9c41-0d8f6f2b6c1e")
		log = append(log, strings.Split(dotnetConsoleTrace, "\n")...)
	}
	log = append(log, "info: Microsoft.Hosting.Lifetime[0]", "      Now listening on: http://[::]:7070")
	s := Summarize(timestamped(log...), redact.New())
	if s.Records != 7 {
		t.Fatalf("records = %d, want 7", s.Records)
	}
	byTemplate := map[string]TemplateCount{}
	for _, tpl := range s.Templates {
		byTemplate[tpl.Template] = tpl
	}
	var failed TemplateCount
	for tpl, tc := range byTemplate {
		if strings.HasPrefix(tpl, "Error status code 'FailedPrecondition'") {
			failed = tc
		}
	}
	if failed.Count != 3 || !failed.Error || !strings.HasPrefix(failed.Sample, "Error status code") {
		t.Fatalf("templates = %+v, want the failed call three times, as an error", s.Templates)
	}
	if tc, ok := byTemplate["Now listening on: http://<ip>"]; !ok || tc.Error {
		t.Fatalf("templates = %+v, want the startup line as information", s.Templates)
	}
	if len(s.Exceptions) != 1 || s.Exceptions[0].Count != 3 || s.Exceptions[0].Language != "dotnet" ||
		s.Exceptions[0].Types[len(s.Exceptions[0].Types)-1] != "StackExchange.Redis.RedisConnectionException" {
		t.Fatalf("exceptions = %+v", s.Exceptions)
	}
}

func TestMessageAndLevel(t *testing.T) {
	for line, want := range map[string]string{
		`{"severity":"ERROR","message":"payment failed"}`:                                        "payment failed|error",
		`{"level":"info","msg":"ok"}`:                                                            "ok|info",
		`{"severity":"error","message":"request error","error":"rpc error: code = Unavailable"}`: "request error: rpc error: code = Unavailable|error",
		`E1108 21:14:11.123456 1 controller.go:42] sync failed`:                                  "E1108 21:14:11.123456 1 controller.go:42] sync failed|error",
		`plain text`:   "plain text|",
		`{"no":"msg"}`: `{"no":"msg"}|`,
	} {
		msg, level := message(line)
		if got := msg + "|" + level; got != want {
			t.Errorf("message(%q) = %q, want %q", line, got, want)
		}
	}
}

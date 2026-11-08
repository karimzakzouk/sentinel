package logs

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/redact"
)

// Summary is what Sentinel keeps of a fetched log: counts, templates and
// exceptions with redacted samples, and the hash of the raw bytes. Never
// the raw lines.
type Summary struct {
	Lines   int       `json:"lines"`
	Records int       `json:"records"`
	From    time.Time `json:"from,omitzero"`
	To      time.Time `json:"to,omitzero"`
	// SHA256 is the hash of the raw log bytes, so the summary can be checked
	// against logs kept elsewhere.
	SHA256     string           `json:"sha256"`
	Templates  []TemplateCount  `json:"templates,omitempty"`
	Exceptions []ExceptionCount `json:"exceptions,omitempty"`
	// Redactions counts what the redactor removed, by rule.
	Redactions map[string]int `json:"redactions,omitempty"`
}

// TemplateCount is a template seen in a log, how often, and one redacted example.
type TemplateCount struct {
	Template string `json:"template"`
	Count    int    `json:"count"`
	// Error marks templates at error level or worded like errors.
	Error  bool   `json:"error,omitempty"`
	Sample string `json:"sample"`
}

// ExceptionCount is an exception seen in a log, how often, and its first line, redacted.
type ExceptionCount struct {
	Exception
	Count  int    `json:"count"`
	Sample string `json:"sample"`
}

// Summary limits keep evidence small. Error templates beyond the most
// frequent ones are kept too, so a busy service's info lines cannot crowd
// its errors out.
const (
	maxTemplates      = 10
	maxErrorTemplates = 20
	maxExceptions     = 5
	maxSample         = 300
)

// Summarize processes a fetched log: lines with runtime timestamps, as the
// Kubernetes API returns them with timestamps=true.
func Summarize(raw []byte, r *redact.Redactor) Summary {
	sum := sha256.Sum256(raw)
	s := Summary{SHA256: hex.EncodeToString(sum[:])}
	var lines []Line
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), maxRecordBytes)
	for sc.Scan() {
		lines = append(lines, ParseLine(sc.Text()))
	}
	s.Lines = len(lines)
	for _, l := range lines {
		if !l.Time.IsZero() {
			if s.From.IsZero() {
				s.From = l.Time
			}
			s.To = l.Time
		}
	}

	miner := NewMiner()
	type templateStats struct {
		error  bool
		sample string
	}
	stats := map[*Template]*templateStats{}
	exceptions := map[string]*ExceptionCount{}
	for _, rec := range Assemble(lines) {
		s.Records++
		text, found := r.String(rec.Text())
		for _, f := range found {
			if s.Redactions == nil {
				s.Redactions = map[string]int{}
			}
			s.Redactions[f.Rule]++
		}
		redacted := Record{Time: rec.Time, Lines: strings.Split(text, "\n")}
		e, isException := ParseException(redacted)
		if isException {
			if ec := exceptions[e.Fingerprint]; ec != nil {
				ec.Count++
			} else {
				exceptions[e.Fingerprint] = &ExceptionCount{Exception: e, Count: 1, Sample: clip(redacted.Lines[0])}
			}
		}
		msg, level := message(redacted.Lines[0])
		tpl := miner.Add(Mask(msg))
		st := stats[tpl]
		if st == nil {
			st = &templateStats{sample: clip(redacted.Lines[0])}
			stats[tpl] = st
		}
		st.error = st.error || isException || errorLike(level, msg)
	}

	for _, tpl := range miner.Templates() {
		st := stats[tpl]
		s.Templates = append(s.Templates, TemplateCount{Template: tpl.String(), Count: tpl.Count, Error: st.error, Sample: st.sample})
	}
	sort.SliceStable(s.Templates, func(i, j int) bool { return s.Templates[i].Count > s.Templates[j].Count })
	kept, errs := s.Templates[:0], 0
	for i, t := range s.Templates {
		switch {
		case i < maxTemplates:
			kept = append(kept, t)
		case t.Error && errs < maxErrorTemplates:
			kept = append(kept, t)
			errs++
		}
	}
	s.Templates = kept
	for _, ec := range exceptions {
		s.Exceptions = append(s.Exceptions, *ec)
	}
	sort.Slice(s.Exceptions, func(i, j int) bool {
		if s.Exceptions[i].Count != s.Exceptions[j].Count {
			return s.Exceptions[i].Count > s.Exceptions[j].Count
		}
		return s.Exceptions[i].Fingerprint < s.Exceptions[j].Fingerprint
	})
	s.Exceptions = s.Exceptions[:min(len(s.Exceptions), maxExceptions)]
	return s
}

func clip(s string) string {
	if len(s) <= maxSample {
		return s
	}
	cut := maxSample
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut] + "…"
}

// message returns the text templates are mined from, and the level if the
// line states one: a JSON line's message and level fields, a klog header's
// severity, or the line.
func message(line string) (string, string) {
	if len(line) > 5 && (line[0] == 'E' || line[0] == 'F') && digits(line[1:5]) {
		return line, "error"
	}
	if strings.HasPrefix(line, "{") {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) == nil {
			msg := firstString(fields, "msg", "message", "MESSAGE", "Message", "log")
			level := firstString(fields, "level", "severity", "lvl", "LEVEL", "Level", "levelname")
			// Structured loggers often keep the error apart from the message.
			if e := firstString(fields, "error", "err", "exception"); e != "" {
				msg += ": " + e
			}
			if msg != "" {
				return msg, strings.ToLower(level)
			}
		}
	}
	return line, ""
}

func firstString(fields map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := fields[k].(string); ok {
			return v
		}
	}
	return ""
}

var errorLevels = map[string]bool{"error": true, "err": true, "fatal": true, "panic": true, "critical": true,
	"crit": true, "alert": true, "emerg": true, "emergency": true, "severe": true}

var errorWords = []string{"error", "exception", "fail", "fatal", "panic", "refused", "timeout", "timed out",
	"denied", "unavailable", "unable to", "cannot", "can't", "could not", "unknown flag", "invalid", "unreachable"}

// errorLike reports a message at an error level, or worded like an error.
func errorLike(level, msg string) bool {
	if errorLevels[level] {
		return true
	}
	lower := strings.ToLower(msg)
	for _, w := range errorWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

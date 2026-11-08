// Package redact removes secrets and personal data from untrusted text, such
// as container logs, before Sentinel keeps it or sends it anywhere.
//
// Detectors are layered and fail closed: provider token formats, structured
// credentials (private keys, JWTs, URL passwords, authorization headers, card
// numbers, email addresses), the values of keys whose names say they hold a
// secret, and strings as random as generated secrets. Every detector misses
// something, so text is redacted again before it leaves the cluster, and raw
// text never goes into permanent records.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Finding is one redacted span of the input, in byte offsets.
type Finding struct {
	Rule       string
	Start, End int
}

const markerPrefix = "[REDACTED:"

// Marker is the text that replaces a span found by rule.
func Marker(rule string) string { return markerPrefix + rule + "]" }

var markerRE = regexp.MustCompile(`\[REDACTED:[a-z0-9-]+\]`)

// Redactor applies the detectors. It is safe for concurrent use.
type Redactor struct {
	patterns []pattern
}

// New returns a Redactor with the default detectors.
func New() *Redactor { return &Redactor{patterns: patterns} }

// String returns s with every finding replaced by its marker, and the
// findings. Redacting the result again returns it unchanged.
func (r *Redactor) String(s string) (string, []Finding) {
	// Markers from an earlier pass are opaque: their colons and brackets must
	// not look like delimiters to any detector.
	var markers spans
	for _, m := range markerRE.FindAllStringIndex(s, -1) {
		markers = append(markers, Finding{Start: m[0], End: m[1]})
	}
	var found []Finding
	add := func(f Finding) {
		if _, inMarker := markers.find(f); f.Start < f.End && !inMarker {
			found = append(found, f)
		}
	}
	lower := strings.ToLower(s)
	for i := range r.patterns {
		p := &r.patterns[i]
		if !p.mayMatch(s, lower) {
			continue
		}
		for _, m := range p.re.FindAllStringSubmatchIndex(s, -1) {
			start, end := m[2*p.group], m[2*p.group+1]
			if start >= 0 && (p.check == nil || p.check(s[start:end])) {
				add(Finding{Rule: p.rule, Start: start, End: end})
			}
		}
	}
	secrets, identifiers := keyValues(s)
	for _, f := range secrets {
		add(f)
	}
	found = merge(found)
	for _, f := range randomStrings(s, found, merge(identifiers)) {
		add(f)
	}
	if len(found) == 0 {
		return s, nil
	}
	found = merge(found)
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, f := range found {
		b.WriteString(s[last:f.Start])
		b.WriteString(Marker(f.Rule))
		last = f.End
	}
	b.WriteString(s[last:])
	return b.String(), found
}

// spans are sorted and disjoint.
type spans []Finding

// find returns a span that overlaps f.
func (ss spans) find(f Finding) (Finding, bool) {
	i := sort.Search(len(ss), func(i int) bool { return ss[i].End > f.Start })
	if i < len(ss) && ss[i].Start < f.End {
		return ss[i], true
	}
	return Finding{}, false
}

// merge sorts findings and joins overlapping ones. A joined finding keeps the
// rule of the one that starts first.
func merge(fs []Finding) spans {
	if len(fs) == 0 {
		return nil
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Start != fs[j].Start {
			return fs[i].Start < fs[j].Start
		}
		return fs[i].End > fs[j].End
	})
	out := fs[:1]
	for _, f := range fs[1:] {
		last := &out[len(out)-1]
		if f.Start < last.End {
			last.End = max(last.End, f.End)
			continue
		}
		out = append(out, f)
	}
	return out
}

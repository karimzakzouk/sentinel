package codefix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// Change is one fixer's proposal for a crash: edits to the source at the
// commit the failing image was built from, what they change, and why.
type Change struct {
	// Fixer names what proposed it: restore, template:NAME, revert,
	// history or llm.
	Fixer string
	// Function is what it changes, for people.
	Function string
	Edits    []Edit
	Why      string
}

// Edit is one file of a Change. After nil deletes the file; Before nil
// creates it.
type Edit struct {
	Path          string
	Line          int
	Before, After []byte
}

// Summary is a line per edit, as a pull request lists them.
func (c *Change) Summary() []string {
	var out []string
	for _, e := range c.Edits {
		switch {
		case e.After == nil:
			out = append(out, fmt.Sprintf("%s: removed (%s)", e.Path, c.Fixer))
		case e.Before == nil:
			out = append(out, fmt.Sprintf("%s: put back (%s)", e.Path, c.Fixer))
		default:
			out = append(out, fmt.Sprintf("%s:%d: %s (%s)", e.Path, e.Line, c.Function, c.Fixer))
		}
	}
	return out
}

// Input is what every fixer sees of a crash.
type Input struct {
	Language string
	// Types are the crash's exception types, outermost first, or a
	// runtime's masked panic message; Message is the message, masked.
	Types   []string
	Message string
	Frames  []logs.Frame
	// Good and Bad are the repository's files at the commits the previous
	// image and the failing one were built from.
	Good, Bad map[string][]byte
}

// A Fixer proposes changes to the source for a crash, best first, or none.
// It never acts: the Lab judges every change, and a person merges it.
type Fixer interface {
	Name() string
	Propose(ctx context.Context, in Input) ([]*Change, error)
}

// Restorer puts back, as it was before the culprit commit, the function the
// crash ran through that the commit changed.
type Restorer struct{}

func (Restorer) Name() string { return "restore" }

func (Restorer) Propose(_ context.Context, in Input) ([]*Change, error) {
	p, err := Propose(in.Language, in.Frames, in.Good, in.Bad)
	if err != nil {
		return nil, err
	}
	return []*Change{{Fixer: "restore", Function: p.Function, Why: p.Why,
		Edits: []Edit{{Path: p.Path, Line: p.Line, Before: p.Before, After: p.After}}}}, nil
}

// Reverter puts back every file the culprit commit changed, as it was:
// the whole commit undone, for when no smaller change holds.
type Reverter struct{}

func (Reverter) Name() string { return "revert" }

func (Reverter) Propose(_ context.Context, in Input) ([]*Change, error) {
	var names []string
	for name := range in.Bad {
		names = append(names, name)
	}
	for name := range in.Good {
		if _, ok := in.Bad[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	c := &Change{Fixer: "revert", Function: "every file the culprit commit changed",
		Why: "no smaller change is known to hold; this undoes the culprit commit, every file it changed put back as it was"}
	for _, name := range names {
		was, before := in.Good[name]
		is, after := in.Bad[name]
		switch {
		case before && after && bytes.Equal(was, is):
		case before && after:
			c.Edits = append(c.Edits, Edit{Path: name, Line: firstDifference(is, was), Before: is, After: was})
		case before:
			c.Edits = append(c.Edits, Edit{Path: name, Line: 1, After: was})
		default:
			c.Edits = append(c.Edits, Edit{Path: name, Line: 1, Before: is})
		}
	}
	if len(c.Edits) == 0 {
		return nil, errors.New("the culprit commit changed no file")
	}
	return []*Change{c}, nil
}

func firstDifference(a, b []byte) int {
	n := commonPrefix(a, b)
	return bytes.Count(a[:n], []byte("\n")) + 1
}

func commonPrefix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffix(a, b []byte, limit int) int {
	n := 0
	for n < len(a) && n < len(b) && n < limit && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

// ErrMoved says a change no longer applies where the branch's head is.
var ErrMoved = errors.New("the source changed there again since the failing commit")

// Rebase applies a change made at the failing commit to the files at the
// branch's head: each edit's changed region, with a few lines around it,
// must appear exactly once in the head's file, as it did when proposed.
func Rebase(c *Change, head map[string][]byte) (*Change, error) {
	out := &Change{Fixer: c.Fixer, Function: c.Function, Why: c.Why}
	for _, e := range c.Edits {
		now, exists := head[e.Path]
		switch {
		case e.Before == nil && exists, e.Before != nil && !exists:
			return nil, fmt.Errorf("%w: %s", ErrMoved, e.Path)
		case e.Before == nil:
			out.Edits = append(out.Edits, e)
			continue
		case bytes.Equal(now, e.Before) || e.After == nil:
			if !bytes.Equal(now, e.Before) {
				return nil, fmt.Errorf("%w: %s", ErrMoved, e.Path)
			}
			out.Edits = append(out.Edits, Edit{Path: e.Path, Line: e.Line, Before: now, After: e.After})
			continue
		}
		start := commonPrefix(e.Before, e.After)
		end := commonSuffix(e.Before, e.After, min(len(e.Before), len(e.After))-start)
		// Whole lines, with two more on each side, anchor the region.
		from := lineStart(e.Before, start, 3)
		to := lineEnd(e.Before, len(e.Before)-end, 3)
		old := e.Before[from:to]
		repl := e.After[from : len(e.After)-(len(e.Before)-to)]
		if bytes.Count(now, old) != 1 {
			return nil, fmt.Errorf("%w: %s", ErrMoved, e.Path)
		}
		i := bytes.Index(now, old)
		after := append(append(append([]byte{}, now[:i]...), repl...), now[i+len(old):]...)
		out.Edits = append(out.Edits, Edit{Path: e.Path, Line: bytes.Count(now[:i], []byte("\n")) + 1 + (e.Line - 1 - bytes.Count(e.Before[:from], []byte("\n"))),
			Before: now, After: after})
	}
	return out, nil
}

// lineStart is the start of the line holding offset i, moved back n-1
// more lines.
func lineStart(b []byte, i, n int) int {
	for ; n > 0; n-- {
		j := bytes.LastIndexByte(b[:i], '\n')
		if j < 0 {
			return 0
		}
		i = j
	}
	return i + 1
}

// lineEnd is the end of the line holding offset i, moved on n-1 more
// lines.
func lineEnd(b []byte, i, n int) int {
	for ; n > 0; n-- {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return len(b)
		}
		i += j + 1
	}
	return i
}

// Same reports whether two changes leave the source the same.
func Same(a, b *Change) bool {
	if len(a.Edits) != len(b.Edits) {
		return false
	}
	for i := range a.Edits {
		if a.Edits[i].Path != b.Edits[i].Path || !bytes.Equal(a.Edits[i].After, b.Edits[i].After) {
			return false
		}
	}
	return true
}

// Fixers runs fixers in order and returns every change they propose, best
// first, without one that leaves the source as an earlier one does; each
// fixer's refusal is kept, to say why when none has a change.
func Fixers(ctx context.Context, in Input, fixers ...Fixer) ([]*Change, []string) {
	var out []*Change
	var refusals []string
	for _, f := range fixers {
		changes, err := f.Propose(ctx, in)
		if err != nil {
			refusals = append(refusals, f.Name()+": "+err.Error())
			continue
		}
	next:
		for _, c := range changes {
			for _, have := range out {
				if Same(have, c) {
					continue next
				}
			}
			out = append(out, c)
		}
	}
	return out, refusals
}

// Explain joins fixers' refusals into one reason.
func Explain(refusals []string) string {
	return strings.Join(refusals, "; ")
}

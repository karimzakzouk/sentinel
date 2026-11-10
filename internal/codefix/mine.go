package codefix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/mine"
)

// Mined proposes changes learned from the repository's own fix history:
// each pattern whose before snippet appears once in the crashing file is
// rewritten to its after. It runs after templates and before a revert.
type Mined struct {
	Patterns []mine.Pattern
}

func (Mined) Name() string { return "mine" }

func (m Mined) Propose(_ context.Context, in Input) ([]*Change, error) {
	if len(m.Patterns) == 0 {
		return nil, errors.New("no repair patterns were mined from the repository's history")
	}
	s, src, err := crashSite(in)
	if err != nil {
		// Without a crash line, try every in-app frame's file, or the whole
		// failing tree for a language match.
		return m.proposeAnywhere(in)
	}
	var out []*Change
	var tried []string
	for _, p := range m.Patterns {
		if p.Language != "" && p.Language != in.Language {
			continue
		}
		before, after := []byte(p.BeforeSnippet), []byte(p.AfterSnippet)
		if bytes.Count(src, before) != 1 {
			tried = append(tried, shortMsg(p.Message)+": before snippet not found once")
			continue
		}
		edited := bytes.Replace(src, before, after, 1)
		if err := checkParse(s.file, src, edited); err != nil {
			tried = append(tried, shortMsg(p.Message)+": "+err.Error())
			continue
		}
		name := s.file
		if s.fn != nil {
			name = s.fn.name()
		}
		line := s.line
		if i := bytes.Index(src, before); i >= 0 {
			line = bytes.Count(src[:i], []byte("\n")) + 1
		}
		why := fmt.Sprintf("applies a repair mined from the repository's history (%s)", p.Message)
		out = append(out, &Change{Fixer: "mine", Function: name, Why: why,
			Edits: []Edit{{Path: s.file, Line: line, Before: src, After: edited}}})
	}
	if len(out) == 0 {
		if len(tried) > 0 {
			return nil, errors.New(strings.Join(tried, "; "))
		}
		return nil, errors.New("no mined pattern matches the crashing file")
	}
	return out, nil
}

// proposeAnywhere tries patterns against any failing source file of the
// crash's language when there is no stack frame to name a line.
func (m Mined) proposeAnywhere(in Input) ([]*Change, error) {
	exts := languageFiles[in.Language]
	if in.Language == "go" {
		exts = []string{".go"}
	}
	files := sortedFiles(in.Bad, exts)
	if len(files) == 0 {
		return nil, errors.New("no mined pattern matches: the repository has no source of the crash's language")
	}
	var out []*Change
	for _, p := range m.Patterns {
		if p.Language != "" && p.Language != in.Language {
			continue
		}
		before, after := []byte(p.BeforeSnippet), []byte(p.AfterSnippet)
		for _, name := range files {
			src := in.Bad[name]
			if bytes.Count(src, before) != 1 {
				continue
			}
			edited := bytes.Replace(src, before, after, 1)
			if err := checkParse(name, src, edited); err != nil {
				continue
			}
			line := bytes.Count(src[:bytes.Index(src, before)], []byte("\n")) + 1
			out = append(out, &Change{Fixer: "mine", Function: name,
				Why:   fmt.Sprintf("applies a repair mined from the repository's history (%s)", p.Message),
				Edits: []Edit{{Path: name, Line: line, Before: src, After: edited}}})
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no mined pattern matches a source file of the crash")
	}
	return out, nil
}

func shortMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:57] + "..."
	}
	if s == "" {
		return "mined"
	}
	return s
}

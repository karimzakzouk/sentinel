package codefix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Completer is a language model: a system prompt and a request in, text out.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// LLM asks a language model for new text for the functions the crash ran
// through, given only what Sentinel found: the crash, those functions, how
// the culprit commit changed them, and the changes already tried. It edits
// nothing itself: Sentinel puts the functions it returns in place, checks
// the files still parse, and sends back what does not; the Lab judges the
// result and a person merges it. Any model works; a weaker one gets fewer
// changes through.
type LLM struct {
	Model Completer
	// Retries is how many times a reply that does not apply is sent back.
	Retries int
	// Tried are earlier changes and what became of them.
	Tried []string
}

func (LLM) Name() string { return "llm" }

const llmSystem = `You fix one crash in a service's source code. Sentinel has already found where it crashed and what the culprit commit changed.
Reply with the complete new text of each function you change, and nothing else, in exactly this form:

FILE: <path, as given>
FUNCTION: <name, as given>
` + "```" + `
<the function's complete new text>
` + "```" + `

Change as little as you can. Change only functions you were shown. Do not add imports, new functions or comments.
Prefer the code as it was before the culprit commit where that fixes the crash; keep the commit's other intent where you can.`

// llmFunction is a function shown to the model.
type llmFunction struct {
	now, was *fn
}

func (l LLM) Propose(ctx context.Context, in Input) ([]*Change, error) {
	exts, ok := languageFiles[in.Language]
	if in.Language == "go" {
		exts, ok = []string{".go"}, true
	}
	if !ok {
		return nil, fmt.Errorf("fixes to code do not read %s", in.Language)
	}
	var shown []llmFunction
	seen := map[string]bool{}
	for _, fr := range in.Frames {
		if !fr.InApp || len(shown) == 4 {
			continue
		}
		now, err := locate(fr, in.Bad, exts)
		if err != nil || now == nil || seen[now.file+now.name()] {
			continue
		}
		seen[now.file+now.name()] = true
		was, _ := counterpart(now, in.Good)
		shown = append(shown, llmFunction{now: now, was: was})
	}
	if len(shown) == 0 {
		return nil, errors.New("no frame of the crash is in a function of the repository, so there is nothing to show a model")
	}
	request := l.request(in, shown)
	var lastErr error
	for range max(1, l.Retries+1) {
		reply, err := l.Model.Complete(ctx, llmSystem, request)
		if err != nil {
			return nil, err
		}
		c, err := applyReply(reply, in.Bad, shown)
		if err == nil {
			return []*Change{c}, nil
		}
		lastErr = err
		request += "\n\nYour last reply could not be used: " + err.Error() + ". Reply again in the form asked, with functions that parse."
	}
	return nil, fmt.Errorf("the model's changes could not be used: %w", lastErr)
}

func (l LLM) request(in Input, shown []llmFunction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A %s service crashed as it ran.\n", in.Language)
	if len(in.Types) > 0 {
		fmt.Fprintf(&b, "Exception: %s\n", strings.Join(in.Types, " caused by "))
	}
	if in.Message != "" {
		fmt.Fprintf(&b, "Message: %s\n", in.Message)
	}
	b.WriteString("Stack, innermost first:\n")
	for _, fr := range in.Frames {
		if fr.InApp {
			fmt.Fprintf(&b, "  %s %s:%d\n", fr.Function, fr.File, fr.Line)
		}
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "\nFILE: %s\nFUNCTION: %s\nNow, at the commit that crashes:\n```\n%s\n```\n", f.now.file, f.now.name(), f.now.text)
		switch {
		case f.was == nil:
			b.WriteString("The culprit commit added this function.\n")
		case bytes.Equal(f.was.text, f.now.text):
			b.WriteString("The culprit commit did not change this function.\n")
		default:
			fmt.Fprintf(&b, "Before the culprit commit:\n```\n%s\n```\n", f.was.text)
		}
	}
	if len(l.Tried) > 0 {
		b.WriteString("\nAlready tried, and why each was not enough:\n")
		for _, t := range l.Tried {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	return b.String()
}

var replyBlock = regexp.MustCompile("(?s)FILE:\\s*(\\S+)\\s*\\n\\s*FUNCTION:\\s*([^\\n]+?)\\s*\\n\\s*```[\\w+#-]*\\n(.*?)\\n\\s*```")

// applyReply puts the functions a reply returns in place of the shown
// ones, re-indented where they sat.
func applyReply(reply string, bad map[string][]byte, shown []llmFunction) (*Change, error) {
	blocks := replyBlock.FindAllStringSubmatch(reply, -1)
	if len(blocks) == 0 {
		return nil, errors.New("it held no FILE, FUNCTION and fenced function")
	}
	type repl struct {
		f    *fn
		text string
	}
	byFile := map[string][]repl{}
	var names []string
	for _, m := range blocks {
		var target *fn
		for _, s := range shown {
			if s.now.file == strings.TrimPrefix(m[1], "./") && (s.now.name() == m[2] || s.now.path[len(s.now.path)-1] == m[2]) {
				target = s.now
			}
		}
		if target == nil {
			return nil, fmt.Errorf("it changed %s in %s, which it was not shown", m[2], m[1])
		}
		byFile[target.file] = append(byFile[target.file], repl{target, reindent(m[3], column(bad[target.file], target.start))})
		names = append(names, target.name())
	}
	c := &Change{Fixer: "llm", Function: strings.Join(names, ", "),
		Why: "a language model's change to " + strings.Join(names, ", ") + ", after the fixers that never write new code had none that held; read it as you would a stranger's pull request"}
	var files []string
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		rs := byFile[file]
		sort.Slice(rs, func(i, j int) bool { return rs[i].f.start > rs[j].f.start })
		src := bad[file]
		after := append([]byte{}, src...)
		line := 0
		for _, r := range rs {
			after = append(append(append([]byte{}, after[:r.f.start]...), r.text...), after[r.f.end:]...)
			line = r.f.line
		}
		if bytes.Equal(after, src) {
			continue
		}
		if err := checkParse(file, src, after); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		c.Edits = append(c.Edits, Edit{Path: file, Line: line, Before: src, After: after})
	}
	if len(c.Edits) == 0 {
		return nil, errors.New("it changed nothing")
	}
	return c, nil
}

// reindent shifts a function a model returned to where the shown one sat:
// its first line starts at the span's start, which keeps the file's own
// indentation before it, and the lines after it move by col, the column
// the function starts at, so a method or nested function keeps its place.
func reindent(text, col string) string {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	have := leading(lines[0])
	lines[0] = strings.TrimLeft(lines[0], " \t")
	for i := 1; i < len(lines); i++ {
		l := strings.TrimPrefix(lines[i], have)
		if strings.TrimSpace(l) == "" {
			l = ""
		} else {
			l = col + l
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

func leading(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// column is the indentation before offset start on its line.
func column(src []byte, start int) string {
	i := bytes.LastIndexByte(src[:start], '\n') + 1
	return leading(string(src[i:start]))
}

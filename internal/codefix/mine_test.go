package codefix

import (
	"context"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/mine"
)

func TestMinedAppliesAPatternAtTheCrash(t *testing.T) {
	src := "package cart\n\nfunc Get(id string) *Cart {\n\tc := carts[id]\n\treturn c\n}\n"
	fixed := "package cart\n\nfunc Get(id string) *Cart {\n\tc := carts[id]\n\tif c == nil {\n\t\treturn &Cart{}\n\t}\n\treturn c\n}\n"
	in := Input{Language: "go", Types: []string{"runtime error: invalid memory address or nil pointer dereference"},
		Message: "runtime error: invalid memory address or nil pointer dereference",
		Frames:  []logs.Frame{{File: "/app/cart.go", Line: 5, InApp: true}},
		Bad:     map[string][]byte{"cart.go": []byte(src)},
		Good:    map[string][]byte{"cart.go": []byte(fixed)}}
	patterns := []mine.Pattern{{
		Language: "go", Message: "fix nil crash in Get",
		BeforeSnippet: "\tc := carts[id]\n\treturn c",
		AfterSnippet:  "\tc := carts[id]\n\tif c == nil {\n\t\treturn &Cart{}\n\t}\n\treturn c",
	}}
	changes, err := Mined{Patterns: patterns}.Propose(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Fixer != "mine" {
		t.Fatalf("got %#v", changes)
	}
	if got := string(changes[0].Edits[0].After); !strings.Contains(got, "c == nil") {
		t.Fatalf("after:\n%s", got)
	}
}

func TestMinedRunsAfterTemplatesInFixers(t *testing.T) {
	// Restore and the template both turn None into {}; the mined pattern
	// uses a different after so it stays a distinct candidate.
	good := map[string][]byte{
		"catalog.py": []byte("def load():\n    items = {}\n    for i in D:\n        items[i] = i\n    return items\n"),
		"README":     []byte("a"),
	}
	bad := map[string][]byte{
		"catalog.py": []byte("def load():\n    items = None\n    for i in D:\n        items[i] = i\n    return items\n"),
		"README":     []byte("b"),
	}
	in := Input{Language: "python", Types: []string{"TypeError"}, Message: "'NoneType' object does not support item assignment",
		Frames: []logs.Frame{{File: "/app/catalog.py", Line: 4, InApp: true}}, Good: good, Bad: bad}
	patterns := []mine.Pattern{{
		Language: "python", Message: "fix assigning into None",
		BeforeSnippet: "    items = None\n    for i in D:",
		AfterSnippet:  "    items = dict()\n    for i in D:",
	}}
	changes, refusals := Fixers(context.Background(), in, Restorer{}, Templates{}, Mined{Patterns: patterns}, Reverter{})
	var names []string
	for _, c := range changes {
		names = append(names, c.Fixer)
	}
	joined := strings.Join(names, ",")
	if !strings.HasPrefix(joined, "restore,template:") || !strings.Contains(joined, ",mine,") || !strings.HasSuffix(joined, ",revert") {
		t.Fatalf("order %v, refusals %v", names, refusals)
	}
}

package codefix

import (
	"context"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// Each template reads its crash, changes only the crash's line or guards
// it, and leaves a file that still parses.
func TestTemplatesFixTheCrashesEveryCodebaseHas(t *testing.T) {
	cases := []struct {
		name, language, file, src, marker string
		types                             []string
		message, want, fixer              string
	}{
		{name: "go: a nil map", language: "go", file: "catalog.go", marker: "items[it.ID]",
			src:   "package main\n\nfunc load() map[string]item {\n\tvar items map[string]item\n\tfor _, it := range defaults {\n\t\titems[it.ID] = it\n\t}\n\treturn items\n}\n",
			types: []string{"assignment to entry in nil map"}, message: "assignment to entry in nil map",
			want: "items := make(map[string]item)", fixer: "template:make-the-map"},
		{name: "go: a nil pointer", language: "go", file: "user.go", marker: "u.Name",
			src:   "package main\n\nimport \"strings\"\n\nfunc name(u *User) (string, error) {\n\treturn strings.ToUpper(u.Name), nil\n}\n",
			types: []string{"runtime error: invalid memory address or nil pointer dereference"}, message: "runtime error: invalid memory address or nil pointer dereference",
			want: "if u == nil {\n\t\treturn \"\", nil\n\t}", fixer: "template:nil-guard"},
		{name: "go: an index past the end", language: "go", file: "first.go", marker: "xs[0]",
			src:   "package main\n\nfunc first(xs []int) int {\n\treturn xs[0]\n}\n",
			types: []string{"runtime error: index out of range [<num>] with length <num>"}, message: "runtime error: index out of range [<num>] with length <num>",
			want: "if len(xs) <= 0 {\n\t\treturn 0\n\t}", fixer: "template:index-guard"},
		{name: "go: a division by zero", language: "go", file: "avg.go", marker: "total / n",
			src:   "package main\n\nfunc avg(total, n int) int {\n\treturn total / n\n}\n",
			types: []string{"runtime error: integer divide by zero"}, message: "runtime error: integer divide by zero",
			want: "if n == 0 {\n\t\treturn 0\n\t}", fixer: "template:zero-guard"},
		{name: "python: assigning into None", language: "python", file: "catalog.py", marker: "items[item",
			src:   "def load():\n    items = None\n    for item in DEFAULTS:\n        items[item['id']] = item\n    return items\n",
			types: []string{"TypeError"}, message: "'NoneType' object does not support item assignment",
			want: "        if items is None:\n            items = {}\n        items[item['id']] = item", fixer: "template:start-empty"},
		{name: "python: a missing key", language: "python", file: "config.py", marker: "config['port']",
			src:   "def port(config):\n    return int(config['port'])\n",
			types: []string{"KeyError"}, message: "'port'", want: "int(config.get('port'))", fixer: "template:get-the-key"},
		{name: "python: an index past the end", language: "python", file: "first.py", marker: "xs[0]",
			src:   "def first(xs):\n    return xs[0]\n",
			types: []string{"IndexError"}, message: "list index out of range", want: "    if len(xs) <= 0:\n        return None\n    return xs[0]", fixer: "template:length-guard"},
		{name: "javascript: a property of undefined", language: "node", file: "server.js", marker: "x.code",
			src:   "function convert(x) {\n  return x.code.toFixed(2);\n}\n",
			types: []string{"TypeError"}, message: "Cannot read properties of undefined (reading 'code')", want: "return x?.code.toFixed(2);", fixer: "template:optional-chaining"},
		{name: "java: a null variable", language: "java", file: "src/Catalog.java", marker: "name.length()",
			src:   "class Catalog {\n  int size() {\n    return name.length();\n  }\n}\n",
			types: []string{"java.lang.NullPointerException"}, message: `Cannot invoke "String.length()" because "name" is null`,
			want: "    if (name == null) {\n        return 0;\n    }\n    return name.length();", fixer: "template:null-guard"},
		{name: "c#: an empty sequence", language: "dotnet", file: "CartStore.cs", marker: "First()",
			src:   "class CartStore {\n  Cart Get() {\n    return carts.First();\n  }\n}\n",
			types: []string{"System.InvalidOperationException"}, message: "Sequence contains no elements", want: "carts.FirstOrDefault();", fixer: "template:or-default"},
		{name: "ruby: indexing nil", language: "ruby", file: "lib/shop.rb", marker: "items[:x]",
			src:   "class Shop\n  def load\n    items = nil\n    items[:x]\n  end\nend\n",
			types: []string{"NoMethodError"}, message: "undefined method '[]' for nil", want: "(items || {})[:x]", fixer: "template:safe-navigation"},
		{name: "ruby: a method of nil", language: "ruby", file: "lib/user.rb", marker: "user.name",
			src:   "def greet(user)\n  \"hi \" + user.name\nend\n",
			types: []string{"NoMethodError"}, message: "undefined method 'name' for nil", want: "user&.name", fixer: "template:safe-navigation"},
		{name: "php: a method of null", language: "php", file: "src/Catalog.php", marker: "->load()",
			src:   "<?php\nclass Catalog {\n  function items() {\n    return $this->db->load();\n  }\n}\n",
			types: []string{"Error"}, message: "Call to a member function load() on null", want: "$this->db?->load();", fixer: "template:nullsafe"},
		{name: "rust: unwrapping None", language: "rust", file: "src/main.rs", marker: "unwrap()",
			src:   "fn first(xs: &[u32]) -> u32 {\n    *xs.first().unwrap()\n}\n",
			types: []string{"panic: called `Option::unwrap()` on a `None` value"}, message: "called `Option::unwrap()` on a `None` value",
			want: "*xs.first().unwrap_or_default()", fixer: "template:unwrap-or-default"},
		{name: "rust: an index past the end", language: "rust", file: "src/catalog.rs", marker: "self.items[0]",
			src:   "impl Catalog {\n    fn first(&self) -> u32 {\n        self.items[0]\n    }\n}\n",
			types: []string{"panic: index out of bounds: the len is <num> but the index is <num>"}, message: "index out of bounds: the len is <num> but the index is <num>",
			want: "self.items.get(0).copied().unwrap_or_default()", fixer: "template:get-or-default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Language: tc.language, Types: tc.types, Message: tc.message,
				Frames: []logs.Frame{{File: "/app/" + tc.file, Line: lineOf(t, tc.src, tc.marker), InApp: true}},
				Bad:    map[string][]byte{tc.file: []byte(tc.src)}, Good: map[string][]byte{tc.file: []byte(tc.src)}}
			changes, err := Templates{}.Propose(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			c := changes[0]
			if c.Fixer != tc.fixer || len(c.Edits) != 1 || !strings.Contains(string(c.Edits[0].After), tc.want) {
				t.Fatalf("%s proposed:\n%s\nwant it to hold:\n%s", c.Fixer, c.Edits[0].After, tc.want)
			}
			if !strings.Contains(c.Why, "stopgap") {
				t.Errorf("the change does not say a guard can hide a bug: %s", c.Why)
			}
		})
	}
}

// A template that does not fit the crash's line proposes nothing.
func TestATemplateThatDoesNotFitProposesNothing(t *testing.T) {
	src := "def load():\n    return compute()\n"
	in := Input{Language: "python", Types: []string{"KeyError"}, Message: "'x'",
		Frames: []logs.Frame{{File: "/app/a.py", Line: 2, InApp: true}}, Bad: map[string][]byte{"a.py": []byte(src)}}
	if changes, err := (Templates{}).Propose(context.Background(), in); err == nil {
		t.Fatalf("proposed %v for a line that reads no key", changes)
	}
}

// The fixers run in order, and a change that leaves the source as an
// earlier one does is dropped; a revert puts back every file the commit
// changed, and a change made at the failing commit applies where the
// branch's head moved on elsewhere in the file.
func TestFixersRunInOrderAndChangesApplyAtTheHead(t *testing.T) {
	good := map[string][]byte{"catalog.py": []byte("def load():\n    items = {}\n    for i in D:\n        items[i] = i\n    return items\n"), "README": []byte("a")}
	bad := map[string][]byte{"catalog.py": []byte("def load():\n    items = None\n    for i in D:\n        items[i] = i\n    return items\n"), "README": []byte("b")}
	in := Input{Language: "python", Types: []string{"TypeError"}, Message: "'NoneType' object does not support item assignment",
		Frames: []logs.Frame{{File: "/app/catalog.py", Line: 4, InApp: true}}, Good: good, Bad: bad}
	changes, refusals := Fixers(context.Background(), in, Restorer{}, Templates{}, Reverter{})
	var fixers []string
	for _, c := range changes {
		fixers = append(fixers, c.Fixer)
	}
	if strings.Join(fixers, ",") != "restore,template:start-empty,revert" || len(refusals) != 0 {
		t.Fatalf("fixers %v, refusals %v", fixers, refusals)
	}
	if revert := changes[2]; len(revert.Edits) != 2 || revert.Edits[0].Path != "README" || string(revert.Edits[0].After) != "a" {
		t.Fatalf("the revert puts back %v", revert.Summary())
	}
	head := map[string][]byte{"catalog.py": append([]byte("import os\n\n"), bad["catalog.py"]...)}
	moved, err := Rebase(changes[0], head)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(moved.Edits[0].After); !strings.HasPrefix(got, "import os\n") || !strings.Contains(got, "items = {}") {
		t.Fatalf("at the head:\n%s", got)
	}
	head["catalog.py"] = []byte("def load():\n    return {}\n")
	if _, err := Rebase(changes[0], head); err == nil {
		t.Fatal("a change applied where the function changed again")
	}
}

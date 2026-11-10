package mine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=A person", "-c", "user.email=person@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A planted fix commit and a single-function change both become patterns
// with the before and after snippets a fixer can apply.
func TestMineLearnsRepairPatternsFromHistory(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")

	write(t, dir, "cart.go", "package cart\n\nfunc Get(id string) *Cart {\n\tc := carts[id]\n\tif c == nil {\n\t\treturn &Cart{}\n\t}\n\treturn c\n}\n")
	write(t, dir, "readme.txt", "shop\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "start")

	// A bug: the nil check is dropped.
	write(t, dir, "cart.go", "package cart\n\nfunc Get(id string) *Cart {\n\tc := carts[id]\n\treturn c\n}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "drop the nil check")

	// The fix that put the guard back, named as one.
	write(t, dir, "cart.go", "package cart\n\nfunc Get(id string) *Cart {\n\tc := carts[id]\n\tif c == nil {\n\t\treturn &Cart{}\n\t}\n\treturn c\n}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "fix nil crash in Get")

	// A single-function change with no fix keyword: Python KeyError guard.
	write(t, dir, "config.py", "def port(cfg):\n    return int(cfg['port'])\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "read the port")
	write(t, dir, "config.py", "def port(cfg):\n    return int(cfg.get('port') or 0)\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "be gentle when port is missing")

	patterns, err := Mine(dir, Options{Cap: 20})
	if err != nil {
		t.Fatal(err)
	}
	var goFix, pyFix bool
	for _, p := range patterns {
		switch {
		case p.Language == "go" && strings.Contains(p.AfterSnippet, "c == nil") && strings.Contains(p.BeforeSnippet, "return c") &&
			strings.Contains(strings.ToLower(p.Message), "fix"):
			goFix = true
		case p.Language == "python" && strings.Contains(p.AfterSnippet, ".get(") && strings.Contains(p.BeforeSnippet, "cfg['port']"):
			pyFix = true
		}
	}
	if !goFix {
		t.Fatalf("expected a Go nil-fix pattern, got %#v", patterns)
	}
	if !pyFix {
		t.Fatalf("expected a Python single-function pattern, got %#v", patterns)
	}
}

func TestMineRespectsPathFilterAndCap(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	write(t, dir, "other/b.go", "package other\n\nfunc B() int { return 1 }\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "start")
	write(t, dir, "pkg/a.go", "package pkg\n\nfunc A() int { return 2 }\n")
	write(t, dir, "other/b.go", "package other\n\nfunc B() int { return 2 }\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "fix off-by-one in both")

	all, err := Mine(dir, Options{Cap: 5, PathFilter: "pkg/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.Language != "go" || !strings.Contains(p.AfterSnippet, "return 2") {
			t.Fatalf("path filter leaked %#v", p)
		}
		if strings.Contains(p.BeforeSnippet, "package other") {
			t.Fatalf("path filter included other/: %#v", p)
		}
	}
	if len(all) == 0 {
		t.Fatal("expected a pattern under pkg/")
	}

	none, err := Mine(dir, Options{Cap: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Cap 1 walks only HEAD; HEAD is the fix commit, so patterns come from it.
	if len(none) == 0 {
		t.Fatal("cap 1 should still see the tip fix commit")
	}
}

func TestMineSkipsUnrelatedCommits(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	write(t, dir, "b.go", "package b\n\nfunc B() {}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "start")
	write(t, dir, "a.go", "package a\n\nfunc A() { return }\n")
	write(t, dir, "b.go", "package b\n\nfunc B() { return }\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "touch two files for a feature")

	patterns, err := Mine(dir, Options{Cap: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patterns {
		if strings.Contains(p.Message, "feature") {
			t.Fatalf("unrelated multi-file commit became a pattern: %#v", p)
		}
	}
}

package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkTree(t *testing.T) {
	tmp := t.TempDir()
	dirs := []string{
		"app", "tests", ".git", "node_modules/foo", "vendor/lib", "__pycache__",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"app/main.py":               "print('hello')",
		"app/config.py":             "DB = 'localhost'",
		"tests/test_main.py":        "assert True",
		"README.md":                 "# test",
		"node_modules/foo/index.js": "module.exports = 1",
		"vendor/lib/util.go":        "package lib",
		"__pycache__/main.pyc":      "binary",
		"app/lock.lock":             "locked",
		"package-lock.json":         "{}",
		"go.sum":                    "checksums",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(tmp, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := WalkTree(tmp, 500)
	if err != nil {
		t.Fatal(err)
	}
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	for _, want := range []string{"app/main.py", "app/config.py", "tests/test_main.py", "README.md"} {
		if !set[want] {
			t.Errorf("expected %s in tree", want)
		}
	}
	for _, skip := range []string{
		"node_modules/foo/index.js",
		"vendor/lib/util.go",
		"__pycache__/main.pyc",
		"app/lock.lock",
		"package-lock.json",
		"go.sum",
	} {
		if set[skip] {
			t.Errorf("expected %s to be filtered out", skip)
		}
	}
}

func TestWalkTreeMaxFiles(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 30; i++ {
		name := filepath.Join(tmp, "file"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := WalkTree(tmp, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) > 5 {
		t.Errorf("expected at most 5 paths, got %d", len(paths))
	}
}

func TestSearch(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "app/main.py"),
		[]byte("import os\nDB_PASSWORD = os.environ['DB_PASSWORD']\nprint(DB_PASSWORD)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "app/config.py"),
		[]byte("DB = 'localhost'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Search(tmp, "DB_PASSWORD", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 matches, got %d", len(results))
	}
	found := false
	for _, r := range results {
		if r.Path == "app/main.py" {
			found = true
			if r.Line < 1 {
				t.Errorf("line should be >= 1, got %d", r.Line)
			}
		}
	}
	if !found {
		t.Error("expected matches in app/main.py")
	}
}

func TestSearchInvalidPattern(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Search(tmp, "[invalid", 10); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestSearchMaxResults(t *testing.T) {
	tmp := t.TempDir()
	content := ""
	for i := 0; i < 100; i++ {
		content += "match line\n"
	}
	if err := os.WriteFile(filepath.Join(tmp, "big.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := Search(tmp, "match", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 10 {
		t.Errorf("expected at most 10 results, got %d", len(results))
	}
}

func TestReadFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "app/main.py"), []byte("print('hello')"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(tmp, "app/main.py")
	if err != nil {
		t.Fatal(err)
	}
	if got != "print('hello')" {
		t.Errorf("got %q, want print('hello')", got)
	}
}

func TestReadFileRejectsAbsolute(t *testing.T) {
	tmp := t.TempDir()
	if _, err := ReadFile(tmp, "/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestReadFileRejectsParentEscape(t *testing.T) {
	tmp := t.TempDir()
	cases := []string{
		"../../../etc/passwd",
		"../../../data/sentinel.db",
		"..",
		"./..",
		"app/../../etc/passwd",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ReadFile(tmp, c); err == nil {
				t.Errorf("expected error for path %q", c)
			}
		})
	}
}

func TestReadFileRejectsRootItself(t *testing.T) {
	tmp := t.TempDir()
	cases := []string{".", "./", ""}
	for _, c := range cases {
		if _, err := ReadFile(tmp, c); err == nil {
			t.Errorf("expected error for path %q", c)
		}
	}
}

func TestReadFileRejectsDirectory(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(tmp, "subdir"); err == nil {
		t.Error("expected error for directory")
	}
}

func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(tmp, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(tmp, "escape"); err == nil {
		t.Error("expected error for symlink that escapes root")
	}
}

func TestIsHex(t *testing.T) {
	cases := map[string]bool{
		"abc123":           true,
		"ABCDEF0123456789": true,
		"xyz":              false,
		"":                 true,
		"40chars_abc":      false,
	}
	for in, want := range cases {
		if got := isHex(in); got != want {
			t.Errorf("isHex(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsBinaryExt(t *testing.T) {
	for _, ext := range []string{".png", ".jpg", ".exe", ".so", ".pyc", ".jar"} {
		if !isBinaryExt(ext) {
			t.Errorf("%s should be binary", ext)
		}
	}
	for _, ext := range []string{".py", ".go", ".js", ".ts", ".rs", ".java", ".md", ".txt", ".yaml"} {
		if isBinaryExt(ext) {
			t.Errorf("%s should NOT be binary", ext)
		}
	}
}

func TestClonerBuilder(t *testing.T) {
	c := NewCloner("tok")
	if c.gitHost != "github.com" {
		t.Errorf("default host = %q", c.gitHost)
	}
	if c.defaultBranch != "main" {
		t.Errorf("default branch = %q", c.defaultBranch)
	}
	c.WithGitHost("gh.example").WithDefaultBranch("develop")
	if c.gitHost != "gh.example" {
		t.Errorf("host = %q", c.gitHost)
	}
	if c.defaultBranch != "develop" {
		t.Errorf("branch = %q", c.defaultBranch)
	}
	// Empty args should not override
	c.WithGitHost("").WithDefaultBranch("")
	if c.gitHost != "gh.example" {
		t.Errorf("empty host should not override, got %q", c.gitHost)
	}
}

func TestClonerGitAuth(t *testing.T) {
	c := NewCloner("")
	if a := c.gitAuth(); a != nil {
		t.Error("empty token should produce nil auth")
	}
	c2 := NewCloner("ghp_xxx")
	a := c2.gitAuth()
	if a == nil {
		t.Fatal("expected non-nil auth")
	}
	if a.Username != "x-access-token" {
		t.Errorf("username = %q", a.Username)
	}
	if a.Password != "ghp_xxx" {
		t.Errorf("password = %q", a.Password)
	}
}

func TestClonerSanitizeErr(t *testing.T) {
	c := NewCloner("ghp_secret")
	err := c.sanitizeErr(nil)
	if err != nil {
		t.Errorf("sanitizeErr(nil) = %v, want nil", err)
	}
	got := c.sanitizeErr(fmt.Errorf("clone https://ghp_secret@github.com/foo/bar: connection refused"))
	if contains(got.Error(), "ghp_secret") {
		t.Errorf("token leaked: %s", got.Error())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

package repo

import (
        "os"
        "path/filepath"
        "testing"
)

func TestWalkTree(t *testing.T) {
        // Create a fake repo structure
        tmp := t.TempDir()
        dirs := []string{
                "app", "tests", ".git", "node_modules/foo", "vendor/lib", "__pycache__",
        }
        for _, d := range dirs {
                _ = os.MkdirAll(filepath.Join(tmp, d), 0755)
        }
        files := map[string]string{
                "app/main.py":             "print('hello')",
                "app/config.py":           "DB = 'localhost'",
                "tests/test_main.py":      "assert True",
                "README.md":               "# test",
                "node_modules/foo/index.js": "module.exports = 1",
                "vendor/lib/util.go":      "package lib",
                "__pycache__/main.cpython.pyc": "binary",
                "app/lock.lock":           "locked",
                "package-lock.json":       "{}",
                "go.sum":                  "checksums",
        }
        for path, content := range files {
                _ = os.WriteFile(filepath.Join(tmp, path), []byte(content), 0644)
        }

        paths, err := WalkTree(tmp, 500)
        if err != nil {
                t.Fatal(err)
        }

        pathSet := make(map[string]bool)
        for _, p := range paths {
                pathSet[p] = true
        }

        // Should be present
        must := []string{"app/main.py", "app/config.py", "tests/test_main.py", "README.md"}
        for _, p := range must {
                if !pathSet[p] {
                        t.Errorf("expected %s in tree", p)
                }
        }

        // Should be filtered out
        mustNot := []string{
                "node_modules/foo/index.js",
                "vendor/lib/util.go",
                "__pycache__/main.cpython.pyc",
                "app/lock.lock",
                "package-lock.json",
                "go.sum",
        }
        for _, p := range mustNot {
                if pathSet[p] {
                        t.Errorf("expected %s to be filtered out", p)
                }
        }
}

func TestSearch(t *testing.T) {
        tmp := t.TempDir()
        _ = os.MkdirAll(filepath.Join(tmp, "app"), 0755)
        if err := os.WriteFile(filepath.Join(tmp, "app/main.py"), []byte("import os\nDB_PASSWORD = os.environ['DB_PASSWORD']\nprint(DB_PASSWORD)\n"), 0644); err != nil {
                t.Fatal(err)
        }
        if err := os.WriteFile(filepath.Join(tmp, "app/config.py"), []byte("DB = 'localhost'\n"), 0644); err != nil {
                t.Fatal(err)
        }

        results, err := Search(tmp, "DB_PASSWORD", 50)
        if err != nil {
                t.Fatal(err)
        }
        if len(results) < 2 {
                t.Fatalf("expected at least 2 matches, got %d", len(results))
        }

        // Should find matches in app/main.py
        foundMain := false
        for _, r := range results {
                if r.Path == "app/main.py" {
                        foundMain = true
                        if r.Line < 1 {
                                t.Errorf("line should be >= 1, got %d", r.Line)
                        }
                }
        }
        if !foundMain {
                t.Error("expected matches in app/main.py")
        }
}

func TestSearchMaxResults(t *testing.T) {
        tmp := t.TempDir()
        content := ""
        for i := 0; i < 100; i++ {
                content += "match line\n"
        }
        _ = os.WriteFile(filepath.Join(tmp, "big.txt"), []byte(content), 0644)

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
        _ = os.MkdirAll(filepath.Join(tmp, "app"), 0755)
        if err := os.WriteFile(filepath.Join(tmp, "app/main.py"), []byte("print('hello')"), 0644); err != nil {
                t.Fatal(err)
        }

        content, err := ReadFile(tmp, "app/main.py")
        if err != nil {
                t.Fatal(err)
        }
        if content != "print('hello')" {
                t.Errorf("got %q, want print('hello')", content)
        }
}

func TestIsBinaryExt(t *testing.T) {
        binary := []string{".png", ".jpg", ".exe", ".so", ".pyc", ".jar"}
        for _, ext := range binary {
                if !isBinaryExt(ext) {
                        t.Errorf("%s should be binary", ext)
                }
        }
        text := []string{".py", ".go", ".js", ".ts", ".rs", ".java", ".md", ".txt", ".yaml"}
        for _, ext := range text {
                if isBinaryExt(ext) {
                        t.Errorf("%s should NOT be binary", ext)
                }
        }
}

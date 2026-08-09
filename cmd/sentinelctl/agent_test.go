package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestRepo creates a temp directory with a small Go source file that
// the code-intel parser can index. Returns the repo path and a cleanup func.
func makeTestRepo(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sentinelctl-agent-test-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	// Write a small Go file with a function + a call so the index has
	// something to find.
	src := `package demo

import "fmt"

// Greet prints a greeting for name.
func Greet(name string) {
    msg := buildMessage(name)
    fmt.Println(msg)
}

func buildMessage(name string) string {
    return "hello, " + name
}
`
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte(src), 0644); err != nil {
		cleanup()
		t.Fatalf("write greet.go: %v", err)
	}
	return dir, cleanup
}

// TestAgentSymbols verifies `sentinelctl agent symbols <repo>` lists all
// functions in the test repo.
func TestAgentSymbols(t *testing.T) {
	repo, cleanup := makeTestRepo(t)
	defer cleanup()

	// handleAgentSymbols uses fs.Parse; we invoke it directly with args.
	// It writes to stdout; capture via os.Pipe.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	handleAgentSymbols([]string{repo})

	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"Greet", "buildMessage", "greet.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("symbols output missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestAgentCallers verifies `sentinelctl agent callers <repo> <name>` finds
// the call site of buildMessage (called by Greet).
func TestAgentCallers(t *testing.T) {
	repo, cleanup := makeTestRepo(t)
	defer cleanup()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	handleAgentCallers([]string{repo, "buildMessage"})

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "Greet") {
		t.Errorf("callers output should contain 'Greet' as caller of buildMessage\ngot:\n%s", out)
	}
	if !strings.Contains(out, "caller") {
		t.Errorf("callers output should mention 'caller'\ngot:\n%s", out)
	}
}

// TestAgentDefinition verifies `sentinelctl agent definition <repo> <name>`
// returns the definition site.
func TestAgentDefinition(t *testing.T) {
	repo, cleanup := makeTestRepo(t)
	defer cleanup()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	handleAgentDefinition([]string{repo, "Greet"})

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "greet.go") {
		t.Errorf("definition output should reference greet.go\ngot:\n%s", out)
	}
	if !strings.Contains(out, "func") {
		t.Errorf("definition output should mention 'func'\ngot:\n%s", out)
	}
}

// TestAgentCallees verifies `sentinelctl agent callees <repo> <name>` finds
// the callees of Greet (buildMessage + Println).
func TestAgentCallees(t *testing.T) {
	repo, cleanup := makeTestRepo(t)
	defer cleanup()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	handleAgentCallees([]string{repo, "Greet"})

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "buildMessage") {
		t.Errorf("callees output should contain buildMessage\ngot:\n%s", out)
	}
}

// TestAgentCodeIntelJSON verifies `sentinelctl agent codeintel --json` emits
// valid JSON with the expected structure.
func TestAgentCodeIntelJSON(t *testing.T) {
	repo, cleanup := makeTestRepo(t)
	defer cleanup()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	c := NewClient("http://localhost:8000", "", true, 0) // jsonOut = true
	c.handleAgentCodeIntel([]string{repo})

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	// Crude JSON shape check — the index_test.go in internal/codeintel
	// covers the full structure; here we just verify the CLI emits something
	// JSON-shaped with the right top-level keys.
	for _, want := range []string{`"symbols"`, `"call_graph"`, `"Greet"`} {
		if !strings.Contains(out, want) {
			t.Errorf("codeintel --json output missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestAgentHelpNoArgs verifies `sentinelctl agent` with no subcommand prints
// the help text and exits 0.
func TestAgentHelpNoArgs(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	c := NewClient("http://localhost:8000", "", false, 0)
	// handleAgent with no args calls printAgentHelp then os.Exit(0). We
	// can't easily capture os.Exit in a test without restructure, so just
	// verify printAgentHelp directly.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("printAgentHelp panicked: %v", r)
		}
	}()
	printAgentHelp()
	_ = c
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	for _, want := range []string{"investigate", "codeintel", "symbols", "callers", "callees", "definition"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent help missing %q\noutput:\n%s", want, out)
		}
	}
}

package agents

import (
	"strings"
	"testing"
)

// editblock_test.go: direct unit tests for the Aider-style SEARCH/REPLACE
// parser and the whole-file-replacement fallback.
//
// Why this file exists: before this, ParseWholeFileReplacement was only
// exercised indirectly by TestFixProposer_WholeFileReplacement (which only
// covers the happy path — bare file path + code block). The loosened
// SEARCH/REPLACE regex added in PR #70 (<<<<<<< \S* instead of
// <<<<<<< SEARCH) had no direct test, and ApplyEditBlocks' layered
// matching (exact → whitespace-normalized → fuzzy) was only tested via
// the fix_proposer retry loop.
//
// This file pins:
//   - ParseWholeFileReplacement: bare path, markdown code fence, no match.
//   - ParseEditBlocks: SEARCH/REPLACE markers, multi-block, multi-file,
//     and the loosened <<<<<<< \S* regex.
//   - ApplyEditBlocks: exact / whitespace-normalized / fuzzy layers,
//     and the actionable error message when all layers fail.
//   - normalizeFilePath: container paths, suffix matching, leading-dir
//     stripping, no-match returns as-is.
//   - HasEditBlocks / HasWholeFile predicates.

// --- ParseWholeFileReplacement -------------------------------------------

func TestParseWholeFileReplacement_BarePath(t *testing.T) {
	output := "app/main.py\n```python\ndef foo():\n    return 1\n```\n"
	path, content := ParseWholeFileReplacement(output)
	if path != "app/main.py" {
		t.Errorf("path = %q, want app/main.py", path)
	}
	want := "def foo():\n    return 1"
	if content != want {
		t.Errorf("content = %q, want %q", content, want)
	}
}

func TestParseWholeFileReplacement_PathWithLeadingReasoning(t *testing.T) {
	// Reasoning models produce prose before the answer. stripReasoning
	// should leave the file path + code block intact.
	output := "<think>I should fix the divide function.</think>\n\n" +
		"app/main.py\n" +
		"```python\n" +
		"def divide(a, b):\n" +
		"    if b == 0:\n" +
		"        raise ValueError(\"zero\")\n" +
		"    return a / b\n" +
		"```\n"
	path, content := ParseWholeFileReplacement(output)
	if path != "app/main.py" {
		t.Errorf("path = %q, want app/main.py", path)
	}
	if !strings.Contains(content, "def divide") {
		t.Errorf("content should contain the divide function, got: %q", content)
	}
}

func TestParseWholeFileReplacement_FallbackCodeBlock(t *testing.T) {
	// No file path line — just a code block whose content looks like code.
	// Should return ("", content).
	output := "Here is the fix:\n```python\ndef foo():\n    return 1\n```\n"
	path, content := ParseWholeFileReplacement(output)
	if path != "" {
		t.Errorf("path = %q, want empty (no file path line)", path)
	}
	if !strings.Contains(content, "def foo") {
		t.Errorf("content should contain def foo, got: %q", content)
	}
}

func TestParseWholeFileReplacement_NoMatch(t *testing.T) {
	// Pure prose, no code block, no file path.
	output := "I think the issue is with the database connection."
	path, content := ParseWholeFileReplacement(output)
	if path != "" || content != "" {
		t.Errorf("expected (\"\", \"\"), got (%q, %q)", path, content)
	}
}

func TestParseWholeFileReplacement_MultipleCodeBlocksPicksFirst(t *testing.T) {
	// When multiple file paths + code blocks appear, the first wins.
	output := "app/main.py\n```python\ndef foo(): pass\n```\n\nlib/util.py\n```python\ndef bar(): pass\n```\n"
	path, content := ParseWholeFileReplacement(output)
	if path != "app/main.py" {
		t.Errorf("path = %q, want app/main.py (first block)", path)
	}
	if !strings.Contains(content, "def foo") {
		t.Errorf("content should be from first block, got: %q", content)
	}
}

func TestParseWholeFileReplacement_CodeBlockWithoutCodeKeyword(t *testing.T) {
	// A code block that doesn't look like code (no def/func/class) and has
	// no preceding file path line → should NOT match the fallback.
	output := "```\njust some text\n```"
	_, content := ParseWholeFileReplacement(output)
	if content != "" {
		t.Errorf("expected empty content for non-code block, got: %q", content)
	}
}

// --- ParseEditBlocks (SEARCH/REPLACE) ------------------------------------

func TestParseEditBlocks_ClassicMarkers(t *testing.T) {
	output := `app/main.py
<<<<<<< SEARCH
def foo():
    return 1
=======
def foo():
    return 2
>>>>>>> REPLACE
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.FilePath != "app/main.py" {
		t.Errorf("FilePath = %q, want app/main.py", b.FilePath)
	}
	if !strings.Contains(b.Search, "return 1") {
		t.Errorf("Search should contain 'return 1', got: %q", b.Search)
	}
	if !strings.Contains(b.Replace, "return 2") {
		t.Errorf("Replace should contain 'return 2', got: %q", b.Replace)
	}
}

func TestParseEditBlocks_LoosenedMarkers(t *testing.T) {
	// PR #70 loosened the regex from `<<<<<<< SEARCH` to `<<<<<<< \S*`
	// so non-standard markers like `<<<<<<< FIX` also work. This test
	// pins that behavior — if someone tightens the regex back to SEARCH,
	// this test fails.
	output := `app/main.py
<<<<<<< FIX
def foo():
    return 1
=======
def foo():
    return 2
>>>>>>> APPLY
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block with loosened markers, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0].Replace, "return 2") {
		t.Errorf("Replace should contain 'return 2', got: %q", blocks[0].Replace)
	}
}

func TestParseEditBlocks_BareMarkers(t *testing.T) {
	// `<<<<<<<` alone (no marker word) should also match — the regex
	// accepts `\S*` which includes zero characters.
	output := `app/main.py
<<<<<<<
def foo():
    return 1
=======
def foo():
    return 2
>>>>>>>
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block with bare markers, got %d", len(blocks))
	}
}

func TestParseEditBlocks_MultipleBlocksForOneFile(t *testing.T) {
	output := `app/main.py
<<<<<<< SEARCH
def foo():
    return 1
=======
def foo():
    return 2
>>>>>>> REPLACE

<<<<<<< SEARCH
def bar():
    return 3
=======
def bar():
    return 4
>>>>>>> REPLACE
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].FilePath != "app/main.py" || blocks[1].FilePath != "app/main.py" {
		t.Errorf("both blocks should be for app/main.py, got %q and %q", blocks[0].FilePath, blocks[1].FilePath)
	}
}

func TestParseEditBlocks_MultipleFiles(t *testing.T) {
	output := `app/main.py
<<<<<<< SEARCH
a = 1
=======
a = 2
>>>>>>> REPLACE

lib/util.py
<<<<<<< SEARCH
b = 3
=======
b = 4
>>>>>>> REPLACE
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].FilePath != "app/main.py" {
		t.Errorf("block 0 FilePath = %q, want app/main.py", blocks[0].FilePath)
	}
	if blocks[1].FilePath != "lib/util.py" {
		t.Errorf("block 1 FilePath = %q, want lib/util.py", blocks[1].FilePath)
	}
}

func TestParseEditBlocks_StripsReasoning(t *testing.T) {
	output := `<think>I need to fix the return value.</think>
<reasoning>The bug is in foo.</reasoning>

app/main.py
<<<<<<< SEARCH
def foo():
    return 1
=======
def foo():
    return 2
>>>>>>> REPLACE
`
	blocks := ParseEditBlocks(output)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block after stripping reasoning, got %d", len(blocks))
	}
}

func TestParseEditBlocks_NoBlocks(t *testing.T) {
	output := "I think the issue is with the database."
	blocks := ParseEditBlocks(output)
	if blocks != nil {
		t.Errorf("expected nil for non-SEARCH/REPLACE output, got %v", blocks)
	}
}

// --- ApplyEditBlocks (layered matching) ----------------------------------

func TestApplyEditBlocks_ExactMatch(t *testing.T) {
	original := "def foo():\n    return 1\n"
	blocks := []EditBlock{{
		FilePath: "app/main.py",
		Search:   "def foo():\n    return 1",
		Replace:  "def foo():\n    return 2",
	}}
	result := ApplyEditBlocks(original, blocks)
	if !result.Success {
		t.Fatalf("expected success, got failure: %s", result.FailedReason)
	}
	if !strings.Contains(result.NewContent, "return 2") {
		t.Errorf("NewContent should contain 'return 2', got: %q", result.NewContent)
	}
	if strings.Contains(result.NewContent, "return 1") {
		t.Errorf("NewContent should NOT contain 'return 1' anymore, got: %q", result.NewContent)
	}
	if result.BlocksApplied != 1 {
		t.Errorf("BlocksApplied = %d, want 1", result.BlocksApplied)
	}
}

func TestApplyEditBlocks_WhitespaceNormalizedMatch(t *testing.T) {
	// Search has different whitespace (tabs vs spaces, extra spaces) but
	// the same token structure. Layer 2 should still match.
	original := "def foo():\n    return 1\n"
	blocks := []EditBlock{{
		FilePath: "app/main.py",
		Search:   "def foo():\n\t\treturn 1", // tabs instead of spaces
		Replace:  "def foo():\n    return 2",
	}}
	result := ApplyEditBlocks(original, blocks)
	if !result.Success {
		t.Fatalf("expected success via whitespace-normalized match, got: %s", result.FailedReason)
	}
	if !strings.Contains(result.NewContent, "return 2") {
		t.Errorf("NewContent should contain 'return 2', got: %q", result.NewContent)
	}
}

func TestApplyEditBlocks_FuzzyMatch(t *testing.T) {
	// Search has a small typo (1-char difference). Levenshtein similarity
	// should be above 0.6, so layer 3 should match.
	original := "def calculate_total():\n    return 42\n"
	blocks := []EditBlock{{
		FilePath: "app/main.py",
		Search:   "def calculate_totel():\n    return 42", // 'al' → 'el' typo
		Replace:  "def calculate_total():\n    return 0",
	}}
	result := ApplyEditBlocks(original, blocks)
	if !result.Success {
		t.Fatalf("expected success via fuzzy match, got: %s", result.FailedReason)
	}
	if !strings.Contains(result.NewContent, "return 0") {
		t.Errorf("NewContent should contain 'return 0', got: %q", result.NewContent)
	}
}

func TestApplyEditBlocks_NoMatchReturnsActionableError(t *testing.T) {
	// Search is completely different from anything in the original.
	// All three layers should fail. The error message must include
	// the failed block, the file path, and the closest match (so the
	// LLM has something to work with on retry).
	original := "def foo():\n    return 1\n"
	blocks := []EditBlock{{
		FilePath: "app/main.py",
		Search:   "def completely_different():\n    return 999",
		Replace:  "def completely_different():\n    return 0",
	}}
	result := ApplyEditBlocks(original, blocks)
	if result.Success {
		t.Fatal("expected failure, got success")
	}
	if result.FailedBlock == nil {
		t.Error("FailedBlock should be set on failure")
	}
	if !strings.Contains(result.FailedReason, "app/main.py") {
		t.Errorf("FailedReason should mention the file path, got: %s", result.FailedReason)
	}
	if !strings.Contains(result.FailedReason, "SEARCH/REPLACE") {
		t.Errorf("FailedReason should mention SEARCH/REPLACE, got: %s", result.FailedReason)
	}
}

func TestApplyEditBlocks_EmptyBlocks(t *testing.T) {
	result := ApplyEditBlocks("anything", nil)
	if result.Success {
		t.Error("expected failure for empty blocks")
	}
	if !strings.Contains(result.FailedReason, "no edit blocks") {
		t.Errorf("FailedReason should mention 'no edit blocks', got: %s", result.FailedReason)
	}
}

// --- normalizeFilePath ----------------------------------------------------

func TestNormalizeFilePath_ExactMatch(t *testing.T) {
	fetched := map[string]string{"app/main.py": "code"}
	if got := normalizeFilePath("app/main.py", fetched); got != "app/main.py" {
		t.Errorf("got %q, want app/main.py", got)
	}
}

func TestNormalizeFilePath_ContainerPath(t *testing.T) {
	// LLM used /app/app/main.py (container path), key is app/main.py.
	fetched := map[string]string{"app/main.py": "code"}
	if got := normalizeFilePath("/app/app/main.py", fetched); got != "app/main.py" {
		t.Errorf("got %q, want app/main.py", got)
	}
}

func TestNormalizeFilePath_SuffixMatch(t *testing.T) {
	// LLM used a path with extra leading dirs.
	fetched := map[string]string{"lib/utils.py": "code"}
	if got := normalizeFilePath("foo/bar/lib/utils.py", fetched); got != "lib/utils.py" {
		t.Errorf("got %q, want lib/utils.py", got)
	}
}

func TestNormalizeFilePath_NoMatchReturnsAsIs(t *testing.T) {
	// LLM hallucinated a path. We return it as-is so the apply step
	// fails with a clear "file not found in fetched source" error,
	// NOT a silent substitution.
	fetched := map[string]string{
		"app/main.py":    "code",
		"src/handler.go": "code",
	}
	got := normalizeFilePath("nonexistent/file.go", fetched)
	if got != "nonexistent/file.go" {
		t.Errorf("got %q, want nonexistent/file.go returned as-is (don't silently substitute)", got)
	}
}

func TestNormalizeFilePath_EmptyPath(t *testing.T) {
	fetched := map[string]string{"app/main.py": "code"}
	if got := normalizeFilePath("", fetched); got != "" {
		t.Errorf("got %q, want empty string for empty input", got)
	}
}

// --- HasEditBlocks / HasWholeFile ----------------------------------------

func TestHasEditBlocks(t *testing.T) {
	if !HasEditBlocks("<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE") {
		t.Error("expected HasEditBlocks=true for SEARCH/REPLACE markers")
	}
	if HasEditBlocks("just prose, no markers") {
		t.Error("expected HasEditBlocks=false for plain prose")
	}
}

func TestHasWholeFile(t *testing.T) {
	if !HasWholeFile("app/main.py\n```python\ndef foo(): pass\n```\n") {
		t.Error("expected HasWholeFile=true for file path + code block")
	}
	if HasWholeFile("just prose, no code block") {
		t.Error("expected HasWholeFile=false for plain prose")
	}
}

package agents

import (
        "math"
        "testing"
)

func TestParseEditBlocks_BasicBlock(t *testing.T) {
        input := `app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400, detail="Divisor cannot be zero")
    result = a / b
>>>>>>> REPLACE`

        blocks := ParseEditBlocks(input)
        if len(blocks) != 1 {
                t.Fatalf("expected 1 block, got %d", len(blocks))
        }
        if blocks[0].FilePath != "app/main.py" {
                t.Errorf("expected file path app/main.py, got %q", blocks[0].FilePath)
        }
        if blocks[0].Search != "    result = a / b\n" {
                t.Errorf("expected search '    result = a / b\\n', got %q", blocks[0].Search)
        }
        if blocks[0].Replace != "    if b == 0:\n        raise HTTPException(status_code=400, detail=\"Divisor cannot be zero\")\n    result = a / b\n" {
                t.Errorf("unexpected replace content: %q", blocks[0].Replace)
        }
}

func TestParseEditBlocks_MultipleBlocks(t *testing.T) {
        input := `app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400)
    result = a / b
>>>>>>> REPLACE

app/other.py
<<<<<<< SEARCH
old line
=======
new line
>>>>>>> REPLACE`

        blocks := ParseEditBlocks(input)
        if len(blocks) != 2 {
                t.Fatalf("expected 2 blocks, got %d", len(blocks))
        }
        if blocks[0].FilePath != "app/main.py" {
                t.Errorf("block 0: expected app/main.py, got %q", blocks[0].FilePath)
        }
        if blocks[1].FilePath != "app/other.py" {
                t.Errorf("block 1: expected app/other.py, got %q", blocks[1].FilePath)
        }
}

func TestParseEditBlocks_WithReasoning(t *testing.T) {
        input := `We are given the root cause: division by zero in the /divide endpoint.
The fix is to add a check for b=0 before performing the division.

{"fix_type":"code_fix","description":"Add zero check","pr_title":"[Sentinel] Fix ZeroDivisionError"}

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400, detail="Divisor cannot be zero")
    result = a / b
>>>>>>> REPLACE`

        blocks := ParseEditBlocks(input)
        if len(blocks) != 1 {
                t.Fatalf("expected 1 block, got %d (reasoning should be stripped)", len(blocks))
        }
        if blocks[0].FilePath != "app/main.py" {
                t.Errorf("expected app/main.py, got %q", blocks[0].FilePath)
        }
}

func TestParseEditBlocks_WithThinkBlocks(t *testing.T) {
        input := `<think>
The user wants me to fix a division by zero error.
The fix is straightforward: check if b == 0 before dividing.
</think>

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400)
    result = a / b
>>>>>>> REPLACE`

        blocks := ParseEditBlocks(input)
        if len(blocks) != 1 {
                t.Fatalf("expected 1 block, got %d (think block should be stripped)", len(blocks))
        }
}

func TestParseEditBlocks_NoBlocks(t *testing.T) {
        input := `This is just prose with no SEARCH/REPLACE markers.`
        blocks := ParseEditBlocks(input)
        if blocks != nil {
                t.Fatalf("expected nil, got %d blocks", len(blocks))
        }
}

func TestApplyEditBlocks_ExactMatch(t *testing.T) {
        original := `def divide(a, b):
    result = a / b
    return result
`
        blocks := []EditBlock{
                {
                        FilePath: "app/main.py",
                        Search:   "    result = a / b\n",
                        Replace:  "    if b == 0:\n        raise HTTPException(status_code=400)\n    result = a / b\n",
                },
        }

        result := ApplyEditBlocks(original, blocks)
        if !result.Success {
                t.Fatalf("expected success, got failure: %s", result.FailedReason)
        }
        expected := `def divide(a, b):
    if b == 0:
        raise HTTPException(status_code=400)
    result = a / b
    return result
`
        if result.NewContent != expected {
                t.Errorf("expected:\n%q\ngot:\n%q", expected, result.NewContent)
        }
}

func TestApplyEditBlocks_WhitespaceNormalizedMatch(t *testing.T) {
        original := `def divide(a, b):
    result   =   a   /   b
    return result
`
        blocks := []EditBlock{
                {
                        FilePath: "app/main.py",
                        Search:   "    result = a / b\n",  // single spaces
                        Replace:  "    if b == 0:\n        raise HTTPException(status_code=400)\n    result = a / b\n",
                },
        }

        result := ApplyEditBlocks(original, blocks)
        if !result.Success {
                t.Fatalf("expected success via whitespace normalization, got failure: %s", result.FailedReason)
        }
}

func TestApplyEditBlocks_FuzzyMatch(t *testing.T) {
        original := `def divide(a, b):
    result = a / b  # compute division
    return result
`
        blocks := []EditBlock{
                {
                        FilePath: "app/main.py",
                        Search:   "    result = a / b\n",  // missing comment
                        Replace:  "    if b == 0:\n        raise HTTPException(status_code=400)\n    result = a / b  # compute division\n",
                },
        }

        result := ApplyEditBlocks(original, blocks)
        if !result.Success {
                t.Fatalf("expected success via fuzzy matching, got failure: %s", result.FailedReason)
        }
}

func TestApplyEditBlocks_NoMatch(t *testing.T) {
        original := `def divide(a, b):
    return a / b
`
        blocks := []EditBlock{
                {
                        FilePath: "app/main.py",
                        Search:   "    this line does not exist in the file\n",
                        Replace:  "    replacement\n",
                },
        }

        result := ApplyEditBlocks(original, blocks)
        if result.Success {
                t.Fatal("expected failure, got success")
        }
        if result.FailedReason == "" {
                t.Error("expected non-empty failure reason")
        }
        if result.FailedBlock == nil {
                t.Error("expected non-nil failed block")
        }
}

func TestParseWholeFileReplacement(t *testing.T) {
        input := "app/main.py\n" +
                "```python\n" +
                "def divide(a, b):\n" +
                "    if b == 0:\n" +
                "        raise HTTPException(status_code=400)\n" +
                "    return a / b\n" +
                "```"

        path, content := ParseWholeFileReplacement(input)
        if path != "app/main.py" {
                t.Errorf("expected path app/main.py, got %q", path)
        }
        if content == "" {
                t.Error("expected non-empty content")
        }
        if !contains(content, "def divide") {
                t.Errorf("expected content to contain 'def divide', got %q", content)
        }
}

func TestLevenshteinSimilarity(t *testing.T) {
        tests := []struct {
                a, b     string
                expected float64
        }{
                {"hello", "hello", 1.0},
                {"hello", "world", 0.2},
                {"same", "same", 1.0},
                {"", "", 1.0},  // identical empty strings
                {"a", "b", 0.0},
                {"abcd", "abce", 0.75},
        }
        for _, tt := range tests {
                got := levenshteinSimilarity(tt.a, tt.b)
                if math.Abs(got-tt.expected) > 0.01 {
                        t.Errorf("levenshteinSimilarity(%q, %q) = %.2f, want %.2f", tt.a, tt.b, got, tt.expected)
                }
        }
}

func TestHasEditBlocks(t *testing.T) {
        if !HasEditBlocks("<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE") {
                t.Error("expected true for output with markers")
        }
        if HasEditBlocks("just plain text") {
                t.Error("expected false for output without markers")
        }
}

func TestExtractMetadataJSON(t *testing.T) {
        input := `{"fix_type":"code_fix","description":"Add zero check","pr_title":"[Sentinel] Fix"}

app/main.py
<<<<<<< SEARCH
    result = a / b
=======
    if b == 0:
        raise HTTPException(status_code=400)
    result = a / b
>>>>>>> REPLACE`

        metadata := extractMetadataJSON(input)
        if metadata == nil {
                t.Fatal("expected non-nil metadata")
        }
        if ft, _ := metadata["fix_type"].(string); ft != "code_fix" {
                t.Errorf("expected fix_type=code_fix, got %q", ft)
        }
        if desc, _ := metadata["description"].(string); desc != "Add zero check" {
                t.Errorf("expected description='Add zero check', got %q", desc)
        }
}

func contains(s, substr string) bool {
        for i := 0; i+len(substr) <= len(s); i++ {
                if s[i:i+len(substr)] == substr {
                        return true
                }
        }
        return false
}

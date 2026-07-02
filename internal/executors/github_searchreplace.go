// Package executors — SEARCH/REPLACE block parser + fuzzy matcher.
// Parses the LLM's output for SEARCH/REPLACE blocks (Aider format) and
// applies them to the file content. Three matching strategies, tried in
// order:
//   1. tryExactMatch        — exact substring match
//   2. tryWhitespaceInsensitiveMatch — same match ignoring whitespace
//   3. tryFuzzyMatch        — Levenshtein-based fuzzy match for typos
// Also includes syntax validation (brace balance) for the patched file.
package executors

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "fmt"
        "log"
        "path/filepath"
        "strings"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

func (e *GitHubExecutor) applySearchReplace(ctx context.Context, repo, branch, path string, changes []schemas.SearchReplace) (string, error) {
        content, err := e.GetFileContent(ctx, repo, path, branch)
        if err != nil {
                return "", fmt.Errorf("fetch file %s: %w", path, err)
        }

        originalContent := content
        applied := 0
        failedBlocks := []int{}      // track which block indices failed
        var matchErrors []string     // collect errors for retry feedback

        for i, ch := range changes {
                // Strategy 1: Exact match

                matched, replacement := tryExactMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        logging.Info("block : matched (exact)", "i", i)
                        continue
                }

                // Strategy 2: Whitespace-insensitive match

                matched, replacement = tryWhitespaceInsensitiveMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        logging.Info("block : matched (whitespace-insensitive)", "i", i)
                        continue
                }

                // Strategy 3: Fuzzy match (Levenshtein >80%)

                matched, replacement = tryFuzzyMatch(content, ch.Search, ch.Replace)
                if matched {
                        content = replacement
                        applied++
                        logging.Info("block : matched (fuzzy)", "i", i)
                        continue
                }

                // If the search text is absent BUT the replacement is present,
                // the fix was already applied in a previous retry. Treat as success
                // instead of looping forever regenerating the same fix.

                if ch.Replace != "" && strings.Contains(content, ch.Replace) {
                        applied++
                        logging.Info("block : already applied (replacement text found in content) — treating as success", "i", i)
                        continue
                }

                // All strategies failed

                failedBlocks = append(failedBlocks, i)

                // so the LLM sees what didn't match (was only logged, not fed back).

                preview := ch.Search
                if len(preview) > 120 {
                        preview = preview[:120] + "..."
                }
                errMsg := fmt.Sprintf("block %d: search text not found (tried exact, whitespace-insensitive, fuzzy). Search preview: %q", i, preview)
                matchErrors = append(matchErrors, errMsg)
                logging.Info("", "errmsg", errMsg)
        }

        if applied == 0 {
                return "", fmt.Errorf("no search/replace blocks matched (0/%d applied): %s",
                        len(changes), strings.Join(matchErrors, "; "))
        }

        // If some blocks failed, check whether the applied blocks are "meaningful"
        // (i.e. they actually change code logic, not just add an import).
        // If ALL applied blocks are just import changes and the actual fix blocks
        // failed, reject the PR — shipping an unused import is worse than no PR.

        if len(failedBlocks) > 0 {
                hasMeaningfulChange := false
                for i, ch := range changes {
                        // Skip blocks that failed

                        isFailed := false
                        for _, fb := range failedBlocks {
                                if fb == i {
                                        isFailed = true
                                        break
                                }
                        }
                        if isFailed {
                                continue
                        }
                        // Check if this block does more than just change imports
                        // An import-only change typically has "import" in the search text
                        // and doesn't contain function/class definitions

                        if !isImportOnlyChange(ch.Search, ch.Replace) {
                                hasMeaningfulChange = true
                                break
                        }
                }
                if !hasMeaningfulChange {
                        return "", fmt.Errorf("only import-only blocks matched, all meaningful fix blocks failed — not shipping a useless partial fix")
                }
                logging.Error("partial fix: / blocks applied,  failed (but has meaningful changes)", "value", applied, len(changes), len(failedBlocks))
        }

        // Syntax validation

        if syntaxErr := validateSyntax(path, content); syntaxErr != nil {
                logging.Error("SYNTAX VALIDATION FAILED", "syntaxerr", syntaxErr)
                // Try applying blocks one at a time, skipping syntax-breaking ones

                content = originalContent
                goodBlocks := 0
                var droppedBlocks []int
                for i, ch := range changes {
                        matched, trial := tryAllStrategies(content, ch.Search, ch.Replace)
                        if !matched {
                                continue
                        }
                        if err := validateSyntax(path, trial); err != nil {
                                logging.Error("block  produces syntax error — skipping", "value", i, err)
                                droppedBlocks = append(droppedBlocks, i)
                                continue
                        }
                        content = trial
                        goodBlocks++
                }
                if goodBlocks == 0 {
                        return "", fmt.Errorf("all search/replace blocks produce syntax errors — LLM generated broken code")
                }

                // the operator (and the LLM retry loop via logs) knows the fix is
                // incomplete. We still ship the partial fix (better than nothing)
                // but the warning ensures it's visible.

                if len(droppedBlocks) > 0 {
                        log.Printf("[github] WARNING: partial fix — recovered %d/%d blocks, dropped %d due to syntax errors (indices: %v). The PR may not fully resolve the incident.",
                                goodBlocks, len(changes), len(droppedBlocks), droppedBlocks)
                }
                logging.Info("recovered: applied / blocks after syntax recovery", "value", goodBlocks, len(changes))
                return content, nil
        }

        logging.Info("applied / search/replace blocks to  (syntax validated ✓)", "value", applied, len(changes), path)
        return content, nil
}

// tryExactMatch tries to find ch.Search in content exactly once.
// Returns (matched, newContent).

func tryExactMatch(content, search, replace string) (bool, string) {
        count := strings.Count(content, search)
        if count != 1 {
                return false, content
        }
        return true, strings.Replace(content, search, replace, 1)
}

// tryWhitespaceInsensitiveMatch strips leading/trailing whitespace from each
// line of both the search text and the file content, then tries to match.
// This handles the #1 LLM error: getting indentation wrong.


func tryWhitespaceInsensitiveMatch(content, search, replace string) (bool, string) {
        // Normalize: strip leading/trailing whitespace from each line

        normalize := func(s string) string {
                lines := strings.Split(s, "\n")
                for i, line := range lines {
                        lines[i] = strings.TrimSpace(line)
                }
                return strings.Join(lines, "\n")
        }

        normalizedSearch := normalize(search)
        normalizedContent := normalize(content)

        count := strings.Count(normalizedContent, normalizedSearch)
        if count != 1 {
                return false, content
        }

        // Found exactly one match in normalized space. Now we need to find the
        // corresponding location in the original content and replace it.
        // Strategy: find the starting line by matching the first non-empty
        // normalized line, then verify the rest match.

        searchLines := strings.Split(search, "\n")
        contentLines := strings.Split(content, "\n")

        for startLine := 0; startLine <= len(contentLines)-len(searchLines); startLine++ {
                match := true
                for j, searchLine := range searchLines {
                        if strings.TrimSpace(contentLines[startLine+j]) != strings.TrimSpace(searchLine) {
                                match = false
                                break
                        }
                }
                if match {
                        // Found the match at startLine. Replace those lines with the
                        // replacement text, preserving the original indentation of the
                        // first line.
                        // IN ADDITION to the replacement's own indent, producing 4+8=12 spaces.

                        originalIndent := getIndent(contentLines[startLine])
                        replaceLines := strings.Split(replace, "\n")
                        for k := range replaceLines {
                                if k == 0 {
                                        // First line: use original indent

                                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                                } else if strings.TrimSpace(replaceLines[k]) != "" {
                                        // Other lines: re-base to originalIndent (strip replacement's own indent).

                                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                                }
                        }
                        newLines := append([]string{}, contentLines[:startLine]...)
                        newLines = append(newLines, replaceLines...)
                        newLines = append(newLines, contentLines[startLine+len(searchLines):]...)
                        return true, strings.Join(newLines, "\n")
                }
        }

        return false, content
}

// tryFuzzyMatch uses a simple similarity score to find the closest match.
// If the best match has >80% similarity (Levenshtein-based), use it.
// safety, but we enable it because free models produce more search text
// errors than GPT-4. The 80% threshold is conservative enough to avoid
// false matches.

func tryFuzzyMatch(content, search, replace string) (bool, string) {
        searchLines := strings.Split(search, "\n")
        contentLines := strings.Split(content, "\n")

        if len(searchLines) == 0 || len(searchLines) > len(contentLines) {
                return false, content
        }

        for i, sl := range searchLines {
                preview := strings.TrimSpace(sl)
                if len(preview) > 80 {
                        preview = preview[:80] + "..."
                }
                logging.Info("fuzzy: search line", "value", i, preview)
        }

        bestScore := 0.0
        bestStart := -1

        // Slide a window over the content lines

        for start := 0; start <= len(contentLines)-len(searchLines); start++ {
                totalSim := 0.0
                for j := 0; j < len(searchLines); j++ {
                        sim := lineSimilarity(strings.TrimSpace(contentLines[start+j]), strings.TrimSpace(searchLines[j]))
                        totalSim += sim
                }
                avgSim := totalSim / float64(len(searchLines))
                if avgSim > bestScore {
                        bestScore = avgSim
                        bestStart = start
                }
        }

        if bestStart >= 0 {
                for j := 0; j < len(searchLines); j++ {
                        fileLine := strings.TrimSpace(contentLines[bestStart+j])
                        searchLine := strings.TrimSpace(searchLines[j])
                        sim := lineSimilarity(fileLine, searchLine)
                        if sim < 1.0 {
                                filePreview := fileLine
                                if len(filePreview) > 80 { filePreview = filePreview[:80] + "..." }
                                searchPreview := searchLine
                                if len(searchPreview) > 80 { searchPreview = searchPreview[:80] + "..." }
                                logging.Info("fuzzy: line  diff (%.0f%%): file= search=", "value", j, sim*100, filePreview, searchPreview)
                        }
                }
        }

        logging.Info("fuzzy: best score=%.1f%% at line  (threshold=85%%)", "value", bestScore*100, bestStart+1)

        // 10-line search block could be completely wrong and still match, applying
        // the fix to the wrong function (e.g. payment fix applied to refund function).
        // 85% is closer to Aider's safe default while still tolerating minor
        // whitespace + identifier differences from free models.

        if bestScore < 0.85 || bestStart == -1 {
                return false, content
        }

        logging.Info("fuzzy match: %.1f%% similarity at line", "value", bestScore*100, bestStart+1)

        // Replace the matched lines with the replacement text.
        // IN ADDITION to the replacement's own indent, producing 4+8=12 spaces
        // and breaking Python block structure (IndentationError).
        // Fix: strip the replacement's leading indent, then prepend originalIndent ONCE.

        originalIndent := getIndent(contentLines[bestStart])
        replaceLines := strings.Split(replace, "\n")
        for k := range replaceLines {
                if k == 0 {
                        // First line: use original indent

                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                } else if strings.TrimSpace(replaceLines[k]) != "" {
                        // Other lines: re-base to originalIndent (strip replacement's own indent,
                        // then prepend originalIndent + any relative indent from the first line).

                        replaceLines[k] = originalIndent + strings.TrimSpace(replaceLines[k])
                }
        }
        newLines := append([]string{}, contentLines[:bestStart]...)
        newLines = append(newLines, replaceLines...)
        newLines = append(newLines, contentLines[bestStart+len(searchLines):]...)
        return true, strings.Join(newLines, "\n")
}

// tryAllStrategies tries exact, whitespace-insensitive, and fuzzy in order.
// Used by the syntax-validation recovery path.

func tryAllStrategies(content, search, replace string) (bool, string) {
        if matched, result := tryExactMatch(content, search, replace); matched {
                return true, result
        }
        if matched, result := tryWhitespaceInsensitiveMatch(content, search, replace); matched {
                return true, result
        }
        if matched, result := tryFuzzyMatch(content, search, replace); matched {
                return true, result
        }
        return false, content
}

// isImportOnlyChange returns true if the search/replace only changes an import
// statement (e.g. "from fastapi import FastAPI" → "from fastapi import FastAPI, HTTPException").
// These changes are useless on their own without the actual fix.

func isImportOnlyChange(search, replace string) bool {
        searchTrimmed := strings.TrimSpace(search)
        replaceTrimmed := strings.TrimSpace(replace)
        return (strings.Contains(searchTrimmed, "import ") || strings.Contains(searchTrimmed, "from ")) &&
                (strings.Contains(replaceTrimmed, "import ") || strings.Contains(replaceTrimmed, "from "))
}

// getIndent returns the leading whitespace of a line.

func getIndent(line string) string {
        for i, c := range line {
                if c != ' ' && c != '\t' {
                        return line[:i]
                }
        }
        return line
}

// lineSimilarity computes a similarity score between two strings [0.0, 1.0]
// using a simple Levenshtein-distance-based ratio.

func lineSimilarity(a, b string) float64 {
        if a == b {
                return 1.0
        }
        if len(a) == 0 || len(b) == 0 {
                return 0.0
        }
        dist := levenshtein(a, b)
        maxLen := len(a)
        if len(b) > maxLen {
                maxLen = len(b)
        }
        return 1.0 - float64(dist)/float64(maxLen)
}

// levenshtein computes the edit distance between two strings.

func levenshtein(a, b string) int {
        la := len(a)
        lb := len(b)
        if la == 0 {
                return lb
        }
        if lb == 0 {
                return la
        }

        // Use a single row for DP (memory optimization)

        prev := make([]int, lb+1)
        curr := make([]int, lb+1)
        for j := 0; j <= lb; j++ {
                prev[j] = j
        }
        for i := 1; i <= la; i++ {
                curr[0] = i
                for j := 1; j <= lb; j++ {
                        cost := 1
                        if a[i-1] == b[j-1] {
                                cost = 0
                        }
                        curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
                }
                prev, curr = curr, prev
        }
        return prev[lb]
}

func min3(a, b, c int) int {
        if b < a {
                a = b
        }
        if c < a {
                a = c
        }
        return a
}

// validateSyntax checks if the given file content is syntactically valid
// for the file's language. Currently supports:
//   - Python (.py): uses `python3 -c "import ast; ast.parse(code)"`
//   - Go (.go): uses `gofmt -e` (checks for parse errors)
//   - JavaScript/TypeScript (.js/.ts): uses `node --check`
//   - JSON (.json): uses json.Unmarshal
//   - YAML (.yaml/.yml): uses yaml.Unmarshal
// For unknown file types, validation is skipped (returns nil).
// sometimes generates code with missing closing quotes, unclosed parens,
// or broken indentation. This catches those before writing to GitHub.

func validateSyntax(path, content string) error {
        ext := strings.ToLower(filepath.Ext(path))
        switch ext {
        case ".py":
                return validatePython(content)
        case ".go":
                return validateGo(content)
        case ".js", ".ts", ".jsx", ".tsx":
                return validateJS(content)
        case ".json":
                return validateJSON(content)
        case ".yaml", ".yml":
                return validateYAML(content)

        // Catches the dominant LLM mistake (unbalanced delimiters) for Java, Rust,
        // Ruby, C#, C/C++, PHP, Kotlin, Swift, Scala. Same validator as Python/JS —
        // these languages share enough lexical structure that brace/paren/bracket
        // balance + string/quote closure catches the bulk of broken output.

        case ".java", ".kt", ".kts":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".rs":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".rb":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".cs":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".php":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".swift":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        case ".scala", ".sc":
                return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
        default:
                // Unknown file type — skip validation

                return nil
        }
}

// validateSyntax, validatePython, validateGo, validateJS, validateJSON,
// validateYAML are implemented in syntax_validate.go (pure-Go, no exec).


package executors

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// applySearchReplace fetches the current file content from the repo, applies
// each search/replace block using layered matching (exact →
// whitespace-insensitive → fuzzy), validates the result is syntactically
// correct, and returns the modified content.
//
// Matching strategies, tried in order per block:
//  1. tryExactMatch — exact substring match (must be unique).
//  2. tryWhitespaceInsensitiveMatch — strip leading/trailing whitespace
//     per line. Handles the #1 LLM error: getting indentation wrong.
//  3. tryFuzzyMatch — Levenshtein similarity >=85%.
//
// If all strategies fail for a block but the replacement text is already
// present, the block is treated as already-applied (a previous retry did
// it). This prevents infinite retry loops regenerating the same fix.
//
// If some blocks failed but the applied blocks are "meaningful" (not just
// import changes), we ship the partial fix. If only import-only blocks
// matched and the actual fix blocks failed, we reject — shipping an unused
// import is worse than no PR.
func (e *GitHubExecutor) applySearchReplace(ctx context.Context, repo, branch, path string, changes []schemas.SearchReplace) (string, error) {
	content, err := e.GetFileContent(ctx, repo, path, branch)
	if err != nil {
		return "", fmt.Errorf("fetch file %s: %w", path, err)
	}

	originalContent := content
	applied := 0
	var failedBlocks []int
	var matchErrors []string

	for i, ch := range changes {
		if matched, replacement := tryExactMatch(content, ch.Search, ch.Replace); matched {
			content = replacement
			applied++
			logging.Info("search/replace block matched (exact)", "block", i)
			continue
		}
		if matched, replacement := tryWhitespaceInsensitiveMatch(content, ch.Search, ch.Replace); matched {
			content = replacement
			applied++
			logging.Info("search/replace block matched (whitespace-insensitive)", "block", i)
			continue
		}
		if matched, replacement := tryFuzzyMatch(content, ch.Search, ch.Replace); matched {
			content = replacement
			applied++
			logging.Info("search/replace block matched (fuzzy)", "block", i)
			continue
		}

		// Already applied? Treat as success instead of looping forever.
		if ch.Replace != "" && strings.Contains(content, ch.Replace) {
			applied++
			logging.Info("search/replace block already applied (replacement found in content)", "block", i)
			continue
		}

		failedBlocks = append(failedBlocks, i)
		preview := ch.Search
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		errMsg := fmt.Sprintf("block %d: search text not found (tried exact, whitespace-insensitive, fuzzy). Search preview: %q", i, preview)
		matchErrors = append(matchErrors, errMsg)
		logging.Info("search/replace block failed to match", "block", i, "preview", preview)
	}

	if applied == 0 {
		return "", fmt.Errorf("no search/replace blocks matched (0/%d applied): %s",
			len(changes), strings.Join(matchErrors, "; "))
	}

	// If some blocks failed, check whether the applied blocks are
	// "meaningful" (i.e. they actually change code logic, not just add an
	// import). If ALL applied blocks are import-only and the actual fix
	// blocks failed, reject the PR.
	if len(failedBlocks) > 0 {
		hasMeaningfulChange := false
		for i, ch := range changes {
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
			if !isImportOnlyChange(ch.Search, ch.Replace) {
				hasMeaningfulChange = true
				break
			}
		}
		if !hasMeaningfulChange {
			return "", fmt.Errorf("only import-only blocks matched, all meaningful fix blocks failed — not shipping a useless partial fix")
		}
		logging.Error("partial fix: applied blocks, some failed (but has meaningful changes)",
			"applied", applied, "total", len(changes), "failed", len(failedBlocks))
	}

	// Syntax validation. If the patched content is broken, retry applying
	// blocks one at a time, skipping syntax-breaking ones. We still ship
	// the partial fix (better than nothing) but log the warning so the
	// operator (and the LLM retry loop via logs) knows the fix is incomplete.
	if syntaxErr := validateSyntax(path, content); syntaxErr != nil {
		logging.Error("syntax validation failed on patched content", "err", syntaxErr)
		content = originalContent
		goodBlocks := 0
		var droppedBlocks []int
		for i, ch := range changes {
			matched, trial := tryAllStrategies(content, ch.Search, ch.Replace)
			if !matched {
				continue
			}
			if err := validateSyntax(path, trial); err != nil {
				logging.Error("block produces syntax error — skipping", "block", i, "err", err)
				droppedBlocks = append(droppedBlocks, i)
				continue
			}
			content = trial
			goodBlocks++
		}
		if goodBlocks == 0 {
			return "", fmt.Errorf("all search/replace blocks produce syntax errors — LLM generated broken code")
		}
		if len(droppedBlocks) > 0 {
			logging.Warn("partial fix — recovered blocks, dropped some due to syntax errors",
				"recovered", goodBlocks, "total", len(changes), "dropped", droppedBlocks)
		}
		logging.Info("recovered after syntax recovery", "applied", goodBlocks, "total", len(changes))
		return content, nil
	}

	logApply(applied, len(changes), path)
	return content, nil
}

// tryExactMatch tries to find search in content exactly once.
// Returns (matched, newContent).
func tryExactMatch(content, search, replace string) (bool, string) {
	count := strings.Count(content, search)
	if count != 1 {
		return false, content
	}
	return true, strings.Replace(content, search, replace, 1)
}

// tryWhitespaceInsensitiveMatch strips leading/trailing whitespace from
// each line of both the search text and the file content, then tries to
// match. Handles the #1 LLM error: getting indentation wrong.
func tryWhitespaceInsensitiveMatch(content, search, replace string) (bool, string) {
	normalize := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimSpace(line)
		}
		return strings.Join(lines, "\n")
	}

	normalizedSearch := normalize(search)
	normalizedContent := normalize(content)
	if strings.Count(normalizedContent, normalizedSearch) != 1 {
		return false, content
	}

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
			originalIndent := getIndent(contentLines[startLine])
			replaceLines := strings.Split(replace, "\n")
			for k := range replaceLines {
				if k == 0 || strings.TrimSpace(replaceLines[k]) != "" {
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

// tryFuzzyMatch uses a Levenshtein-based similarity score to find the
// closest match. If the best match has >=85% similarity, use it.
func tryFuzzyMatch(content, search, replace string) (bool, string) {
	searchLines := strings.Split(search, "\n")
	contentLines := strings.Split(content, "\n")

	if len(searchLines) == 0 || len(searchLines) > len(contentLines) {
		return false, content
	}

	bestScore := 0.0
	bestStart := -1
	for start := 0; start <= len(contentLines)-len(searchLines); start++ {
		totalSim := 0.0
		for j := 0; j < len(searchLines); j++ {
			totalSim += lineSimilarity(strings.TrimSpace(contentLines[start+j]), strings.TrimSpace(searchLines[j]))
		}
		avgSim := totalSim / float64(len(searchLines))
		if avgSim > bestScore {
			bestScore = avgSim
			bestStart = start
		}
	}

	if bestScore < 0.85 || bestStart == -1 {
		return false, content
	}

	originalIndent := getIndent(contentLines[bestStart])
	replaceLines := strings.Split(replace, "\n")
	for k := range replaceLines {
		if k == 0 || strings.TrimSpace(replaceLines[k]) != "" {
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

// isImportOnlyChange returns true if the search/replace only changes an
// import statement (e.g. "from fastapi import FastAPI" → "from fastapi
// import FastAPI, HTTPException"). These changes are useless on their own
// without the actual fix.
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
// using a Levenshtein-distance-based ratio.
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

// levenshtein computes the edit distance between two strings using
// single-row DP for O(min(n,m)) space.
func levenshtein(a, b string) int {
	la := len(a)
	lb := len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

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

// min3 returns the minimum of three ints.
func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// validateSyntax dispatches to the per-language validator based on file
// extension. See syntax_validate.go for the per-language implementations.
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
	case ".java", ".kt", ".kts", ".rs", ".rb", ".cs", ".php", ".swift",
		".scala", ".sc", ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx":
		// Brace-balance validation catches the dominant LLM mistake
		// (unbalanced delimiters) for C-family languages.
		return validateBraceBalance(content, []rune{'{', '}', '(', ')', '[', ']'})
	default:
		return nil
	}
}

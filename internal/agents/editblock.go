// Package agents — SEARCH/REPLACE edit block parser. v4.3.0.
// Replaces the fragile JSON-based fix format with Aider-style SEARCH/REPLACE
// blocks. This is the format used by Aider, Cline, RooCode, and OpenAI's
// Codex CLI — the industry standard for LLM-driven code edits.
// Why not JSON? Code is full of quotes, braces, and newlines that break JSON
// escaping. JSON fails catastrophically on truncation. LLMs (especially free
// reasoning models like nemotron) produce 12k+ chars of reasoning before the
// JSON, which gets truncated by max_tokens.
// SEARCH/REPLACE blocks are:
//   - Truncation-tolerant (a partial block can still match)
//   - No escaping needed (code stays literal)
//   - Unambiguous (<<<<<<< SEARCH is hard to miss)
//   - Seen in training data by most models (Aider/Cline are public)
// Format:
//   path/to/file.py
//   <<<<<<< SEARCH
//   original code here
//   =======
//   new code here
//   >>>>>>> REPLACE
// Multiple blocks per file are allowed. Multiple files are allowed.
// The parser extracts all blocks, applies them with layered matching:
//   1. Exact match (verbatim)
//   2. Whitespace-normalized match (collapse all whitespace)
//   3. Fuzzy match (Levenshtein distance, find closest lines)
// On failure, returns an actionable error with the closest match found,
// which the fix proposer feeds back to the LLM for a retry.
package agents

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// EditBlock is one SEARCH/REPLACE block for one file.

type EditBlock struct {
	FilePath string
	Search   string
	Replace  string
}

// EditResult is the outcome of applying edit blocks to a file.

type EditResult struct {
	Success      bool
	NewContent   string
	BlocksTotal  int
	BlocksApplied int
	FailedBlock  *EditBlock   // nil if all succeeded
	FailedReason string       // actionable error message
	ClosestMatch string       // best match found (for LLM feedback)
}

// editBlockRe matches one SEARCH/REPLACE block.
// Allows flexible whitespace around the markers.

var editBlockRe = regexp.MustCompile(
	`(?ms)^<<<<<<<\s*SEARCH\s*$\n(.*?)^=======\s*$\n(.*?)^>>>>>>>\s*REPLACE\s*$`,
)

// filePathRe matches a file path on the line before <<<<<<< SEARCH.
// It's a non-marker line that looks like a path (contains / or . or ends with a known extension).

var filePathLineRe = regexp.MustCompile(`^[\w./-]+\.\w+$`)

// ParseEditBlocks extracts all SEARCH/REPLACE blocks from the LLM output.
// The LLM may produce reasoning/thinking before the blocks — we strip that.
// Returns a slice of EditBlock, one per SEARCH/REPLACE pair found.
// Each block is associated with the most recent file path line above it.

func ParseEditBlocks(llmOutput string) []EditBlock {
	// Strip reasoning/thinking blocks that reasoning models (nemotron, DeepSeek-R1) produce

	output := stripReasoning(llmOutput)

	// Find all SEARCH/REPLACE blocks

	matches := editBlockRe.FindAllStringSubmatchIndex(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var blocks []EditBlock
	for _, idx := range matches {
		searchStart, searchEnd := idx[2], idx[3]
		replaceStart, replaceEnd := idx[4], idx[5]
		search := output[searchStart:searchEnd]
		replace := output[replaceStart:replaceEnd]

		// Find the file path: scan backwards from the SEARCH marker
		// to find the most recent line that looks like a file path.

		beforeBlock := output[:idx[0]]
		filePath := findFilePathBefore(beforeBlock)

		blocks = append(blocks, EditBlock{
			FilePath: filePath,
			Search:   search,
			Replace:  replace,
		})
	}

	return blocks
}

// stripReasoning removes <think>...</think>, <reasoning>...</reasoning>,
// and other reasoning model artifacts from the output.

func stripReasoning(text string) string {
	// Remove <think>...</think> blocks

	thinkRe := regexp.MustCompile(`(?s)<think>.*?</think>`)
	text = thinkRe.ReplaceAllString(text, "")

	// Remove <reasoning>...</reasoning> blocks

	reasoningRe := regexp.MustCompile(`(?s)<reasoning>.*?</reasoning>`)
	text = reasoningRe.ReplaceAllString(text, "")

	// If the output has no SEARCH/REPLACE markers at all, return as-is
	// (it might be a whole-file replacement or JSON metadata)

	if !strings.Contains(text, "<<<<<<< SEARCH") && !strings.Contains(text, "<<<<<<<") {
		return text
	}

	// Strip everything before the first file path + SEARCH marker.
	// Reasoning models produce prose like "We are given..." before the actual blocks.
	// Find the first occurrence of a line that looks like a file path followed by <<<<<<< SEARCH

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check if this line is a file path and the next line starts the SEARCH block

		if filePathLineRe.MatchString(trimmed) {
			if i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "<<<<<<<") {
				// Found the start of the edit blocks — return from here

				return strings.Join(lines[i:], "\n")
			}
		}
		// Also check if this line itself starts with <<<<<<< (file path might be missing)

		if strings.HasPrefix(trimmed, "<<<<<<<") {
			return strings.Join(lines[i:], "\n")
		}
	}

	return text
}

// findFilePathBefore scans backwards from the end of `before` to find
// the most recent line that looks like a file path.

func findFilePathBefore(before string) string {
	lines := strings.Split(before, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, "=======") || strings.HasPrefix(line, ">>>>>>>") {
			continue
		}
		// Check if it looks like a file path

		if filePathLineRe.MatchString(line) {
			return line
		}
		// If it contains a dot and no spaces, it might be a path

		if strings.Contains(line, ".") && !strings.Contains(line, " ") {
			return line
		}
	}
	return ""
}

// ApplyEditBlocks applies a slice of EditBlocks to the given file content.
// Uses layered matching: exact → whitespace-normalized → fuzzy.
// Returns an EditResult with the new content or an actionable error.

func ApplyEditBlocks(originalContent string, blocks []EditBlock) *EditResult {
	if len(blocks) == 0 {
		return &EditResult{
			Success:      false,
			FailedReason: "no edit blocks provided",
		}
	}

	result := originalContent
	applied := 0

	for i, block := range blocks {
		// Trim trailing newline from search/replace (LLMs often add one)

		search := strings.TrimRight(block.Search, "\n")
		replace := strings.TrimRight(block.Replace, "\n")

		// Layer 1: Exact match

		if idx := strings.Index(result, search); idx >= 0 {
			result = result[:idx] + replace + result[idx+len(search):]
			applied++
			continue
		}

		// Layer 2: Whitespace-normalized match

		if newResult, ok := applyWhitespaceNormalized(result, search, replace); ok {
			result = newResult
			applied++
			continue
		}

		// Layer 3: Fuzzy match (Levenshtein-based line matching)

		if newResult, closestMatch, ok := applyFuzzyMatch(result, search, replace); ok {
			result = newResult
			applied++
			continue
		} else {
			// All layers failed — return actionable error

			closest := closestMatch
			if closest == "" {
				closest = findClosestLines(result, search, 5)
			}
			return &EditResult{
				Success:      false,
				NewContent:   result,
				BlocksTotal:  len(blocks),
				BlocksApplied: applied,
				FailedBlock:  &blocks[i],
				FailedReason: fmt.Sprintf(
					"SEARCH/REPLACE block #%d for %s failed to match.\n\n"+
						"The SEARCH section must exactly match existing code including all whitespace and indentation.\n\n"+
						"SEARCH block that failed:\n%s\n\n"+
						"Closest match found in file:\n%s",
					i+1, block.FilePath, search, closest,
				),
				ClosestMatch: closest,
			}
		}
	}

	return &EditResult{
		Success:       true,
		NewContent:    result,
		BlocksTotal:   len(blocks),
		BlocksApplied: applied,
	}
}

// applyWhitespaceNormalized tries to match the search block after normalizing
// all whitespace (collapsing runs of whitespace to single spaces).

func applyWhitespaceNormalized(content, search, replace string) (string, bool) {
	normalize := func(s string) string {
		// Collapse all whitespace runs to single space, trim leading/trailing

		re := regexp.MustCompile(`\s+`)
		return strings.TrimSpace(re.ReplaceAllString(s, " "))
	}

	normContent := normalize(content)
	normSearch := normalize(search)

	if normSearch == "" {
		return "", false
	}

	idx := strings.Index(normContent, normSearch)
	if idx < 0 {
		return "", false
	}

	// Find the actual position in the original content by tracking
	// character positions through the normalization.
	// This is approximate — we find the first line of the search block
	// and the last line, then replace everything between them.

	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 {
		return "", false
	}

	firstLine := strings.TrimSpace(searchLines[0])
	lastLine := strings.TrimSpace(searchLines[len(searchLines)-1])

	// Find first line in content

	contentLines := strings.Split(content, "\n")
	startIdx := -1
	for i, line := range contentLines {
		if strings.TrimSpace(line) == firstLine {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return "", false
	}

	// Find last line starting from startIdx

	endIdx := -1
	for i := startIdx; i < len(contentLines); i++ {
		if strings.TrimSpace(contentLines[i]) == lastLine {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return "", false
	}

	// Replace lines startIdx through endIdx with the replace block
	// Preserve original indentation of the first line

	origIndent := ""
	for _, c := range contentLines[startIdx] {
		if c == ' ' || c == '\t' {
			origIndent += string(c)
		} else {
			break
		}
	}

	replaceLines := strings.Split(replace, "\n")
	newLines := make([]string, 0, len(contentLines)-((endIdx-startIdx+1))+len(replaceLines))
	newLines = append(newLines, contentLines[:startIdx]...)
	for i, rl := range replaceLines {
		if i == 0 {
			newLines = append(newLines, origIndent+strings.TrimLeft(rl, " \t"))
		} else {
			newLines = append(newLines, rl)
		}
	}
	newLines = append(newLines, contentLines[endIdx+1:]...)

	return strings.Join(newLines, "\n"), true
}

// applyFuzzyMatch tries to find the closest matching lines using Levenshtein distance.
// Returns (newContent, closestMatch, success).

func applyFuzzyMatch(content, search, replace string) (string, string, bool) {
	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 {
		return "", "", false
	}

	contentLines := strings.Split(content, "\n")
	if len(contentLines) == 0 {
		return "", "", false
	}

	// Find the best matching window in the content for the search block.
	// We slide a window of len(searchLines) over the content and compute
	// the average Levenshtein similarity.

	bestScore := 0.0
	bestStart := -1
	bestEnd := -1

	windowSize := len(searchLines)
	if windowSize > len(contentLines) {
		windowSize = len(contentLines)
	}

	for i := 0; i <= len(contentLines)-windowSize; i++ {
		score := 0.0
		for j := 0; j < windowSize && j < len(searchLines); j++ {
			sim := levenshteinSimilarity(
				strings.TrimSpace(contentLines[i+j]),
				strings.TrimSpace(searchLines[j]),
			)
			score += sim
		}
		score /= float64(windowSize)

		if score > bestScore {
			bestScore = score
			bestStart = i
			bestEnd = i + windowSize - 1
		}
	}

	// Require at least 60% similarity to accept the fuzzy match

	if bestScore < 0.6 || bestStart < 0 {
		closestMatch := ""
		if bestStart >= 0 {
			closestMatch = strings.Join(contentLines[bestStart:bestEnd+1], "\n")
		}
		return "", closestMatch, false
	}

	// Replace the matched window with the replace block
	// Preserve original indentation

	origIndent := ""
	for _, c := range contentLines[bestStart] {
		if c == ' ' || c == '\t' {
			origIndent += string(c)
		} else {
			break
		}
	}

	replaceLines := strings.Split(replace, "\n")
	newLines := make([]string, 0, len(contentLines)-((bestEnd-bestStart+1))+len(replaceLines))
	newLines = append(newLines, contentLines[:bestStart]...)
	for i, rl := range replaceLines {
		if i == 0 {
			newLines = append(newLines, origIndent+strings.TrimLeft(rl, " \t"))
		} else {
			newLines = append(newLines, rl)
		}
	}
	newLines = append(newLines, contentLines[bestEnd+1:]...)

	closestMatch := strings.Join(contentLines[bestStart:bestEnd+1], "\n")
	return strings.Join(newLines, "\n"), closestMatch, true
}

// findClosestLines finds the lines in content that are most similar to the search block.
// Used for error feedback when all matching layers fail.

func findClosestLines(content, search string, numLines int) string {
	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 {
		return ""
	}

	contentLines := strings.Split(content, "\n")
	if len(contentLines) == 0 {
		return ""
	}

	firstSearchLine := strings.TrimSpace(searchLines[0])
	bestScore := 0.0
	bestIdx := 0

	for i, line := range contentLines {
		score := levenshteinSimilarity(strings.TrimSpace(line), firstSearchLine)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	// Return numLines lines around the best match

	start := bestIdx - numLines/2
	if start < 0 {
		start = 0
	}
	end := start + numLines
	if end > len(contentLines) {
		end = len(contentLines)
	}

	return strings.Join(contentLines[start:end], "\n")
}

// levenshteinSimilarity returns a similarity score between 0 and 1.
// 1 = identical, 0 = completely different.
// Uses the Levenshtein distance normalized by the longer string's length.

func levenshteinSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	dist := levenshteinDistance(a, b)
	maxLen := math.Max(float64(len(a)), float64(len(b)))
	return 1.0 - (float64(dist) / maxLen)
}

// levenshteinDistance computes the edit distance between two strings.
// Uses the standard dynamic programming algorithm with O(n*m) time and O(min(n,m)) space.

func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Use two rows to save memory

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
			curr[j] = min3(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
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

// ParseWholeFileReplacement handles the fallback where the LLM returns the
// entire fixed file instead of SEARCH/REPLACE blocks. This is the simplest
// and most robust format — no matching needed.
// The LLM output should contain the file path on the first line, then the
// full file content in a code block:
//   app/main.py
//   ```python
//   import os
//   ...full file content...
//   ```
// Returns (filePath, content) or ("", "") if no whole-file replacement found.

func ParseWholeFileReplacement(llmOutput string) (string, string) {
	output := stripReasoning(llmOutput)

	// Look for: filename followed by a code block

	lines := strings.Split(output, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !filePathLineRe.MatchString(trimmed) {
			continue
		}
		// Check if the next non-empty line starts a code block

		for j := i + 1; j < len(lines) && j < i+3; j++ {
			nextTrimmed := strings.TrimSpace(lines[j])
			if strings.HasPrefix(nextTrimmed, "```") {
				// Extract everything between ``` and ```

				blockStart := j + 1
				blockEnd := -1
				for k := blockStart; k < len(lines); k++ {
					if strings.HasPrefix(strings.TrimSpace(lines[k]), "```") {
						blockEnd = k
						break
					}
				}
				if blockEnd > blockStart {
					content := strings.Join(lines[blockStart:blockEnd], "\n")
					return trimmed, content
				}
			}
		}
	}

	// Also try: just a code block with the path in the fence language

	codeBlockRe := regexp.MustCompile("(?s)```(?:\\w+[/\\.\\w]*)?\\n(.*?)```")
	matches := codeBlockRe.FindAllStringSubmatch(output, -1)
	for _, m := range matches {
		if len(m) >= 2 && strings.Contains(m[1], "def ") || strings.Contains(m[1], "func ") || strings.Contains(m[1], "class ") {
			// Looks like code content — but we don't have a file path
			// Return empty path so the caller can use the default

			return "", m[1]
		}
	}

	return "", ""
}

// HasEditBlocks returns true if the LLM output contains SEARCH/REPLACE markers.

func HasEditBlocks(llmOutput string) bool {
	return strings.Contains(llmOutput, "<<<<<<<") && strings.Contains(llmOutput, ">>>>>>>")
}

// HasWholeFile returns true if the LLM output looks like a whole-file replacement.

func HasWholeFile(llmOutput string) bool {
	_, content := ParseWholeFileReplacement(llmOutput)
	return content != ""
}

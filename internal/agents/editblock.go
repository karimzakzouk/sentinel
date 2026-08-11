package agents

// editblock.go: Aider-style SEARCH/REPLACE edit-block parser. Replaces the
// fragile JSON-based fix format with the format used by Aider, Cline, RooCode,
// and OpenAI's Codex CLI — the industry standard for LLM-driven code edits.
//
// Why not JSON? Code is full of quotes, braces, and newlines that break JSON
// escaping. JSON fails catastrophically on truncation. LLMs (especially free
// reasoning models) produce 12k+ chars of reasoning before the JSON, which
// gets truncated by max_tokens.
//
// SEARCH/REPLACE blocks are:
//   - Truncation-tolerant (a partial block can still match)
//   - No escaping needed (code stays literal)
//   - Unambiguous (<<<<<<< SEARCH is hard to miss)
//   - Seen in training data by most models
//
// Format:
//
//      path/to/file.py
//      <<<<<<< SEARCH
//      original code here
//      =======
//      new code here
//      >>>>>>> REPLACE
//
// Multiple blocks per file are allowed. Multiple files are allowed.
// ApplyEditBlocks uses layered matching:
//  1. Exact match (verbatim)
//  2. Whitespace-normalized match (collapse all whitespace)
//  3. Fuzzy match (Levenshtein distance, find closest lines)

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
	Success       bool
	NewContent    string
	BlocksTotal   int
	BlocksApplied int
	FailedBlock   *EditBlock // nil if all succeeded
	FailedReason  string     // actionable error message
	ClosestMatch  string     // best match found (for LLM feedback)
}

var (
	thinkRe2     = regexp.MustCompile(`(?s)<think>.*?</think>`)
	reasoningRe2 = regexp.MustCompile(`(?s)<reasoning>.*?</reasoning>`)
	whitespaceRe = regexp.MustCompile(`\s+`)
	codeBlockRe  = regexp.MustCompile("(?s)```(?:\\w+[/\\.\\w]*)?\\n(.*?)```")

	editBlockRe = regexp.MustCompile(
		`(?ms)^<<<<<<<\s*SEARCH\s*$\n(.*?)^=======\s*$\n(.*?)^>>>>>>>\s*REPLACE\s*$`,
	)

	// filePathLineRe matches a file path on the line before <<<<<<< SEARCH.
	filePathLineRe = regexp.MustCompile(`^[\w./-]+\.\w+$`)
)

// ParseEditBlocks extracts all SEARCH/REPLACE blocks from llmOutput. The LLM
// may produce reasoning/thinking before the blocks — that is stripped. Each
// block is associated with the most recent file-path line above it.
func ParseEditBlocks(llmOutput string) []EditBlock {
	output := stripReasoning(llmOutput)

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

// stripReasoning removes <think>...</think>, <reasoning>...</reasoning>, and
// other reasoning-model artifacts from text. If the text has no SEARCH/REPLACE
// markers, it's returned as-is (it might be a whole-file replacement or JSON).
func stripReasoning(text string) string {
	text = thinkRe2.ReplaceAllString(text, "")
	text = reasoningRe2.ReplaceAllString(text, "")

	if !strings.Contains(text, "<<<<<<< SEARCH") && !strings.Contains(text, "<<<<<<<") {
		return text
	}

	// Strip everything before the first file path + SEARCH marker.
	// Reasoning models produce prose like "We are given..." before the blocks.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if filePathLineRe.MatchString(trimmed) {
			if i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "<<<<<<<") {
				return strings.Join(lines[i:], "\n")
			}
		}
		if strings.HasPrefix(trimmed, "<<<<<<<") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return text
}

// findFilePathBefore scans backwards from the end of before to find the most
// recent line that looks like a file path.
func findFilePathBefore(before string) string {
	lines := strings.Split(before, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" ||
			strings.HasPrefix(line, "<<<<<<<") ||
			strings.HasPrefix(line, "=======") ||
			strings.HasPrefix(line, ">>>>>>>") {
			continue
		}
		if filePathLineRe.MatchString(line) {
			return line
		}
		// Heuristic: contains a dot and no spaces.
		if strings.Contains(line, ".") && !strings.Contains(line, " ") {
			return line
		}
	}
	return ""
}

// ApplyEditBlocks applies blocks to originalContent using layered matching
// (exact -> whitespace-normalized -> fuzzy). Returns an EditResult with the
// new content or an actionable error.
func ApplyEditBlocks(originalContent string, blocks []EditBlock) *EditResult {
	if len(blocks) == 0 {
		return &EditResult{
			Success:       false,
			FailedReason:  "no edit blocks provided",
			BlocksTotal:   0,
			BlocksApplied: 0,
		}
	}

	result := originalContent
	applied := 0

	for i, block := range blocks {
		search := strings.TrimRight(block.Search, "\n")
		replace := strings.TrimRight(block.Replace, "\n")

		// Layer 1: exact match
		if idx := strings.Index(result, search); idx >= 0 {
			result = result[:idx] + replace + result[idx+len(search):]
			applied++
			continue
		}

		// Layer 2: whitespace-normalized
		if newResult, ok := applyWhitespaceNormalized(result, search, replace); ok {
			result = newResult
			applied++
			continue
		}

		// Layer 3: fuzzy
		if newResult, closestMatch, ok := applyFuzzyMatch(result, search, replace); ok {
			result = newResult
			applied++
			continue
		} else {
			closest := closestMatch
			if closest == "" {
				closest = findClosestLines(result, search, 5)
			}
			return &EditResult{
				Success:       false,
				NewContent:    result,
				BlocksTotal:   len(blocks),
				BlocksApplied: applied,
				FailedBlock:   &blocks[i],
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

// applyWhitespaceNormalized tries to match search after normalizing all
// whitespace (collapsing runs to single spaces). Returns the new content and
// true on success.
func applyWhitespaceNormalized(content, search, replace string) (string, bool) {
	normalize := func(s string) string {
		return strings.TrimSpace(whitespaceRe.ReplaceAllString(s, " "))
	}

	normContent := normalize(content)
	normSearch := normalize(search)
	if normSearch == "" {
		return "", false
	}
	if !strings.Contains(normContent, normSearch) {
		return "", false
	}

	// Find the actual position by tracking first and last lines of the search
	// block in the original content.
	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 {
		return "", false
	}
	firstLine := strings.TrimSpace(searchLines[0])
	lastLine := strings.TrimSpace(searchLines[len(searchLines)-1])

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

	// Preserve original indentation of the first line.
	origIndent := ""
	for _, c := range contentLines[startIdx] {
		if c == ' ' || c == '\t' {
			origIndent += string(c)
		} else {
			break
		}
	}

	replaceLines := strings.Split(replace, "\n")
	newLines := make([]string, 0, len(contentLines)-(endIdx-startIdx+1)+len(replaceLines))
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

// applyFuzzyMatch finds the closest matching window in content for search
// using Levenshtein similarity. Returns (newContent, closestMatch, success).
func applyFuzzyMatch(content, search, replace string) (string, string, bool) {
	searchLines := strings.Split(search, "\n")
	if len(searchLines) == 0 {
		return "", "", false
	}
	contentLines := strings.Split(content, "\n")
	if len(contentLines) == 0 {
		return "", "", false
	}

	windowSize := len(searchLines)
	if windowSize > len(contentLines) {
		windowSize = len(contentLines)
	}

	bestScore := 0.0
	bestStart := -1
	bestEnd := -1
	for i := 0; i <= len(contentLines)-windowSize; i++ {
		score := 0.0
		for j := 0; j < windowSize && j < len(searchLines); j++ {
			score += levenshteinSimilarity(
				strings.TrimSpace(contentLines[i+j]),
				strings.TrimSpace(searchLines[j]),
			)
		}
		score /= float64(windowSize)
		if score > bestScore {
			bestScore = score
			bestStart = i
			bestEnd = i + windowSize - 1
		}
	}

	if bestScore < FuzzyMatchThreshold || bestStart < 0 {
		closestMatch := ""
		if bestStart >= 0 {
			closestMatch = strings.Join(contentLines[bestStart:bestEnd+1], "\n")
		}
		return "", closestMatch, false
	}

	// Preserve original indentation.
	origIndent := ""
	for _, c := range contentLines[bestStart] {
		if c == ' ' || c == '\t' {
			origIndent += string(c)
		} else {
			break
		}
	}

	replaceLines := strings.Split(replace, "\n")
	newLines := make([]string, 0, len(contentLines)-(bestEnd-bestStart+1)+len(replaceLines))
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

// findClosestLines finds the lines in content most similar to the search
// block. Used for error feedback when all matching layers fail.
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

// levenshteinSimilarity returns a similarity score in [0, 1] (1 = identical).
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

// levenshteinDistance computes the edit distance via the standard DP
// algorithm with O(min(n,m)) space (two rows).
func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
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
// entire fixed file instead of SEARCH/REPLACE blocks. The LLM output should
// contain the file path on the first line, then the full file content in a
// code block:
//
//	app/main.py
//	```python
//	import os
//	```
//
// Returns (filePath, content) or ("", "") if no whole-file replacement found.
func ParseWholeFileReplacement(llmOutput string) (string, string) {
	output := stripReasoning(llmOutput)

	lines := strings.Split(output, "\n")

	// Strategy 1: Bare file path line followed by a code block.
	// Matches: src/handlers.rs\n```\n...code...\n```
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !filePathLineRe.MatchString(trimmed) {
			continue
		}
		// Look for a code block starting within the next 3 lines.
		for j := i + 1; j < len(lines) && j < i+3; j++ {
			nextTrimmed := strings.TrimSpace(lines[j])
			if strings.HasPrefix(nextTrimmed, "```") {
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

	// Strategy 2: Markdown header with file path, followed by code block.
	// Matches: ### src/handlers.rs\n```rust\n...code...\n```
	// This is what small models (8B) actually produce — they use markdown
	// headers for structure instead of bare file paths.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Strip markdown header prefix (###, ##, #)
		if strings.HasPrefix(trimmed, "#") {
			afterHeader := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if filePathLineRe.MatchString(afterHeader) {
				// Look for a code block within the next 3 lines.
				for j := i + 1; j < len(lines) && j < i+3; j++ {
					nextTrimmed := strings.TrimSpace(lines[j])
					if strings.HasPrefix(nextTrimmed, "```") {
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
							return afterHeader, content
						}
					}
				}
			}
		}
	}

	// Strategy 3: File path mentioned in the line before a code block
	// (e.g., "File: src/handlers.rs" or "Edit src/handlers.rs:").
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Look for file path patterns in descriptive text.
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "file:") || strings.Contains(lower, "edit ") ||
			strings.Contains(lower, "modify ") || strings.Contains(lower, "path:") {
			// Extract the file path from the line.
			for _, word := range strings.Fields(trimmed) {
				cleanWord := strings.Trim(word, ":,;\"'()[]{}")
				if filePathLineRe.MatchString(cleanWord) {
					// Look for code block within next 3 lines.
					for j := i + 1; j < len(lines) && j < i+3; j++ {
						nextTrimmed := strings.TrimSpace(lines[j])
						if strings.HasPrefix(nextTrimmed, "```") {
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
								return cleanWord, content
							}
						}
					}
				}
			}
		}
	}

	// Strategy 4: Any code block that looks like code (fallback).
	for _, m := range codeBlockRe.FindAllStringSubmatch(output, -1) {
		if len(m) >= 2 && (strings.Contains(m[1], "def ") ||
			strings.Contains(m[1], "func ") ||
			strings.Contains(m[1], "class ") ||
			strings.Contains(m[1], "pub fn ") ||
			strings.Contains(m[1], "fn ") ||
			strings.Contains(m[1], "package ") ||
			strings.Contains(m[1], "import ")) {
			return "", m[1]
		}
	}
	return "", ""
}

// HasEditBlocks reports whether llmOutput contains SEARCH/REPLACE markers.
func HasEditBlocks(llmOutput string) bool {
	return strings.Contains(llmOutput, "<<<<<<<") && strings.Contains(llmOutput, ">>>>>>>")
}

// HasWholeFile reports whether llmOutput looks like a whole-file replacement.
func HasWholeFile(llmOutput string) bool {
	_, content := ParseWholeFileReplacement(llmOutput)
	return content != ""
}

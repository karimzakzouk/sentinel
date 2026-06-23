package detection

import (
	"regexp"
	"strings"
)

// LogPreprocessor cleans up raw pod logs before passing them to the LLM.
// Based on the original architecture doc:
//   - Dedup repeated lines
//   - Extract unique tracebacks (max 10)
//   - Group by severity (ERROR/WARN/INFO)
//   - Cap at 8KB
type LogPreprocessor struct {
	tracebackRe *regexp.Regexp
	errorRe     *regexp.Regexp
}

func NewLogPreprocessor() *LogPreprocessor {
	return &LogPreprocessor{
		tracebackRe: regexp.MustCompile(`(?s)Traceback.*?(?:\n\n|\n[^ \t]|\Z)`),
		errorRe:     regexp.MustCompile(`(?i)(error|exception|panic|fatal|failed|traceback)`),
	}
}

// Process takes raw pod logs and returns a cleaned, deduplicated, severity-grouped version.
func (p *LogPreprocessor) Process(raw string) string {
	if raw == "" {
		return ""
	}

	// Split into lines
	lines := strings.Split(raw, "\n")

	// Dedup repeated lines (keep first occurrence)
	seen := make(map[string]bool)
	var deduped []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip K8s error messages
		if strings.Contains(line, "unable to retrieve container logs for containerd://") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			deduped = append(deduped, line)
		}
	}

	// Extract unique tracebacks
	tracebacks := p.extractTracebacks(deduped)

	// Group by severity
	var errors, warnings, info []string
	for _, line := range deduped {
		upper := strings.ToUpper(line)
		switch {
		case strings.Contains(upper, "ERROR") || strings.Contains(upper, "TRACEBACK") ||
			strings.Contains(upper, "PANIC") || strings.Contains(upper, "FATAL") ||
			strings.Contains(upper, "EXCEPTION") || p.isErrorLine(line):
			errors = append(errors, line)
		case strings.Contains(upper, "WARN"):
			warnings = append(warnings, line)
		default:
			info = append(info, line)
		}
	}

	// Build the output: errors first, then warnings, then info
	var out strings.Builder

	if len(errors) > 0 {
		out.WriteString("## Errors\n")
		for _, line := range errors {
			out.WriteString(line + "\n")
		}
	}

	if len(tracebacks) > 0 {
		out.WriteString("\n## Tracebacks\n")
		for i, tb := range tracebacks {
			if i >= 10 {
				break
			}
			out.WriteString(tb + "\n\n")
		}
	}

	if len(warnings) > 0 {
		out.WriteString("\n## Warnings\n")
		for _, line := range warnings[:min(10, len(warnings))] {
			out.WriteString(line + "\n")
		}
	}

	if len(info) > 0 {
		out.WriteString("\n## Info\n")
		for _, line := range info[:min(5, len(info))] {
			out.WriteString(line + "\n")
		}
	}

	result := out.String()
	if len(result) > 8192 {
		result = result[:8192] + "\n... (truncated)"
	}
	return result
}

// extractTracebacks pulls out complete Python/Go/Java tracebacks.
func (p *LogPreprocessor) extractTracebacks(lines []string) []string {
	var tracebacks []string
	var current []string
	inTraceback := false

	for _, line := range lines {
		if strings.Contains(line, "Traceback (most recent call last)") {
			if inTraceback && len(current) > 0 {
				tracebacks = append(tracebacks, strings.Join(current, "\n"))
			}
			current = []string{line}
			inTraceback = true
		} else if inTraceback {
			// Traceback continues with indented lines or error type lines
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") ||
				strings.Contains(line, "Error:") || strings.Contains(line, "Exception") ||
				strings.HasPrefix(line, "File ") {
				current = append(current, line)
			} else {
				// End of traceback
				if len(current) > 0 {
					tracebacks = append(tracebacks, strings.Join(current, "\n"))
				}
				current = nil
				inTraceback = false
			}
		}
	}

	if inTraceback && len(current) > 0 {
		tracebacks = append(tracebacks, strings.Join(current, "\n"))
	}

	return tracebacks
}

func (p *LogPreprocessor) isErrorLine(line string) bool {
	return p.errorRe.MatchString(line)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

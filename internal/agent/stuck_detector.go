package agent

import (
	"fmt"
	"strings"
)

// StuckDetector monitors the agent's tool call history and detects
// unproductive patterns (OpenHands pattern). When the agent repeats
// the same action, it gets a recovery prompt instead of continuing.
type StuckDetector struct {
	history []toolCallEntry
	maxHist int
}

type toolCallEntry struct {
	tool    string
	argsKey string
	success bool
	output  string
}

func NewStuckDetector() *StuckDetector {
	return &StuckDetector{maxHist: 10}
}

func (s *StuckDetector) Record(tool string, args map[string]any, result ToolResult) {
	s.history = append(s.history, toolCallEntry{
		tool:    tool,
		argsKey: serializeArgs(args),
		success: result.Success,
		output:  truncate(result.Output, 200),
	})
	if len(s.history) > s.maxHist {
		s.history = s.history[len(s.history)-s.maxHist:]
	}
}

func (s *StuckDetector) CheckStuck() string {
	if len(s.history) < 3 {
		return ""
	}
	if s.detectRepeatedSameCall(3) {
		return "[STUCK DETECTOR] You have called the same tool with the same arguments 3+ times. Try a different approach: read a different file, search for a different pattern, or declare done with your best hypothesis."
	}
	if s.detectRepeatedTool(4) {
		return "[STUCK DETECTOR] You have called the same tool 4+ times with different arguments. Switch to a different tool or review your findings so far."
	}
	if s.detectRepeatedErrors(3) {
		return "[STUCK DETECTOR] Your last 3 tool calls all returned errors or no results. Try reading a different file or declaring done."
	}
	if s.detectAlternating(4) {
		return "[STUCK DETECTOR] You are alternating between two tools without progress. Step back and declare done if you have a reasonable hypothesis."
	}
	return ""
}

func (s *StuckDetector) detectRepeatedSameCall(n int) bool {
	if len(s.history) < n {
		return false
	}
	last := s.history[len(s.history)-1]
	for i := len(s.history) - 2; i >= len(s.history)-n; i-- {
		if s.history[i].tool != last.tool || s.history[i].argsKey != last.argsKey {
			return false
		}
	}
	return true
}

func (s *StuckDetector) detectRepeatedTool(n int) bool {
	if len(s.history) < n {
		return false
	}
	last := s.history[len(s.history)-1].tool
	for i := len(s.history) - 2; i >= len(s.history)-n; i-- {
		if s.history[i].tool != last {
			return false
		}
	}
	return true
}

func (s *StuckDetector) detectRepeatedErrors(n int) bool {
	if len(s.history) < n {
		return false
	}
	for i := len(s.history) - 1; i >= len(s.history)-n; i-- {
		e := s.history[i]
		if e.success && !looksLikeEmptyResult(e.output) {
			return false
		}
	}
	return true
}

func (s *StuckDetector) detectAlternating(n int) bool {
	if len(s.history) < n*2 {
		return false
	}
	toolA := s.history[len(s.history)-1].tool
	toolB := s.history[len(s.history)-2].tool
	if toolA == toolB {
		return false
	}
	for i := 0; i < n; i++ {
		idx := len(s.history) - 1 - i*2
		if idx < 0 || s.history[idx].tool != toolA {
			return false
		}
		if idx-1 < 0 || s.history[idx-1].tool != toolB {
			return false
		}
	}
	return true
}

func serializeArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range args {
		fmt.Fprintf(&b, "%s=%v,", k, v)
	}
	return b.String()
}

func looksLikeEmptyResult(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "no matches") || strings.Contains(lower, "no definition") ||
		strings.Contains(lower, "no callers") || strings.Contains(lower, "no callees") ||
		strings.Contains(lower, "not found") || strings.Contains(lower, "missing")
}

package agent

import (
	"context"
	"fmt"
	"strings"
)

// condenseContext uses an LLM call to summarize the investigation findings
// so far, reducing token count while preserving key information.
//
// This replaces the naive compactConversation() which just concatenated
// "Tool result" strings. The LLM-summarized version preserves the semantic
// meaning of findings (e.g., "found a race condition in store.go between
// SELECT and UPDATE") instead of raw tool output that may be thousands of
// tokens of file contents.
//
// Based on OpenHands "Context Condenser" pattern.
func (a *Agent) condenseContext(ctx context.Context, conv []map[string]string, systemPrompt string) []map[string]string {
	if len(conv) <= 6 {
		return conv
	}

	// Extract tool results from the conversation — these are the findings.
	var findings []string
	for i := 2; i < len(conv)-2; i++ {
		msg := conv[i]
		if msg["role"] == "user" && strings.Contains(msg["content"], "Tool ") {
			findings = append(findings, msg["content"])
		}
	}

	if len(findings) == 0 {
		return conv
	}

	// Build a summary prompt. We ask the LLM to compress the findings
	// into a brief summary that preserves the key facts.
	summaryInput := strings.Join(findings, "\n\n")
	if len(summaryInput) > 3000 {
		summaryInput = summaryInput[:3000] + "...(truncated)"
	}

	summaryCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	summary, err := a.router.FrontierText(summaryCtx,
		"You are an investigation summarizer. Compress the following tool results into a brief summary that preserves key findings: file names, line numbers, function names, and any bugs or anomalies found. Keep it under 200 words.",
		summaryInput,
		300,
	)
	cancel()

	if err != nil || summary == "" {
		// Fallback to the old naive compaction if the LLM call fails.
		return compactConversation(conv, systemPrompt)
	}

	// Build the condensed conversation: system + summary + last 2 messages.
	return []map[string]string{
		{"role": "system", "content": systemPrompt + "\n\n" + agentSystemRole},
		{"role": "user", "content": fmt.Sprintf(
			"Previous investigation summary (LLM-condensed):\n%s\n\nContinue investigating. Respond with JSON only.", summary)},
		conv[len(conv)-2],
		conv[len(conv)-1],
	}
}

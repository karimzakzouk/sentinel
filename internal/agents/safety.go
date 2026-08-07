package agents

import "github.com/karimzakzouk/sentinel/internal/logging"

// truncateErr truncates an error message to 200 chars. Prevents slice-bounds
// panics on short strings when callers do err.Error()[:N].
func truncateErr(s string) string {
	const maxLen = 200
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// truncateStr truncates s to maxLen chars, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// recoverPanic should be deferred in every goroutine that runs agent logic.
// It prevents a single agent bug from killing the entire Sentinel process.
func recoverPanic(agentName string) {
	if r := recover(); r != nil {
		logging.Error("agent panic recovered", "agent", agentName, "panic", r)
	}
}

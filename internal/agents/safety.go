package agents

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// truncateErr safely truncates an error message to maxLen characters.
// Prevents "slice bounds out of range" panics on short strings.

func truncateErr(s string) string {
	const maxLen = 200
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// truncateStr safely truncates any string.

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
		logging.Info("agent panic recovered", "value", agentName, r)
	}
}

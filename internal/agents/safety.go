package agents

import (
        "fmt"
        "log"
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

// safeSlice safely slices a string, never panicking.
func safeSlice(s string, start, end int) string {
        if start < 0 {
                start = 0
        }
        if end > len(s) {
                end = len(s)
        }
        if start > end {
                return ""
        }
        return s[start:end]
}

// recoverPanic should be deferred in every goroutine that runs agent logic.
// It prevents a single agent bug from killing the entire Sentinel process.
func recoverPanic(agentName string) {
        if r := recover(); r != nil {
                log.Printf("[panic] agent %s recovered: %v", agentName, r)
        }
}

var _ = fmt.Sprintf // keep fmt import

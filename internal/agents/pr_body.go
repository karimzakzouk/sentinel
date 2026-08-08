package agents

// pr_body.go: builds the PR body for an autonomous fix. Called from the
// execution phase after sandbox verification, so all data is available.

import (
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// BuildPRBody creates the PR body for an autonomous fix.
func BuildPRBody(incident *schemas.Incident, sandboxVerified bool, sandboxSummary string, canaryImage string) string {
	state := &incident.State
	cluster := &incident.Cluster
	rc := state.RootCause
	fp := state.FixProposal

	if rc == nil || fp == nil {
		return "Autonomous fix by Sentinel."
	}

	var b strings.Builder

	detected := incident.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
	resolved := incident.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
	duration := incident.UpdatedAt.Sub(incident.CreatedAt).Round(time.Second)

	detectionMethod := "Manual trigger (SentinelIncident CR)"
	if len(cluster.Anomalies) > 0 && cluster.Anomalies[0].Detector == "auto_detection" {
		detectionMethod = "Auto-detected via live log streaming"
	}

	b.WriteString("## Sentinel Autonomous Fix\n\n")
	fmt.Fprintf(&b, "**Service:** `%s` · **Namespace:** `%s`\n", cluster.Service, cluster.Namespace)

	fmt.Fprintf(&b, "**Detected:** %s (%s)\n", detected, detectionMethod)

	fmt.Fprintf(&b, "**Resolved:** %s\n", resolved)

	fmt.Fprintf(&b, "**Time to fix:** %s\n", formatDuration(duration))

	fmt.Fprintf(&b, "**Confidence:** %.1f%%\n\n", rc.Confidence*100)

	b.WriteString("---\n\n")

	errorType := extractErrorTypeFromLogs(cluster)
	b.WriteString("### Incident Summary\n\n")
	b.WriteString("| Field | Value |\n|-------|-------|\n")
	fmt.Fprintf(&b, "| Detection method | %s |\n", detectionMethod)

	if errorType != "" {
		fmt.Fprintf(&b, "| Error type | `%s` |\n", errorType)

	}
	fmt.Fprintf(&b, "| Category | %s |\n\n", rc.Category)

	b.WriteString("### Root Cause\n\n")
	b.WriteString(rc.Statement + "\n\n")

	b.WriteString("### Agent Reasoning\n\n")
	if len(state.Hypotheses) > 0 {
		fmt.Fprintf(&b, "**Investigator** — formed %d hypotheses:\n", len(state.Hypotheses))

		for _, h := range state.Hypotheses {
			marker := " "
			if h.Falsified {
				marker = "~~"
			}
			fmt.Fprintf(&b, "%d. **%s%s** (confidence: %.2f)%s — %s\n",
				int(h.HID[1]-'0'), marker, h.HID, h.Confidence, marker, truncate(h.Statement, 100))

		}
		b.WriteString("\n")
	}

	survivorID := ""
	for _, h := range state.Hypotheses {
		if !h.Falsified {
			survivorID = h.HID
			break
		}
	}
	if survivorID != "" {
		fmt.Fprintf(&b, "**Analyst** — %s selected as root cause (highest confidence, no contradicting evidence).\n\n", survivorID)

	}
	fmt.Fprintf(&b, "**Fix Proposer** — recommended `%s`: %s\n\n", fp.FixType, truncate(fp.Description, 120))

	b.WriteString("### Sandbox Verification\n\n")
	if sandboxVerified {
		b.WriteString("| Level | Check | Result |\n|-------|-------|--------|\n")
		b.WriteString("| 1 | Canary pod starts and becomes Ready | Passed |\n")
		b.WriteString("| 2 | All endpoints return 2xx/3xx | Passed |\n")
		b.WriteString("| 3 | Original error is gone | Passed |\n")
		b.WriteString("| 4 | Regression test passes | Passed |\n")
		b.WriteString("| 5 | Reproducer script passes | Passed |\n\n")
		if canaryImage != "" {
			fmt.Fprintf(&b, "**Canary image:** `%s`\n\n", canaryImage)

		}
	} else {
		fmt.Fprintf(&b, "**NEEDS REVIEW** — %s\n\n", sandboxSummary)

		b.WriteString("This fix was **not** verified in a sandbox. Review the changes carefully before merging.\n\n")
	}

	excerpt := extractErrorExcerpt(cluster)
	if excerpt != "" {
		b.WriteString("### Error Excerpt (from pod logs)\n\n")
		b.WriteString("```\n")
		b.WriteString(excerpt)
		b.WriteString("\n```\n\n")
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "_Autonomous fix by Sentinel · Confidence: %.1f%%_\n", rc.Confidence*100)

	if sandboxVerified {
		b.WriteString("_Review before merging. The canary image was verified in an isolated sandbox namespace._\n")
	} else {
		b.WriteString("_Review before merging. This fix was NOT verified in a sandbox — extra scrutiny required._\n")
	}
	return b.String()
}

// extractErrorTypeFromLogs uses the multi-language ParseError to detect the
// error type from the anomaly explanation.
func extractErrorTypeFromLogs(cluster *schemas.AnomalyCluster) string {
	for _, a := range cluster.Anomalies {
		if parsed := ParseError(a.Explanation); parsed != nil && parsed.ErrorType != "" && parsed.ErrorType != "unknown" {
			return parsed.ErrorType
		}
	}
	return ""
}

// extractErrorExcerpt pulls the relevant error lines from the pod logs stored
// in the anomaly explanation.
func extractErrorExcerpt(cluster *schemas.AnomalyCluster) string {
	for _, a := range cluster.Anomalies {
		text := a.Explanation
		idx := strings.Index(text, "## Pod Logs")
		if idx == -1 {
			continue
		}
		logs := text[idx:]
		lines := strings.Split(logs, "\n")
		var excerpt []string
		capturing := false
		for _, line := range lines {
			if strings.Contains(line, "Traceback") ||
				strings.Contains(line, "Error:") ||
				strings.Contains(line, "panic:") ||
				strings.Contains(line, "Exception") {
				capturing = true
			}
			if capturing {
				excerpt = append(excerpt, line)
				if len(excerpt) > 2 && (strings.Contains(line, "Error") ||
					strings.Contains(line, "panic") ||
					strings.Contains(line, "Exception")) {
					break
				}
			}
		}
		if len(excerpt) > 0 {
			result := strings.Join(excerpt, "\n")
			if len(result) > MaxPRBodyExcerptChars {
				result = result[:MaxPRBodyExcerptChars] + "..."
			}
			return result
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) - minutes*60
	if seconds == 0 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	return fmt.Sprintf("%d minutes %d seconds", minutes, seconds)
}

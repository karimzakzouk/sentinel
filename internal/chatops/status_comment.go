package chatops

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StatusCommentManagerImpl is the default StatusCommentManager.
type StatusCommentManagerImpl struct {
	gh GitHubClient
}

// NewStatusCommentManager creates a StatusCommentManager.
func NewStatusCommentManager(gh GitHubClient) StatusCommentManager {
	return &StatusCommentManagerImpl{gh: gh}
}

// Update posts (or edits) the [SENTINELSTATUS] comment on the PR. If a
// status comment exists, it is edited in place; otherwise a new one is
// posted.
func (s *StatusCommentManagerImpl) Update(ctx context.Context, repo string, prNumber int, incident *Incident, extra string) error {
	body := s.Render(incident, extra)

	commentID, _, err := s.gh.FindStatusComment(ctx, repo, prNumber)
	if err != nil {
		return fmt.Errorf("find status comment: %w", err)
	}

	if commentID != 0 {
		return s.gh.UpdateComment(ctx, repo, commentID, body)
	}
	return s.gh.PostComment(ctx, repo, prNumber, body)
}

// Render generates the markdown body for the status comment. The body
// starts with an HTML marker (so FindStatusComment can locate it via the
// list-comments API), followed by a phase table. extra is appended below
// the table (e.g. recent error message, "auto-merge queued" notice).
func (s *StatusCommentManagerImpl) Render(incident *Incident, extra string) string {
	var sb strings.Builder

	// Hidden marker — used to find the comment via list-comments API.
	// incident_id makes it easy to cross-reference with the DB.
	fmt.Fprintf(&sb, "<!-- [SENTINELSTATUS] incident_id=%s pr_number=%d -->\n\n",
		incident.ID, incident.PRNumber)

	sb.WriteString("## 🤖 Sentinel Status\n\n")

	sb.WriteString("| Phase | Status | Detail |\n")
	sb.WriteString("|-------|--------|--------|\n")

	fmt.Fprintf(&sb, "| Detection | ✅ | Service: `%s` (namespace: `%s`) |\n",
		incident.Service, incident.Namespace)

	if incident.RootCause != "" {
		fmt.Fprintf(&sb, "| Root cause | ✅ | %s (confidence: %.0f%%) |\n",
			truncate(incident.RootCause, 80), incident.Confidence*100)

	} else {
		sb.WriteString("| Root cause | ⏳ | Investigating... |\n")
	}

	switch {
	case incident.Status == "verified" || incident.Status == "resolved":
		sb.WriteString("| Sandbox | ✅ | Verified |\n")
	case incident.Status == "executing":
		sb.WriteString("| Sandbox | ⏳ | Running... |\n")
	case strings.HasPrefix(incident.Status, "failed"):
		sb.WriteString("| Sandbox | ❌ | Failed |\n")
	default:
		sb.WriteString("| Sandbox | — | Pending |\n")
	}

	if incident.PRURL != "" {
		fmt.Fprintf(&sb, "| PR | ✅ | [%s#%d](%s) |\n", incident.PRRepo, incident.PRNumber, incident.PRURL)

	} else {
		sb.WriteString("| PR | ⏳ | Opening... |\n")
	}

	fmt.Fprintf(&sb, "| Overall | %s | `%s` |\n", emojiForStatus(incident.Status), incident.Status)

	autoMergeStatus := "—"
	autoMergeDetail := "(not configured)"
	if incident.AutoMergeEnabled {
		switch {
		case incident.AutoMergeMerged:
			autoMergeStatus = "✅"
			autoMergeDetail = "Merged"
		case incident.AutoMergeSkipped != "":
			autoMergeStatus = "⏭️"
			autoMergeDetail = incident.AutoMergeSkipped
		default:
			autoMergeStatus = "⏳"
			autoMergeDetail = "Queued (evaluating gates)"
		}
	}
	fmt.Fprintf(&sb, "| Auto-merge | %s | %s |\n", autoMergeStatus, autoMergeDetail)

	sb.WriteString("\n")
	fmt.Fprintf(&sb, "_Last updated: %s_\n\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

	sb.WriteString("Commands: `/sentinel help` for available commands.\n")

	if extra != "" {
		sb.WriteString("\n---\n\n")
		sb.WriteString(extra)
		sb.WriteString("\n")
	}

	return sb.String()
}

// emojiForStatus maps an incident status to an emoji for the status table.
func emojiForStatus(status string) string {
	switch status {
	case "verified", "resolved":
		return "✅"
	case "investigating", "root_caused", "fix_proposed", "approved", "executing":
		return "⏳"
	case "failed", "rejected", "escalated":
		return "❌"
	case "governance_review":
		return "⚠️"
	}
	return "—"
}

// truncate shortens a string to maxLen chars, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

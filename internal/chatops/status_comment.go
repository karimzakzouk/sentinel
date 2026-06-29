// Package chatops — in-place editable [SENTINELSTATUS] comment manager.
// Every Sentinel-tracked PR gets ONE status comment that's edited in place
// as state changes (Prow's [APPROVALNOTIFIER] pattern). New comments are
// only posted for *events* (sandbox re-run, regression detected), not state
// changes — this prevents comment spam on busy PRs.
// The comment body starts with an HTML comment marker so we can find it
// later via GitHub's list-comments API:
//   <!-- [SENTINELSTATUS] incident_id=abc-123 pr_number=42 -->
// Followed by a markdown table of all pipeline phases + their statuses.
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

// Update posts (or edits) the [SENTINELSTATUS] comment on the PR.

func (s *StatusCommentManagerImpl) Update(ctx context.Context, repo string, prNumber int, incident *Incident, extra string) error {
        body := s.Render(incident, extra)

        // Find existing status comment.

        commentID, _, err := s.gh.FindStatusComment(ctx, repo, prNumber)
        if err != nil {
                return fmt.Errorf("find status comment: %w", err)
        }

        if commentID != 0 {
                // Edit in place.

                return s.gh.UpdateComment(ctx, repo, commentID, body)
        }
        // Post new.

        return s.gh.PostComment(ctx, repo, prNumber, body)
}

// Render generates the markdown body for the status comment.
// `extra` is appended below the status table (e.g. recent error message,
// "auto-merge queued" notice).

func (s *StatusCommentManagerImpl) Render(incident *Incident, extra string) string {
        var sb strings.Builder

        // Hidden marker — used to find the comment via list-comments API.
        // incident_id makes it easy to cross-reference with the DB.

        sb.WriteString(fmt.Sprintf("<!-- [SENTINELSTATUS] incident_id=%s pr_number=%d -->\n\n",
                incident.ID, incident.PRNumber))

        sb.WriteString("## 🤖 Sentinel Status\n\n")

        // Status table

        sb.WriteString("| Phase | Status | Detail |\n")
        sb.WriteString("|-------|--------|--------|\n")

        // Detection

        sb.WriteString(fmt.Sprintf("| Detection | ✅ | Service: `%s` (namespace: `%s`) |\n",
                incident.Service, incident.Namespace))

        // Root cause

        if incident.RootCause != "" {
                sb.WriteString(fmt.Sprintf("| Root cause | ✅ | %s (confidence: %.0f%%) |\n",
                        truncate(incident.RootCause, 80), incident.Confidence*100))
        } else {
                sb.WriteString("| Root cause | ⏳ | Investigating... |\n")
        }

        // Sandbox — derive from status

        if incident.Status == "verified" || incident.Status == "resolved" {
                sb.WriteString("| Sandbox | ✅ | Verified |\n")
        } else if incident.Status == "executing" {
                sb.WriteString("| Sandbox | ⏳ | Running... |\n")
        } else if strings.HasPrefix(incident.Status, "failed") {
                sb.WriteString("| Sandbox | ❌ | Failed |\n")
        } else {
                sb.WriteString("| Sandbox | — | Pending |\n")
        }

        // PR

        if incident.PRURL != "" {
                sb.WriteString(fmt.Sprintf("| PR | ✅ | [%s#%d](%s) |\n", incident.PRRepo, incident.PRNumber, incident.PRURL))
        } else {
                sb.WriteString("| PR | ⏳ | Opening... |\n")
        }

        // Overall status

        sb.WriteString(fmt.Sprintf("| Overall | %s | `%s` |\n", emojiForStatus(incident.Status), incident.Status))

        // Auto-merge status — v4.2.5:

        autoMergeStatus := "—"
        autoMergeDetail := "(not configured)"
        if incident.AutoMergeEnabled {
                if incident.AutoMergeMerged {
                        autoMergeStatus = "✅"
                        autoMergeDetail = "Merged"
                } else if incident.AutoMergeSkipped != "" {
                        autoMergeStatus = "⏭️"
                        autoMergeDetail = incident.AutoMergeSkipped
                } else {
                        autoMergeStatus = "⏳"
                        autoMergeDetail = "Queued (evaluating gates)"
                }
        }
        sb.WriteString(fmt.Sprintf("| Auto-merge | %s | %s |\n", autoMergeStatus, autoMergeDetail))

        sb.WriteString("\n")
        sb.WriteString(fmt.Sprintf("_Last updated: %s_\n\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC")))

        // Commands hint

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

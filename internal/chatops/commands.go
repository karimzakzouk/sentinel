// Package chatops — MVP command implementations.
//
// 6 commands ship in v4.0.0:
//   /sentinel help          — auto-generated command list (filtered by user's perm)
//   /sentinel status        — re-render the in-place [SENTINELSTATUS] comment
//   /sentinel explain       — re-explain the root cause in plain English via LLM
//   /sentinel trace         — dump the full agent reasoning trace
//   /sentinel retry-sandbox — re-run sandbox verification on the current branch
//   /sentinel alternatives  — generate 3 alternative fix approaches via LLM
//   /sentinel apply <A|B|C> — switch the PR to the chosen alternative (placeholder)
//
// More commands (hold, approve, no-merge, merge, revert, rollback, pause,
// override, investigate, retest, verify) ship in v4.1.0 alongside auto-merge.
package chatops

import (
        "context"
        "encoding/json"
        "fmt"
        "strings"
)

func init() {
        // Informational (Tier 1 — read access)
        RegisterCommand(&CommandSpec{
                Name:        "help",
                Description: "List available commands (filtered by your permission)",
                Usage:       "/sentinel help",
                Example:     "/sentinel help",
                Permission:  PermRead,
                Tier:        "Informational",
                Handler:     cmdHelp,
        })
        RegisterCommand(&CommandSpec{
                Name:        "status",
                Description: "Re-render the in-place status comment with current state",
                Usage:       "/sentinel status",
                Example:     "/sentinel status",
                Permission:  PermRead,
                Tier:        "Informational",
                Handler:     cmdStatus,
        })
        RegisterCommand(&CommandSpec{
                Name:        "explain",
                Description: "Re-explain the root cause in plain English",
                Usage:       "/sentinel explain",
                Example:     "/sentinel explain",
                Permission:  PermRead,
                Tier:        "Informational",
                Handler:     cmdExplain,
        })
        RegisterCommand(&CommandSpec{
                Name:        "trace",
                Description: "Show the full agent reasoning trace (hypotheses, evidence, decisions)",
                Usage:       "/sentinel trace",
                Example:     "/sentinel trace",
                Permission:  PermRead,
                Tier:        "Informational",
                Handler:     cmdTrace,
        })

        // Operational (Tier 2 — write access)
        RegisterCommand(&CommandSpec{
                Name:        "retry-sandbox",
                Description: "Re-run sandbox verification on the current branch state",
                Usage:       "/sentinel retry-sandbox",
                Example:     "/sentinel retry-sandbox",
                Permission:  PermWrite,
                Tier:        "Operational",
                Handler:     cmdRetrySandbox,
        })
        RegisterCommand(&CommandSpec{
                Name:        "alternatives",
                Description: "Generate 3 alternative fix approaches (LLM re-runs FixProposer at higher temperature)",
                Usage:       "/sentinel alternatives",
                Example:     "/sentinel alternatives",
                Permission:  PermWrite,
                Tier:        "Operational",
                Handler:     cmdAlternatives,
        })
        RegisterCommand(&CommandSpec{
                Name:        "apply",
                Description: "After /sentinel alternatives, switch the PR to the chosen alternative (A, B, or C)",
                Usage:       "/sentinel apply <A|B|C>",
                Example:     "/sentinel apply B",
                Permission:  PermWrite,
                Tier:        "Operational",
                Handler:     cmdApply,
        })
}

// cmdHelp posts the auto-generated help text, filtered by the user's permission.
// Fixes Prow's #5 complaint (command discovery is poor) — users see exactly
// what they can run, plus 🔒 markers for higher-tier commands they can't.
func cmdHelp(ctx context.Context, cmd *Command, deps *Deps) error {
        perm, err := deps.GitHub.GetPermission(ctx, cmd.PRRepo, cmd.Author)
        if err != nil {
                // Fall back to "read" — informational commands will still be shown.
                perm = "read"
        }
        body := RenderHelp(cmd.Author, perm)
        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// cmdStatus re-renders the in-place [SENTINELSTATUS] comment with the
// current incident state. Useful when the comment is stale (e.g. after a
// manual intervention that didn't go through Sentinel).
func cmdStatus(ctx context.Context, cmd *Command, deps *Deps) error {
        inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
        if err != nil {
                return fmt.Errorf("find incident: %w", err)
        }
        if inc == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "This PR isn't tracked by Sentinel. If you just opened it manually, Sentinel will track it once an incident is associated.")
        }
        if deps.StatusComment != nil {
                if err := deps.StatusComment.Update(ctx, cmd.PRRepo, cmd.PRNumber, inc, ""); err != nil {
                        return fmt.Errorf("update status comment: %w", err)
                }
        }
        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                "✅ Status comment refreshed. See the [SENTINELSTATUS] comment above.")
}

// cmdExplain re-runs the analyst agent on the incident's root cause and
// posts a plain-English explanation. Reviewers want to understand *why*
// before merging — this gives them the "why" without reading the full
// agent trace.
func cmdExplain(ctx context.Context, cmd *Command, deps *Deps) error {
        inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
        if err != nil {
                return fmt.Errorf("find incident: %w", err)
        }
        if inc == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ This PR isn't tracked by Sentinel. No incident to explain.")
        }

        // Acknowledge that we're working (sandbox retries take minutes).
        _ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                "🧠 Re-explaining the root cause... (this takes ~10-20s)")

        // Use the LLM to generate a plain-English explanation.
        system := `You are Sentinel, an autonomous AI SRE. A reviewer has asked for a plain-English explanation of an incident's root cause. Be clear, concise, and actionable. Avoid jargon. Structure your response as:

## What happened
<one paragraph>

## Why it happened
<one paragraph>

## What the fix does
<one paragraph>

## What could still go wrong
<one short paragraph — be honest about edge cases>

Do NOT include a "what to do next" section — the reviewer can decide that.`

        user := fmt.Sprintf("Service: %s (namespace: %s)\nRoot cause: %s\nConfidence: %.2f\nStatus: %s",
                inc.Service, inc.Namespace, inc.RootCause, inc.Confidence, inc.Status)

        explanation, err := deps.Router.FrontierText(ctx, system, user, 800)
        if err != nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        fmt.Sprintf("❌ LLM call failed: %v", err))
        }

        body := fmt.Sprintf("## 🧠 Root Cause Explanation\n\nRequested by @%s.\n\n%s", cmd.Author, explanation)
        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// cmdTrace dumps the full agent reasoning trace (hypotheses considered,
// evidence gathered, why each was kept/dropped). For engineers who want
// the full picture before reviewing.
//
// Format: a markdown table of every agent step with timestamps + decisions.
// Source: the incident's state.agent_trace (a []map[string]any).
func cmdTrace(ctx context.Context, cmd *Command, deps *Deps) error {
        inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
        if err != nil {
                return fmt.Errorf("find incident: %w", err)
        }
        if inc == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ This PR isn't tracked by Sentinel. No trace to show.")
        }

        var sb strings.Builder
        sb.WriteString(fmt.Sprintf("## 📋 Agent Trace for incident `%s`\n\n", inc.ID))
        sb.WriteString(fmt.Sprintf("**Service:** `%s` (namespace: `%s`)  \n", inc.Service, inc.Namespace))
        sb.WriteString(fmt.Sprintf("**Status:** `%s`  \n", inc.Status))
        if inc.RootCause != "" {
                sb.WriteString(fmt.Sprintf("**Root cause:** %s (confidence: %.0f%%)\n\n", inc.RootCause, inc.Confidence*100))
        }

        // Hypotheses section
        if len(inc.Hypotheses) > 0 {
                sb.WriteString("### Hypotheses Considered\n\n")
                sb.WriteString("| ID | Confidence | Falsified | Statement |\n")
                sb.WriteString("|----|------------|-----------|-----------|\n")
                for _, h := range inc.Hypotheses {
                        falsified := ""
                        if h.Falsified {
                                falsified = "~~❌~~"
                        } else {
                                falsified = "✅"
                        }
                        stmt := h.Statement
                        if len(stmt) > 100 {
                                stmt = stmt[:97] + "..."
                        }
                        sb.WriteString(fmt.Sprintf("| %s | %.2f | %s | %s |\n", h.ID, h.Confidence, falsified, stmt))
                }
                sb.WriteString("\n")
        }

        // Agent trace section
        if len(inc.AgentTrace) > 0 {
                sb.WriteString("### Agent Steps\n\n")
                sb.WriteString("| Step | Agent | Action | Detail |\n")
                sb.WriteString("|------|-------|--------|--------|\n")
                for i, step := range inc.AgentTrace {
                        agent, _ := step["agent"].(string)
                        action, _ := step["action"].(string)
                        if action == "" {
                                // Fall back to other common keys
                                if v, ok := step["iteration"]; ok {
                                        action = fmt.Sprintf("iteration %v", v)
                                }
                        }
                        detail := ""
                        // Pick the most informative field
                        for _, k := range []string{"hypothesis_count", "files_changed", "search_results", "fetched_files", "evidence_count", "fix_type", "error"} {
                                if v, ok := step[k]; ok {
                                        detail = fmt.Sprintf("%s=%v", k, v)
                                        break
                                }
                        }
                        sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n", i+1, agent, action, detail))
                }
                sb.WriteString("\n")
        }

        if inc.SandboxSummary != "" {
                sb.WriteString("### Sandbox\n\n```\n")
                sb.WriteString(inc.SandboxSummary)
                sb.WriteString("\n```\n\n")
        }

        sb.WriteString(fmt.Sprintf("_Requested by @%s_", cmd.Author))
        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, sb.String())
}

// cmdRetrySandbox re-runs the 3-level sandbox verification against the
// current branch state. Catches the case where a human edited the PR after
// Sentinel opened it (e.g. fixed a typo, added a test) and wants the new
// code verified.
//
// Implementation: calls deps.Sandbox.RunForIncident(incidentID), which
// re-fetches the branch state and runs Levels 1-3 (and 4/5 in strict mode).
func cmdRetrySandbox(ctx context.Context, cmd *Command, deps *Deps) error {
        inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
        if err != nil {
                return fmt.Errorf("find incident: %w", err)
        }
        if inc == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ This PR isn't tracked by Sentinel. Cannot retry sandbox.")
        }

        if deps.Sandbox == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ Sandbox runner not configured on this Sentinel instance.")
        }

        _ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                "🧪 Re-running sandbox verification... (this takes 1-5 minutes)")

        verified, summary, err := deps.Sandbox.RunForIncident(ctx, inc.ID)
        if err != nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        fmt.Sprintf("❌ Sandbox run failed: %v", err))
        }

        var body string
        if verified {
                body = fmt.Sprintf("## ✅ Sandbox Re-Verified\n\nRequested by @%s.\n\nAll verification levels passed.\n\n```\n%s\n```",
                        cmd.Author, summary)
        } else {
                body = fmt.Sprintf("## ❌ Sandbox Verification Failed\n\nRequested by @%s.\n\nThe fix did not pass sandbox verification:\n\n```\n%s\n```\n\nUse `/sentinel alternatives` to see other fix approaches.",
                        cmd.Author, summary)
        }

        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// cmdAlternatives re-runs the FixProposer at higher temperature with a
// prompt that asks for 3 different fix approaches. Reviewers can compare
// them and pick the one they prefer via `/sentinel apply <A|B|C>`.
//
// This is the most LLM-intensive command — it makes one frontier LLM call
// with a longer prompt and asks for structured output (3 alternatives).
func cmdAlternatives(ctx context.Context, cmd *Command, deps *Deps) error {
        inc, err := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber)
        if err != nil {
                return fmt.Errorf("find incident: %w", err)
        }
        if inc == nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ This PR isn't tracked by Sentinel. No incident to generate alternatives for.")
        }

        _ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                "🤔 Generating 3 alternative fix approaches... (this takes ~30-60s)")

        system := `You are Sentinel's Fix Proposer agent. A reviewer has asked for alternative approaches to a fix you proposed.

You will be given the root cause and the current fix description. Generate THREE DIFFERENT fix approaches:

- Approach A: the simplest minimal change (usually what was already proposed)
- Approach B: a more idiomatic/framework-aligned approach
- Approach C: the most defensive/robust approach (e.g. abstraction, validation layer)

For each approach, describe:
- The fix in 1-2 sentences
- Pros: what's good about it
- Cons: what's bad about it
- Files that would change

Respond as a JSON object with shape:
{
  "alternatives": [
    {"id": "A", "description": "...", "pros": "...", "cons": "...", "files": ["..."]},
    {"id": "B", "description": "...", "pros": "...", "cons": "...", "files": ["..."]},
    {"id": "C", "description": "...", "pros": "...", "cons": "...", "files": ["..."]}
  ]
}

Output ONLY the JSON object. No prose before or after.`

        user := fmt.Sprintf("Service: %s\nRoot cause: %s\nCurrent fix description: (see PR)\n\nGenerate 3 alternative fix approaches.",
                inc.Service, inc.RootCause)

        result, err := deps.Router.FrontierJSON(ctx, system, user, 2000)
        if err != nil {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        fmt.Sprintf("❌ LLM call failed: %v", err))
        }

        body := renderAlternatives(result, cmd.Author, inc.ID)
        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// renderAlternatives formats the LLM's JSON response as a markdown comment.
func renderAlternatives(result map[string]any, author, incidentID string) string {
        alts, _ := result["alternatives"].([]any)
        if len(alts) == 0 {
                return fmt.Sprintf("## 🤔 Alternative Fix Approaches\n\nRequested by @%s.\n\n⚠️ The LLM returned no alternatives. Try again or use `/sentinel retest` to retry the original fix proposer.", author)
        }

        var sb strings.Builder
        sb.WriteString(fmt.Sprintf("## 🤔 Alternative Fix Approaches\n\nRequested by @%s.\n\n", author))
        sb.WriteString(fmt.Sprintf("<!-- [SENTINELALTERNATIVES] incident_id=%s count=%d -->\n\n", incidentID, len(alts)))

        for _, a := range alts {
                am, ok := a.(map[string]any)
                if !ok {
                        continue
                }
                id, _ := am["id"].(string)
                desc, _ := am["description"].(string)
                pros, _ := am["pros"].(string)
                cons, _ := am["cons"].(string)
                files, _ := am["files"].([]any)

                sb.WriteString(fmt.Sprintf("### Approach %s\n\n", id))
                sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", desc))
                sb.WriteString(fmt.Sprintf("**Pros:** %s\n\n", pros))
                sb.WriteString(fmt.Sprintf("**Cons:** %s\n\n", cons))
                if len(files) > 0 {
                        sb.WriteString("**Files changed:**\n")
                        for _, f := range files {
                                sb.WriteString(fmt.Sprintf("- `%v`\n", f))
                        }
                        sb.WriteString("\n")
                }
        }

        sb.WriteString("---\n\n")
        sb.WriteString("Reply `/sentinel apply A`, `/sentinel apply B`, or `/sentinel apply C` to switch the PR to that approach.\n")
        return sb.String()
}

// cmdApply switches the PR to a chosen alternative fix. Reads the
// [SENTINELALTERNATIVES] comment, parses the chosen alternative, re-runs
// the FixProposer with that approach as the system hint, and pushes the
// new commits to the branch.
//
// v4.0.0 MVP: posts a "coming soon" message. Full implementation requires
// extending the FixProposer to accept an "approach hint" parameter.
func cmdApply(ctx context.Context, cmd *Command, deps *Deps) error {
        if len(cmd.ArgList) == 0 {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        "❌ Usage: `/sentinel apply <A|B|C>`. Run `/sentinel alternatives` first to see options.")
        }
        choice := strings.ToUpper(cmd.ArgList[0])
        if choice != "A" && choice != "B" && choice != "C" {
                return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber,
                        fmt.Sprintf("❌ Invalid choice `%s`. Must be A, B, or C. Run `/sentinel alternatives` to see options.", cmd.ArgList[0]))
        }

        // NOTE: Full /sentinel apply implementation requires extending FixProposer
        // to accept an "approach hint" parameter, re-running it with the chosen
        // approach, pushing new commits via the GitHub executor, and re-triggering
        // sandbox verification. Tracked as a future enhancement. For now, the
        // command posts the LLM-generated approach description so the developer
        // can implement it manually.

        body := fmt.Sprintf("## ⏳ Apply Approach %s\n\n"+
                "Requested by @%s.\n\n"+
                "Automatic approach application is not yet implemented. To use approach %s:\n\n"+
                "1. Manually implement approach %s on this branch (description below)\n"+
                "2. Run `/sentinel retry-sandbox` to verify it\n"+
                "3. Merge as usual\n\n"+
                "The LLM-generated approach description is in the [SENTINELALTERNATIVES] comment above.",
                choice, cmd.Author, choice, choice)

        return deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
}

// unused import guards (kept for future expansion)
var (
        _ = json.Marshal
        _ = context.Background
)

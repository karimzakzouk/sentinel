// Package chatops implements GitHub-PR-comment-driven command execution for
// Sentinel. Every command is parsed, permission-checked, executed, and the
// result is fed back via emoji reactions + status comments.
//
// Design principles:
//
//   - ACK with a 👍 reaction immediately when a command is received so the
//     author knows the bot saw it.
//   - One negation convention: every state-changing command takes `cancel`
//     as an argument. No /un-, no /remove-.
//   - Labels are the gate; the auto-merger is the actor. Commands never
//     merge directly — they flip labels, the merger watches labels.
//   - Auto-generated command registry. /sentinel help is generated from
//     code, never out of sync.
//   - In-place editable [SENTINELSTATUS] comment for multi-step workflows.
//   - Permission errors are actionable — they say *why* and *what to do*.
//
// Commands span three permission tiers:
//
//   - Informational (read): help, status, explain, trace
//   - Operational (write): retry-sandbox, alternatives, apply, hold,
//     approve, no-merge, merge, investigate, retest, verify
//   - Destructive (admin): revert, rollback, pause, resume, override
//
// The WebhookHandler (handler.go) is the HTTP entry point. It parses the
// GitHub issue_comment event, calls ParseCommand, then Dispatch.
package chatops

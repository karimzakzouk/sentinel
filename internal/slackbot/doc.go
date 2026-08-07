// Package slackbot implements an interactive two-way Slack bot for Sentinel.
//
// The bot reports what it's doing as it works (investigation started, fix
// proposed, PR opened) and accepts commands via @sentinel mentions or
// slash commands:
//
//   - "pause for X" — pauses autonomous mode for X minutes
//   - "investigate Y" — manually triggers an investigation for service Y
//   - "show me the last 5 incidents" — lists recent incidents
//   - "status" — shows the current autonomous mode status
//   - "resume" — resumes autonomous mode
//
// Uses the Slack Events API (not RTM — simpler, webhook-based).
//
// HTTP entry point: POST /slack/events (Bot.HandleEvents).
//
// # Signature verification
//
// Every request is HMAC-SHA256 verified using the signing secret Slack
// issues at app creation. Stale timestamps (>5 min old) or future
// timestamps (>60s ahead) are rejected to prevent replay attacks. The
// url_verification challenge is accepted without a signature so initial
// app setup works when the secret is not yet configured.
package slackbot

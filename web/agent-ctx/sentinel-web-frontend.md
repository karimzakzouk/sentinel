# Work Record — Sentinel Web Frontend Rebuild

**Task:** Replace the old 1174-line `page.tsx` with a comprehensive 8-section SPA frontend.
**Agent:** main (z.ai code)
**Status:** ✅ Complete — `npm run build` passes, `out/index.html` generated.

## What was built

### Core shell
- `src/app/page.tsx` — client-side app shell: fixed sidebar + section router + header (version, pause/resume, refresh, SSE status). Mobile sidebar via Sheet.
- `src/app/globals.css` — Sentinel dark theme tokens (zinc-950 bg, cyan-500 accent), custom scrollbar, range-slider styling, live-pulse animation.
- `src/lib/utils.ts` — added `cn` + formatting helpers (timeAgo, formatDuration, formatCost, formatConfidence, truncate, shortId, parseCsv, joinCsv).
- `src/lib/types.ts` — fixed `Provider.priority: int` → `number` (invalid TS type).

### Hooks
- `src/hooks/use-sse.ts` — singleton SSE store (one shared EventSource across all components), 100-event ring buffer, connection status.
- `src/hooks/use-fetch.ts` — generic fetch hook with manual refetch, auto-refresh interval, deps-driven refetch.

### Shared components
- `src/components/Sidebar.tsx` — 220px fixed sidebar, Sentinel logo, 8 nav items with lucide icons, active=cyan.
- `src/components/StatusBadge.tsx` — status→coloured badge + StatusDot.
- `src/components/ConfidenceBar.tsx` — 0-1 value→coloured progress bar (green/amber/red).
- `src/components/ActivityFeed.tsx` — live SSE event feed with type-specific icons.
- `src/components/ui-helpers.tsx` — Skeleton, EmptyState, ErrorState, SectionHeader, Panel.
- `src/components/Chart.tsx` — CSS-only BarChartH, BarChartV, StackedBar, ChartLegend (no chart library).

### Sections (8)
1. `Dashboard.tsx` — 6 KPI cards, success-rate SVG gauge, recent incidents, live activity feed.
2. `Incidents.tsx` — filterable table (status/service), detail Sheet with cluster summary, hypotheses+confidence bars, root cause, fix proposal+files, sandbox levels L1-5, PR link, agent_trace timeline.
3. `Providers.tsx` — circuit-breaker pool table with green/yellow/red state indicators + summary cards.
4. `AutoMerge.tsx` — queue table with gates pass/fail, rate-limit radial gauge, cooldown indicator, stats.
5. `Sandbox.tsx` — runs derived from incident traces, expandable pod logs (monospace, last 2KB), level dots.
6. `Metrics.tsx` — incidents-by-status bar, LLM cost by model, LLM calls stacked (success/error), sandbox level failures, investigation duration histogram, auto-merge by state.
7. `Chatops.tsx` — 19 hardcoded `/sentinel` commands (from internal/chatops/commands.go + commands_more.go), permission filters, copy-to-clipboard.
8. `Settings.tsx` — 11 config cards (LLM, Timeouts, Confidence, Sandbox, Auto-merge, Detection, Kubernetes, Observability, GitHub App, Slack, Kaniko). Runtime fields saveable via `updateRuntimeConfig()`; startup-only fields read-only with "restart" badge. Custom Toggle/Select/Slider/SecretInput/SecretTextarea/CsvInput primitives. Dirty-field tracking, toast on save.

## Build verification
```
✓ Compiled successfully in 3.0s
  Running TypeScript ... Finished TypeScript in 3.8s
✓ Generating static pages (3/3) in 146ms
```
- `out/index.html` exists (CI check passes).
- No new dependencies added (lucide-react v1.21.0 — `Github` icon not present, used `GitGraph` instead).
- No chart library — all charts are CSS divs.

## Notes
- All sections use the shared `useSSE` singleton + `useFetch` hook for live updates + polling.
- Empty states + skeleton loaders + error states on every data-driven section.
- Responsive: sidebar collapses to hamburger Sheet on mobile.
- SSE gracefully degrades (status "offline") when the Go backend isn't present (e.g. in standalone dev).

# Work Record — Sentinel Web: Real API Rewrite

**Task ID:** sentinel-web-real-api
**Agent:** main (z.ai code)
**Status:** ✅ Complete — `npm run build` passes, static export generated.

## What was done

Rewrote all 12 section components in `src/components/sections/`, plus the shared shell (`page.tsx`, `sidebar.tsx`, `command-palette.tsx`, `common.tsx`), to consume real Sentinel API data via the Zustand store (`useSentinelStore`) instead of static mock data. Deleted `src/lib/mock-data.ts` entirely.

## Files touched

### Shell + shared
- `src/app/page.tsx` — initializes `useSSE()` + `useAutoRefresh(30s)` hooks on mount. Removed the notification `<Bell>` button and profile avatar (per user request). Top bar now shows: section title, search trigger, version badge (from `useSentinelStore(s => s.version)`), SSE connection indicator (live/offline + api-down), real-time clock (updates every 1s via `useEffect`).
- `src/components/sentinel/sidebar.tsx` — removed fake "prod-us-east-1" cluster status and "42d 18h" uptime. Footer now shows real API health, SSE state, and incident count (7d) from the store. "Live Investigation" nav item only pulses when there's an actual active incident.
- `src/components/sentinel/command-palette.tsx` — replaced `useSentinel` + `startLiveSimulation` + mock `incidents` import with `useSentinelStore`. Incident search now uses `useSentinelStore(s => s.incidents)`. Pause/resume action calls `pauseAutonomous(30)` / `resumeAutonomous()` with toast feedback.
- `src/components/sentinel/common.tsx` — removed `import type { IncidentStatus, SandboxLevelStatus } from "@/lib/mock-data"`. Defined both types inline. Also added `EmptyState`, `ErrorState`, `SkeletonRow`, `PanelSkeleton` helpers + `formatCost` + `pct` formatters. `StatusBadge` now accepts arbitrary `string` status (handles `root_caused`, `fix_proposed`, `approved`, `executing`, `rejected` in addition to the original 5). `Mono` accepts an optional `title` prop. `ConfidenceBar` accepts both 0-1 and 0-100 ranges.

### Sections (12)
1. `dashboard.tsx` — KPI cards computed from real store data: `activeIncidents.length` (filter `["investigating","executing","fix_proposed","approved","root_caused"]`), `prIncidents.length`, sandbox verification counts from `metrics.sandbox_verifications_total`, LLM cost from `metrics.llm_total_cost_usd`, provider health from `providers`, queue depth from `autoMerge.queue.length`. Activity feed uses `useSentinelStore(s => s.activityFeed)` (populated by SSE). Removed mock heatmap, mock metrics, mock activity seed.
2. `incidents.tsx` — table from `useSentinelStore(s => s.incidents)`. Filter by status/service/search. Detail drawer fetches full incident via `useSentinelStore(s => s.fetchIncident)` (which calls `GET /incidents/:id`). Detail shows: cluster summary (service/ns/severity/summary/anomalies), root cause (`state.root_cause`), hypotheses (`state.hypotheses` with confidence bars + falsified strike-through), fix proposal (`state.fix_proposal` with file contents + search/replace diffs), PR link (`pr_url`), agent trace (`state.agent_trace` rendered generically since it's `Array<Record<string, unknown>>`), beta distribution (`beta_alpha`/`beta_beta`).
3. `live-investigation.tsx` — finds most-recent active incident from store. Pipeline stepper derives phase index from `state.agent_trace` entries (matches `phase` field against canonical 9-phase list). Live console renders trace entries as timestamped lines. Agent trace tree shows tokens/cost/duration aggregated from trace entries. Fix diff viewer shows `state.fix_proposal.files_to_change`. Empty state when no incidents exist, with manual `triggerInvestigation()` button that prompts for service+namespace.
4. `sandbox.tsx` — derives sandbox runs from incidents that have `fix_proposal`, `pr_url`, or terminal status. L1-L5 levels inferred from incident status (verified→all pass, failed→some fail, investigating→pending). Expanded row shows cluster summary, fix proposal, agent trace excerpts. Empty state when no incidents have sandbox data.
5. `pull-requests.tsx` — derives PRs from incidents with `pr_number`. Shows real `pr_number`, `pr_repo`, `pr_branch`, `pr_url`, opened/merged timestamps. Gate breakdown is "inferred" from incident state (clearly labeled). Empty state when no PRs.
6. `providers.tsx` — pool table from `useSentinelStore(s => s.providers)`. Cost-by-model chart from `metrics.llm_total_cost_usd`. Tokens-by-model chart from `metrics.llm_tokens_total` (parses `"model|direction"` keys). LLM calls summary from `metrics.llm_calls_total`. Per-provider detail cards show priority/status/model/fast_model/frontier_model/trips/last_trip/circuit_breaker_until. Empty state when no providers.
7. `auto-merge.tsx` — uses `useSentinelStore(s => s.autoMerge)`. Shows queue table (with gates_passed/gates_failed), rate limit gauge (merged_last_hour/max_per_hour/remaining), cooldown indicator (active?until?reason), stats (total_merged/total_reverted/success_rate). Recent activity derived from queue states. Empty state when `autoMerge === null`.
8. `detection.tsx` — pause/resume buttons call `pauseAutonomous(durationMin)` / `resumeAutonomous()`. Shows: autonomous mode status (proxied from `healthy`), SSE connection status (`sseConnected`), version, incident count, webhook endpoints (static list matching Go server routes: `/webhook/alertmanager`, `/webhook/github`, `/slack/events`), recent detections from activity feed. Explicitly notes that pod watchers, log streams, and detection rules aren't exposed via the API.
9. `chatops.tsx` — hardcoded 19 commands (matching `internal/chatops/commands.go` + `commands_more.go`). Audit log shows empty state: "Command audit log available when incidents occur" with note that the API doesn't yet expose a chatops audit endpoint. Slack integration card shows "not configured" until settings are populated.
10. `metrics.tsx` — all 6 charts built from real `MetricsSummary` structure: `incidents_total` (status→count bar), `investigation_duration_seconds` (histogram), `llm_calls_total` (success/error stacked by model), `llm_total_cost_usd` (cost bar), `sandbox_level_failures` (failure bar), `auto_merge_total` (donut). Empty state when `metrics === null`. Raw JSON dump at the bottom for debugging.
11. `settings.tsx` — uses `useSentinelStore(s => s.runtimeConfig)` for field values. Save button calls `updateRuntimeConfig(diff, apiToken)` directly (bypassing the store's `updateSettings` which doesn't accept a token). API token stored in localStorage. "Live" fields (matching `RuntimeConfig` keys) are editable; "startup" fields are read-only with a "startup" badge. 19 cards total (5 with live fields: LLM, Timeouts, Confidence, Sandbox, Observability, GitHub App; the rest are startup-only).
12. `api-explorer.tsx` — hardcoded endpoint list matching Go server routes (18 endpoints from `internal/api/server.go`). "Send" button makes a real `fetch()` to the endpoint with the user-supplied API token in the `Authorization: Bearer` header. Response panel shows real status, statusText, duration (ms), and pretty-printed JSON body. cURL preview updated live with the actual path (path params substituted) and token (truncated for display).

## Build verification

```
✓ Compiled successfully in 11.2s
  Running TypeScript ...
  Finished TypeScript in 5.2s ...
✓ Generating static pages using 3 workers (3/3) in 632ms

Route (app)
┌ ○ /
└ ○ /_not-found
```

- `out/index.html` regenerated (35,695 bytes).
- `next.config.mjs` has `typescript: { ignoreBuildErrors: false }` — all type errors are real errors. Build passed cleanly.
- No new dependencies added.

## Notable fixes to provided files

- `src/lib/api.ts` — added `export type { Incident, Provider, AutoMergeState, RuntimeConfig, StartupConfig, MetricsSummary }` re-export so `store.ts`'s `import { type Incident } from "./api"` resolves.
- `src/lib/store.ts` — fixed a type error in the SSE event handler: `data.message` is `unknown` (from `Record<string, unknown>`), which made `feedMsg` non-string. Added an `asStr()` helper that coerces `unknown` → `string` safely.

## Design rules followed

- **Loading states** — every section shows skeletons (animated pulse) when `loading && data.length === 0`.
- **Error states** — every section shows `<ErrorState>` with a Retry button when `error && data.length === 0`.
- **Empty states** — every section shows `<EmptyState>` with a friendly message + icon when data is genuinely empty (fresh install).
- **No `any` types** — all types come from `@/lib/types` or are inline-defined.
- **No fake data** — no fake timestamps, fake cluster status, fake user avatars, fake notifications. The version comes from `/healthz`. The clock is real (`new Date()`). The SSE status reflects the real `EventSource` connection.
- **Works with zero data** — dashboard, incidents, sandbox, PRs, providers, auto-merge, metrics, and settings all render sensible empty states when the API returns no data (fresh install).

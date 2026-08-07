// Package api provides the HTTP API server for Sentinel.
//
// The server exposes:
//
//   - Health + readiness probes (/healthz, /readyz) for kubelet.
//   - Incident CRUD (/incidents, /incidents/{id}) backed by feedback.Store.
//   - Admin actions (/admin/investigate, /admin/pause, /admin/resume) for
//     manual investigation triggers + pause/resume of autonomous mode.
//   - Settings (/settings, /settings/github-app, /settings/github-app-install)
//     for hot-reloadable runtime config + GitHub App management.
//   - Status (/providers, /automerge, /metrics, /metrics-summary) for the
//     dashboard's provider / auto-merge / Prometheus panels.
//   - SSE (/events) for live updates pushed from any component that calls
//     BroadcastEvent on the server (typically the agents pipeline).
//   - Webhooks (/webhook/github, /webhook/alertmanager, /slack/events)
//     forwarded to the chatops / alertmanager / slack handlers.
//   - pprof (/debug/pprof/*) for production profiling.
//   - /api/meta returns the chatops command list + available endpoints so
//     the frontend doesn't have to hardcode them.
//
// Bearer-token auth (configurable via SetAPIToken) gates admin endpoints.
// Health/readiness/metrics/SSE/web UI remain unauthenticated so probes +
// dashboards work without a token. A per-IP rate limiter (10 req/min) caps
// admin + webhook endpoints to prevent brute-force token attacks and
// webhook flood DoS.
//
// The Next.js dashboard is served at / via internal/web (embedded).
package api

# Sentinel API Reference

Sentinel exposes a REST API on port 8000 (the same port as the web UI). All admin endpoints require a Bearer token (`SENTINEL_API_TOKEN`), passed via the `Authorization` header.

## Authentication

```
Authorization: Bearer <token>
```

The token is set via `SENTINEL_API_TOKEN` env var or `sentinel.apiToken` Helm value. If empty, admin endpoints are unauthenticated (with a startup warning).

## Endpoints

### Health & Readiness

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/healthz` | No | Liveness probe — returns version + auth status |
| GET | `/readyz` | No | Readiness probe — returns "ready" when DB is connected |

### Incidents

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/incidents` | Yes | List recent incidents (query: `?limit=50`) |
| GET | `/incidents/:id` | Yes | Get a single incident by UUID or prefix |

### Admin

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/admin/investigate` | Yes | Trigger a manual investigation (body: `{"service":"...","namespace":"..."}`) |
| POST | `/admin/pause` | Yes | Pause autonomous detection (query: `?duration=30m`) |
| POST | `/admin/resume` | Yes | Resume autonomous detection |

### Settings

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/settings` | Yes | Get runtime config (live-editable fields) |
| PUT | `/settings` | Yes | Update runtime config (body: `RuntimeConfig` JSON) |
| GET | `/settings/github-app` | Yes | Get GitHub App status |
| POST | `/settings/github-app` | Yes | Configure GitHub App (body: `{app_id, private_key}`) |
| DELETE | `/settings/github-app` | Yes | Remove GitHub App config |
| POST | `/settings/github-app-install` | Yes | Set GitHub App installation ID |

### Metrics

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/metrics` | No | Prometheus-format metrics |
| GET | `/metrics-summary` | No | Aggregated metrics for the dashboard (incidents by status, LLM cost, sandbox stats) |

### LLM Providers

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/providers` | Yes | LLM provider pool status (health, circuit breakers, call counts) |

### Auto-Merge

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/automerge` | Yes | Auto-merge policy status (enabled, rate limit, cooldown, tracked PRs) |

### Real-Time Events

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/events` | Yes (via `?token=`) | SSE stream — incident + pipeline events in real time |

SSE events are sent as unnamed `data:` lines (JSON payload with `type` field). Event types:
- `connected` — initial connection ack
- `incident` — incident started/updated/resolved
- `pipeline` — pipeline phase started/complete

### Webhooks

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/webhook/github` | HMAC-SHA256 | GitHub webhook (issue_comment for chatops, pull_request for PR review) |
| POST | `/webhook/alertmanager` | Shared secret | Alertmanager webhook (v4 payload) |
| POST | `/slack/events` | Slack signature | Slack Events API |

### Debug

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/debug/pprof/` | Yes | pprof index (heap, goroutine, threadcreate, block, mutex) |
| GET | `/debug/pprof/cmdline` | Yes | pprof cmdline |
| GET | `/debug/pprof/profile` | Yes | pprof CPU profile (query: `?seconds=30`) |
| GET | `/debug/pprof/symbol` | Yes | pprof symbol table |
| GET | `/debug/pprof/trace` | Yes | pprof execution trace |

## Examples

### List incidents
```bash
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8000/incidents?limit=10
```

### Trigger manual investigation
```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"service":"demo-app","namespace":"sim"}' \
  http://localhost:8000/admin/investigate
```

### Subscribe to SSE events
```bash
curl -N -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8000/events?token=$TOKEN"
```

### Get heap profile
```bash
go tool pprof "http://localhost:8000/debug/pprof/heap?token=$TOKEN"
```

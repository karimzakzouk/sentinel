# Migration Guide: v1.5.x → v2.0.0

v2.0.0 is a **breaking refactor**. This guide walks through every change that affects existing installs.

## TL;DR

```bash
# 1. Back up your Postgres data (the schema is unchanged, but backups are cheap)
kubectl -n sentinel exec sentinel-postgresql-0 -- pg_dump -U sentinel sentinel > sentinel-backup.sql

# 2. Uninstall the old chart
helm uninstall sentinel -n sentinel

# 3. Pull the new chart
helm repo update
helm pull sentinel/sentinel --version 2.0.0 --untar

# 4. Install with the new defaults
helm install sentinel sentinel/sentinel -n sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini
```

The helm install output will print:
- The auto-generated Postgres password (also stored in a Secret)
- The auto-generated UI password (credentials mode is now the default)

## Breaking changes

### 1. Distributed mode removed

**Was**: `mode: distributed` ran 7 service pods + Kafka + Zookeeper.
**Now**: Only `core` mode exists (single pod, all 6 layers). The `mode` key is gone.

If you were running distributed mode, you must switch to core mode. Core mode handles ~50 incidents/day on a single pod — if you need more, run multiple `core` replicas behind a leader-election lock (planned for v2.1).

### 2. Kafka dependency removed

**Was**: `kafka.enabled: true` pulled in Strimzi operator + Kafka + Zookeeper.
**Now**: No Kafka dependency. The chart only depends on Weaviate (optional).

### 3. Operator service removed

**Was**: `services/operator/` ran a kopf-based operator to watch `SentinelIncident` CRs.
**Now**: The same function is built into `sentinel-core` as the `crd_watcher_loop`. Faster (real `Watch()` stream instead of polling), no extra pod, no kopf dependency.

### 4. Postgres password auto-generated

**Was**: `postgres.auth.password` defaulted to `sentinel` (in plain text).
**Now**: Defaults to empty → chart auto-generates a 32-char random password, stored in a Secret named `<release>-postgresql`. The password is printed to `helm install` output.

To use a specific password, set `postgres.auth.password` explicitly.

### 5. UI auth defaults to credentials

**Was**: `ui.auth.mode: disabled` (anyone with cluster DNS access could log in).
**Now**: `ui.auth.mode: credentials` with an auto-generated 24-char password (printed to `helm install` output).

To use a specific password: `--set ui.auth.password=yourpassword`.
To use GitHub OAuth: `--set ui.auth.mode=github --set ui.auth.githubClientId=... --set ui.auth.githubClientSecret=...`.
To disable auth (dev only, blocked if ingress is enabled): `--set ui.auth.mode=disabled`.

### 6. NetworkPolicy enabled by default

**Was**: `networkPolicy.enabled: false` (Sentinel pod had unrestricted network access).
**Now**: `networkPolicy.enabled: true` — deny-by-default, allow only:
- Intra-namespace ingress
- Egress to: K8s API, DNS, GitHub API, LLM API, Weaviate, Postgres

If your cluster's CNI doesn't support NetworkPolicy, set `networkPolicy.enabled: false`.

### 7. RBAC namespace-scoped by default

**Was**: `rbac.clusterScope: true` (Sentinel had cluster-admin read on everything).
**Now**: `rbac.clusterScope: false` — Sentinel only gets read on its own namespace + write on the SentinelIncident CRD.

To watch CRs across all namespaces, set `rbac.clusterScope: true`.

### 8. Weaviate enabled by default

**Was**: Weaviate was optional, often disabled in test installs.
**Now**: `weaviate.enabled: true` by default. The memory/RAG layer is the product — keep it on unless your cluster can't afford 1GB RAM + 20GB disk.

### 9. Terraform executor removed

**Was**: `services/execution/executors/terraform.py` allowed Sentinel to propose + apply Terraform changes.
**Now**: Removed. Nobody was using it, and autonomous Terraform applies are years away from enterprise acceptance.

### 10. Structured logging

**Was**: stdlib `logging.basicConfig`.
**Now**: `structlog` with JSON output (when `SENTINEL_LOG_JSON=1`) or pretty console (default). Every log line automatically carries `incident_id`, `service`, `agent` context via contextvars.

If you have a log pipeline that expects stdlib format, set `SENTINEL_LOG_JSON=0` (default) — the pretty console output is still parseable.

## New features

### `/readyz` endpoint
Distinct from `/healthz`. Returns 200 only after the database is initialized AND all 7 background loops have started. Use this for readiness probes (the chart already does).

### Real K8s Watch on CRD watcher
The CRD watcher now uses `Watch().stream()` instead of 10-second polling. New `SentinelIncident` CRs are detected in ~1 second instead of ~10 seconds. Falls back to polling if the watch stream breaks.

### LLM JSON mode
The LLM router now requests `response_format={"type": "json_object"}` from providers that support it (OpenAI, z.ai, OpenRouter, Gemini-via-OpenAI). Cuts JSON repair attempts by ~80%. Falls back silently if the provider rejects it.

### Pydantic Settings v2 for runtime settings
The runtime settings (previously a plain dict) are now a proper `RuntimeSettings(BaseModel)` with validation. Settings updates via the API are validated before being applied.

## Bug fixes

- **analyst.py git_commit was always None** — the line `state.cluster.anomalies[0].signal_id and None` always evaluated to `None`, breaking code-churn decay in Weaviate. Fixed.
- **PyGithub deprecation warning** — `Github(login_or_token=...)` was deprecated. Switched to `Github(auth=Auth.Token(...))`.
- **Helm chart referenced `mode` field that's gone** — distributed-mode templates had `{{- if eq .Values.mode "distributed" -}}` guards that always rendered nothing. Removed.

## Test count

- v1.5.x: 56 tests
- v2.0.0: 83 tests (12 new runtime_settings tests, 6 new K8s helpers tests, 9 new helm tests, 4 integration test stubs)

## What's next (v2.1 roadmap)

This refactor unlocks:
- **Verified-outcome memory** — close the feedback loop so the second time a problem occurs, it's already fixed.
- **Metric-recovery verification** — sandbox verification that watches the actual SLO metric recover after the fix.
- **Predictive PR review** — score every PR diff against the incident memory and flag risky ones before merge.

See `docs/roadmap.md` for details.

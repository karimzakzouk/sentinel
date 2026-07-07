# Sentinel internal/ — package map

This file is a quick orientation guide for the internal/ tree. Every package
has a doc comment on its `package` declaration explaining why it exists; this
file is the higher-level map.

## Core pipeline (existing)

| Package | Role |
|---|---|
| `detection` | Watches K8s events, fires incidents on CrashLoopBackOff / OOMKilled / image pull errors. |
| `alertmanager` | Receives Alertmanager webhooks, normalizes to SentinelIncident. |
| `correlation` | Sliding-window detection of cascading failures across services. |
| `depanalysis` | Reverse-engineers service call graphs from network flows + traces. |
| `agents` | LLM-driven investigation, fix proposal, edit-block application. |
| `executors` | Sandbox runner, syntax validator, test runner, GitHub search/replace. |
| `fixverify` | Verifies a proposed fix in a sandbox before merge. |
| `automerge` | 11-gate policy that decides whether to merge a Sentinel PR. |
| `prreview` | PR review + comment posting. |
| `feedback` | Incident memory — SQLite + vector store for similar-incident retrieval. |
| `emb` | Embedding providers (remote OpenAI, local TF-IDF hash fallback). |

## Infrastructure (existing)

| Package | Role |
|---|---|
| `k8s` | Kubernetes client wrapper. |
| `llm` | Multi-provider LLM failover pool. |
| `github` | GitHub App client (clone, commit, PR, comment). |
| `config` | Single source of truth for runtime config. |
| `runtimecfg` | Hot-reload of config (no restart needed for most fields). |
| `safety` | Embedded policy engine (5 plain Go rules; replaces the v2 OPA sidecar). |
| `safeactions` | The controller every write action goes through. |
| `chatops` | 19 `/sentinel` Slack commands across 3 permission tiers. |
| `slackbot` | Slack bot adapter. |
| `api` | HTTP API server (REST + SSE). |
| `web` | Embedded Next.js dashboard (built into the binary). |
| `metrics` | Prometheus metrics endpoint. |
| `tracing` | OpenTelemetry tracing setup. |
| `logging` | slog setup. |
| `schemas` | Shared types. |
| `events` | In-process event bus (SSE broadcaster). |
| `errorparse` | Error-string pattern matching. |
| `repo` | git clone + checkout helpers. |
| `watcher` | SentinelIncident CR controller. |
| `version` | Build-time version. |
| `errs` | Error wrapping. |

## Tier 1 — closes the autonomous loop (NEW)

| Package | Role |
|---|---|
| `postmerge` | **Post-merge regression watcher + auto-rollback.** Phase 1 (10 min) + Phase 2 (24h) monitoring of every merged PR. Reverts via `git revert` if metrics regress past threshold. This is the "un-fix it at 3:05am" half — no other AI-SRE tool does this. |
| `slo` | **Proactive SLO burn-rate watcher.** Google SRE workbook multi-window burn-rate rules (14.4x/6x/3x/1x). Fires synthetic incidents BEFORE the crash. Feeds into the existing investigation pipeline. |
| `causal` | **Causal root-cause attribution.** Granger-causality + topological sort on the dependency DAG. Tells you WHICH service caused a cascade, not just WHICH services were affected. Extends `correlation` + `depanalysis`. |
| `admission` | **Validating admission webhook.** Sentinel as a Kyverno replacement — but with incident memory. Blocks deploys that look similar to past incidents. Static policies (no-privileged-pods, require-resource-limits, block-bad-images) + learned policies (similar-to-past-incident). |

## Tier 2 — the brain (NEW)

| Package | Role |
|---|---|
| `attest` | **Signed audit trail (EU AI Act + SOC 2 + IETF Agent Audit Trail).** Every autonomous action emits an Ed25519-signed attestation with LLM provenance (model, prompt hash, tokens, cost), evidence, and a SHA256 hash chain. Verifiable by external auditors. |
| `observability` | **OTel + Prometheus + Loki + Tempo ingestion.** `PrometheusQuerier` implements both `slo.MetricsQuerier` and `postmerge.MetricsSource`. Loki + Tempo clients pull logs + traces as investigation evidence. |
| `llmcost` | **Cost-aware LLM routing + semantic cache.** Task-complexity classifier (trivial/medium/hard) routes to the appropriate model. Cosine-similarity cache (0.95 threshold) serves identical-ish prompts from cache. Target: 70% cost reduction per incident. |
| `chaos` | **Self-chaos mode.** Scheduled chaos experiments against staging (kill-pod, mutate-configmap, restart-deployment). Sentinel investigates its own injections. Output: eval data for sentinel-bench + few-shot examples for the feedback store + public "we ran N chaos experiments this month, fixed M" stat. |

## Sentinel-Bench (NEW, repo-level)

`/sentinel-bench/` — public eval harness. Scenario format, runner, results.
See `sentinel-bench/README.md` for the methodology, metrics (DBR/VPR/TAR/FMR),
and contributing guide.

## Wiring (TODO)

These packages are designed + compile + vet clean, but are NOT yet wired
into `cmd/sentinel/main.go`. The wiring is the next commit — each package
needs:

1. A constructor call in `main.go` with config from `internal/config`
2. A goroutine `Start(ctx)` invocation
3. An integration point into the existing pipeline (e.g. `postmerge.Watcher`
   subscribes to `automerge.Merger`'s `OnMerged` callback)
4. A chart/values.yaml surface for operators to enable/disable

The packages are deliberately self-contained so the wiring commit is small
and reviewable.

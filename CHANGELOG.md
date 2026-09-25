# Changelog

All notable changes to Sentinel are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.7.12] — 2026-09-26

### Changed

- **Evidence gathering is no longer skippable by self-reported confidence**
  — the "confidence fast path" (top hypothesis >= 0.7 skips the evidence
  gatherer) is now OFF by default; `SENTINEL_EVIDENCE_FAST_PATH=true`
  restores it. Eval run #26: user-api's top hypothesis (a hallucinated
  config story, 0.78) skipped verification entirely, patched the wrong
  file (db.ts instead of the leaking tokenStore.ts), and the wrong fix
  was auto-merged. LLM self-reported confidence must never gate
  verification — it is not calibrated.
- **Sandbox Level 1 now checks restart stability and OOMKills** — a
  canary that OOM-loops flips Ready between kills and passes any
  duration-only Ready window. Level 1 now (a) fails immediately if the
  canary container was ever OOMKilled (exit 137), (b) resets the
  sustained window whenever the restart count increases (crash-looping
  while Ready = not stable), and (c) fetches the PREVIOUS container's
  logs (`kubectl logs --previous`) when the container restarted, so the
  Level 3 oracle and the fix-retry feedback see the actual crash
  evidence instead of the fresh container's clean logs.

### Added

- **CI-failure feedback loop** (`SENTINEL_AUTOMERGE_CI_RETRIES`, default
  1) — when CI fails on a tracked auto-merge PR, the merger fetches the
  failing test output (failed Actions jobs' logs: test names, assertion
  messages, build errors; check-run annotations as fallback), feeds it
  to the fix proposer as structured feedback, pushes the corrected fix
  to the SAME branch (the open PR updates and CI re-runs automatically),
  and re-verifies in the sandbox before letting the merge gates run
  again. Bounded by the retry count; the in-flight guard prevents the
  30s poll loop from double-triggering; the counter survives restarts
  (new `ci_retries` column in `tracked_prs`). Run #26: shipping-api's
  fix stopped the crash but broke the `calculateQuote_rejectsBlank-
  DestinationZip` contract test — the PR sat unmerged until the harness
  closed it. With the loop, the fix proposer gets the failing test name
  and revises.
- **Post-deploy health check + auto-revert**
  (`SENTINEL_POSTDEPLOY_CHECK_ENABLED`, default false) — after a
  successful merge, watches the deployed service's pods for up to
  `SENTINEL_POSTDEPLOY_CHECK_DURATION_SEC` (default 480s): waits for the
  new ReplicaSet to roll out, then fails the merge on (a) any OOMKill
  on a rolled-out pod, (b) any restart of a pod that had been Ready,
  or (c) ~2 minutes of zero Ready replicas. On regression: opens a
  revert PR (optionally auto-merged, `SENTINEL_POSTDEPLOY_REVERT_AUTO_
  MERGE`), comments on the original PR, and records
  `reverted:postdeploy_regression`. Optionally triggers the global
  auto-merge cooldown (`SENTINEL_POSTDEPLOY_REGRESSION_COOLDOWN`,
  default true). Closes the run #26 user-api blind spot: the sandbox
  canary has no traffic, so a memory leak that only manifests under
  load passes L1-L5 — but the DEPLOYED pods OOM-loop under real
  traffic, and a pod that flips Ready between OOM kills is NOT
  recovered.

## [1.7.11] — 2026-09-25

### Fixed

- **Auto-merge untracks PRs before CI has started** — a PR evaluated
  seconds after opening reports "neutral" CI state with zero check
  runs, which the gate treated as a hard failure (run #24: every PR
  untracked before the first check even registered). Neutral + 0 check
  runs now counts as pending and keeps waiting, bounded by the CI
  timeout.

## [1.7.10] — 2026-09-25

### Fixed

- **Sandbox Level 2 probes hardcoded /healthz-style paths** — the
  canary probed a fixed candidate set that 404'd on services whose
  real health endpoints differ. Probe paths are now derived from the
  workload's own readiness/liveness httpGet probes (all must pass),
  falling back to the convention candidate set (any-of).

## [1.7.9] — 2026-09-25

### Fixed

- **Chart RBAC: sandbox cannot copy secrets into sandbox namespaces**
  — `CopyConfigMapsAndSecrets` reads the prod Deployment's secretRefs
  and re-creates those Secrets next to the canary, but the sandbox
  ruleset granted create only on pods/services/configmaps. With
  `sentinel.rbac.readSecrets=true` the reads succeeded while the
  creates returned `secrets is forbidden`, so every canary pod sat in
  CreateContainerConfigError and Level 1 never went Ready — eval run
  #22 lost 10/10 sandbox attempts this way (with the v1.7.8 Kaniko
  fix confirmed working: 8/9 canary builds succeeded). Adds `secrets`
  to the sandbox create ruleset (gated on `sentinel.sandbox.enabled`,
  same trust level as the existing cluster-wide create-deployments:
  a Deployment can already envFrom-project any secret without read
  RBAC, so this grant does not widen the threat model).

## [1.7.8] — 2026-09-25

### Fixed

- **Kaniko canary builds fail with EBUSY on kind nodes** — Kaniko
  builds an image by deleting its own root filesystem, and paths that
  are bind-mounted into the pod cannot be unlinked. On kind (node =
  Docker container), every pod inherits the host's `/product_uuid`
  bind mount, so every canary build died *after* pushing the cache
  layer with `error building image: deleting file system after stage
  0: unlinkat //product_uuid: device or resource busy`. Eval run #21
  lost all three canary builds this way: no canary image → sandbox
  never verified → auto-merge gate 3 (`sandbox`) closed → PRs closed
  unmerged at harness cleanup. With zero merged PRs, the
  deploy→Argo→recovery chain never fires at all. Fix: pass
  `--ignore-path=/product_uuid` (and, defense-in-depth,
  `--ignore-path=/var/run/secrets/kubernetes.io/serviceaccount` —
  runtime mounts must never leak into image layers or the layer cache
  anyway) to the Kaniko executor.

## [1.7.7] — 2026-09-25

### Added

- **Fix-service budget (`SENTINEL_MAX_FIX_SERVICES`, chart value
  `sentinel.maxFixServices`)** — caps how many distinct services the
  process will run the LLM investigation→fix pipeline for (0 =
  unlimited, the default). Each incident pipeline costs 30-60k tokens
  across investigation rounds and fix proposals, and concurrent
  pipelines share the provider's per-org daily quota: Groq's TPD is
  200k, so four eval runs in one day drained it before the fifth
  started — run #20 opened zero PRs with every fix proposal 429ing
  against an exhausted pool. Over-cap and repeat detections are
  recorded as new `skipped` incidents (visible in the API and eval
  scoreboard, zero LLM cost); manual, Slack, chaos and CR triggers
  bypass the cap so operator intent is never refused. The budget is
  claimed atomically before any semaphore slot or LLM call is
  consumed, and re-detection waves of an already-attempted service
  (the soak-end token burn seen since run #14) no longer re-enter the
  pipeline.

## [1.7.6] — 2026-09-25

### Fixed

- **Pool failover sent the primary provider's model name to every
  provider — the overflow was never callable.** The tier methods
  (FastJSON/FrontierText/...) pass the ROUTER's model name into
  `completeWithPool`, which handed the same string to every pool entry,
  ignoring each entry's own `fastModel`/`frontierModel`. With groq
  primary + gemini overflow, every failover called Google with
  `openai/gpt-oss-120b` and died with
  `GenerateContentRequest.model: unexpected model name format` (run #19:
  pool "active", all 6 incidents failed, 0/9 fixes — groq's free-tier
  TPM 8k/TPD 200k exhausts within minutes, so the run lives or dies on
  the overflow). New `poolModelForRouterModel()` remaps the router-level
  tier name to each entry's own model for that tier;
  `TestRouter_PoolRemapsModelPerProvider` proves end-to-end (httptest
  servers capture the wire-level model per provider) that a failed-over
  frontier call arrives at the overflow as the overflow's frontier
  model, not the primary's.

## [1.7.5] — 2026-09-24

### Fixed

- **LLM failover pool never activated — `SENTINEL_LLM_PROVIDERS` was
  silently ignored.** `loadLLMProviders()` existed and was fully
  unit-tested, but nothing called it: `config.Load()` never assigned
  the `LLMProviders` field, so every deployment booted in
  single-provider mode no matter what the chart delivered (the env var
  itself was rendered and delivered correctly all along). One-line
  wiring fix plus `TestLoadWiresLLMProviders`, a regression test that
  calls `Load()` end-to-end and fails if the env var does not reach
  `cfg.LLMProviders` — unit-testing the parser alone is exactly how
  this shipped broken.

## [1.7.3] — 2026-09-24

Investigation-fidelity fixes, all surfaced by the marketly eval run #10
(4 incidents → 4 PRs, 1 correct root cause, 3 misdiagnoses).

### Fixed

- **Tail-based pod-log fetch** — crash signatures (Node
  `FATAL ERROR: Ineffective mark-compacts`, Rust panics, Go panics) sit
  at the END of the previous container's output. Both fetchers
  (`internal/k8s.FetchCrashingPodLogs`, detection-layer
  `fetchPodLogs`) previously read the FIRST 8KB (`LimitBytes` from the
  start), which on a chatty pod cuts off the very line that names the
  failure — run #10's user-api heap-exhaustion was misdiagnosed as a
  Postgres-pool issue for exactly this reason. Both now request the
  last ~300 lines and keep the final 64KB (`keepLogTail`), so the
  signature always survives truncation.
- **Empty-cause cascades no longer suppress investigations** — the
  correlator returned "suppress" for ANY 3-service cascade, including
  ones with no attributable deployment cause. Under shared load,
  several services erroring in the same minute is not evidence that
  any of them is a victim; run #10's empty-cause cascade silently
  swallowed a fully-detected Rust panic. `RegisterIncident` now
  suppresses only when the emitted event carries a `LikelyCause`; the
  cascade alert + causal analysis still fire either way.
- **Ruby exception-class patterns** (`NoMethodError`, `NameError`,
  `ArgumentError`, `RuntimeError`, `ThreadError`, `LockError`, …) —
  Sidekiq logs job exceptions at WARN with a timestamp prefix, which
  the anchored patterns never matched. Verified against the
  analytics-worker `RedisClient` API-mismatch failure from run #10;
  backtrace-only lines asserted as non-errors.

## [1.7.2] — 2026-09-24

Chart safety fix, surfaced by the marketly eval the moment the values
nesting bug was corrected: the write-namespaces loop rendered a
`kind: Namespace` object for every `sentinel.rbac.writeNamespaces`
entry. Since write namespaces are pre-existing by definition, this (a)
made `helm install` fail with an ownership-metadata error whenever the
namespace existed without Helm labels, and (b) meant `helm uninstall`
would attempt to **delete the watched namespace and everything running
in it**. The object is removed; only the write Role + RoleBinding are
created per namespace. If the namespace does not exist, Role creation
fails with a clear "namespace not found" — create it first.

## [1.7.1] — 2026-09-24

Detection-coverage + observability patch, driven by the first full
marketly eval run (9 services, 9 planted bugs): the investigation
pipeline solved a Rust panic in 9 seconds, but real-world worker/log
formats slipped past the error-pattern matcher, and a namespace-allowlist
misconfiguration silently swallowed a completed fix.

### Added

- **Error patterns for worker/structured log formats** (`internal/errorparse`):
  - Celery (`ERROR/ForkPoolWorker-N`, `ERROR/MainProcess`)
  - Python resolver/socket failures logged mid-line (`gaierror`,
    `Name or service not known`, `ConnectionRefusedError`,
    `connection refused` case-insensitive)
  - Python structlog padded levels (`[error    ]`, `[critical  ]`)
  - Sidekiq/Ruby lock failures (`LockError`, `could not acquire lock`,
    `deadlock` case-insensitive)
  - Spring Boot ` ERROR <pid> --- ` severity lines
  - `java.util.*Exception` (e.g. `NoSuchElementException` — previously
    only `java.lang.*` matched)
  - lowercase `Segmentation fault`
  Each format also got an extraction fallback so incident titles stay
  meaningful (`celery_task_error`, `deadlock`, `lock_acquire_timeout`,
  `spring_error`, `segfault`, …). Patterns verified against real log
  lines captured from the eval; same-format INFO/WARN lines asserted
  as non-errors.

### Changed

- **Namespace-allowlist denial is now loud and attributed**: when a fix
  is approved and generated but the target namespace is not in
  `SENTINEL_WRITE_NAMESPACES`, the incident records the reason in
  `state.error`, an `execution/denied_namespace_allowlist` event is
  appended, a `pipeline` SSE event with the reason is broadcast, and
  the log line moves from WARN to ERROR with the chart-value hint.
  Previously the incident silently reverted to `escalated` with no
  explanation — in the eval this masked a complete, correct fix.

## [1.7.0] — 2026-09-23

Durability + honesty-of-signals release. Four production-hardening fixes
from a full code review, plus a real-cluster e2e test in CI.

### Added

- **Restart-safe auto-merge** — tracked PRs are now persisted to SQLite
  (`tracked_prs` table) and re-adopted at startup via `Merger.Reconcile`.
  A pod restart no longer silently orphans in-flight auto-merges; PRs
  merged/closed by a human during downtime are detected and pruned
  (`skipped:closed_while_down`), and the poll loop untracks PRs closed
  mid-flight (`closed_before_merge`).
- **Sandbox verification levels surfaced everywhere** — `SandboxResult`
  now records how far verification got (`LevelsVerified`, e.g. "L1-L5"
  vs "L1-L3" when no canary image was built). The PR label shows it
  (`sandbox-verified:L1-L5`), the incident records it, and a new
  auto-merge gate enforces a configurable minimum
  (`SENTINEL_AUTOMERGE_MIN_SANDBOX_LEVEL`, default 3 = historical
  behavior; set 5 to require the full suite).
- **kind-cluster e2e workflow** (`.github/workflows/e2e.yml`) — installs
  the chart into a fresh kind cluster, deploys a deliberately-crashing
  service, and asserts the detection layer files an incident. Weekly
  schedule + manual dispatch.
- **Helm values** for the new knobs: `sentinel.sandbox.networkPolicyRequired`
  (default true), `sentinel.autoMerge.minSandboxLevel` (default 3).
- **Tests**: detection integration tests against the fake clientset
  (Deployment→ReplicaSet→Pod owner-chain repo discovery, CrashLoopBackOff /
  OOMKilled classification, cooldown suppression, unannotated-pod
  negative case); a full auto-merge truth table (24 gate cases incl.
  overrides, rate limit, cooldown); reconciliation round-trip tests;
  SQLite tracked-PR persistence tests; sandbox fail-closed/fail-open tests.

### Changed

- **Sandbox NetworkPolicy now fails closed** — if the default-deny policy
  cannot be created, the run aborts with `networkpolicy_create_failed`
  (InfrastructureError) instead of deploying a canary with unrestricted
  egress. Fail-open remains available as an explicit opt-out
  (`SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED=false`) for restricted
  clusters.
- **README rewritten** — cleaner structure, accurate architecture
  diagram, level-labeled verification, updated configuration reference.

### Fixed

- CI-timeout PR comment read as if Sentinel were merging the PR itself
  ("Merging manually") — now reads "Merge the PR manually instead."
- Doc drift: security scanner comments said 18 patterns; there are 19.
- Stale `SetAPIToken` doc comment claimed SSE `/events` was unauthenticated;
  it is token-gated via `?token=` (the comment now matches the code).

## [1.0.0] — 2026-08-08

Complete refactor from the ground up. New orphan branch, no shared history
with previous versions. Every file rewritten clean.

### Added

- **38 internal Go packages** — all with doc comments, tests, and structured
  logging via slog.
- **3 binaries**: `sentinel` (server), `sentinelctl` (CLI), `sentinel-bench`
  (eval harness).
- **5-level sandbox verification** — L1 Pod Ready + sustained, L2 health
  endpoints, L3 error-gone oracle, L4 regression test, L5 reproducer (L4+L5
  parallel).
- **Defense-in-depth prompt-injection defense** — repo allowlist, path-confined
  ReadFile, 18-pattern security scanner, reviewer-agent LLM, sandbox
  NetworkPolicy default-deny.
- **Tier 1 features**: post-merge regression watcher + auto-rollback, proactive
  SLO burn-rate watcher, causal root-cause attribution (Granger-causality),
  validating admission webhook (Kyverno-with-memory).
- **Tier 2 features**: Ed25519-signed audit trail (EU AI Act / SOC 2 compliant),
  cost-aware LLM routing + semantic cache, self-chaos mode.
- **sentinel-bench** — real eval harness with 3 SHA256-pinned scenarios
  (S001: crashloop-image-typo, S002: python-keyerror-env-var, S003:
  go-nil-pointer-panic). DBR/VPR/TAR/FMR metrics.
- **Helm chart** with ingress template, least-privilege RBAC, NetworkPolicy,
  PDB, ServiceMonitor.
- **CI pipeline**: golangci-lint, go test -race, go vet, helm lint, helm
  template, Trivy container scan, govulncheck, gosec, CodeQL.
- **Release pipeline**: native ARM64 builds (no QEMU), multi-arch manifest,
  cosign keyless signing, SPDX SBOM, Helm chart publish to gh-pages.
- **Next.js 16 frontend** with 12 sections, 87 vitest tests, ESLint flat config.
- **19 `/sentinel` chatops commands** across 3 permission tiers.
- **11-gate auto-merge** with per-PR mutex (prevents double-merge).
- **20+ LLM providers** via GoAI with multi-provider failover pool + circuit
  breaker.
- **SQLite + HNSW vector memory** for similar-incident retrieval + Beta priors.

### Fixed (vs v5)

- **Credentials race in LLM router** — v5 swapped `r.providerType/baseURL/apiKey`
  under the lock for each pool call; two concurrent calls could see each other's
  credentials. v1: `buildGoAIModel` takes a `providerCfg` value, no global state
  mutated per call.
- **Semantic cache unbounded growth** — v5 cache never evicted. v1: FIFO eviction
  at `maxEntries` (default 1000) + TTL checked on both exact-match and
  semantic-search paths.
- **Clusterer data race** — v5 `emit()` read `c.pauseChecker` without the lock.
  v1: copy under the lock into a local, call outside the lock.
- **Investigator double-iteration bug** — v5 `runWithoutRepo` incremented
  `state.Iterations` despite the explicit comment at `runWithRepo` removing it.
  v1: `pipeline.go` is the sole owner of the counter.
- **Fake UUIDv7 in attest** — v5 filled random bits with reshuffled timestamp.
  v1: real RFC 9562 UUIDv7 with `crypto/rand` + 48-bit Unix ms timestamp.
- **Missing `ingress.yaml` Helm template** — v5 had the values block but no
  template. v1: proper Ingress with className, annotations, TLS, host/path.
- **RBAC over-permissioning** — v5 granted cluster-wide secrets read when
  `sandbox.enabled` (default true). v1: secrets read gated ONLY on
  `rbac.readSecrets` (default false).
- **17 adapter structs → 1 wiring struct** — v5 had 17 adapter structs in
  `cmd/sentinel/`. v1: 1 `wiring` struct with 26 interface-satisfying methods.
- **`main.go` 1,618 LOC → 263 LOC** — v1 entry point is readable in 5 minutes.
- **Frontend: `import * as Icons`** — killed tree-shaking, shipped thousands of
  unused icons. v1: named imports only.
- **Frontend: loading flicker** — v5 set `loading: true` on every SSE refetch.
  v1: only on initial fetch.
- **Frontend: fabricated sandbox data** — v5 synthesized L1-L5 from incident
  status + regex. v1: shows "No sandbox data" when the API returns nothing.
- **Frontend: hardcoded chatops commands** — v5 hardcoded in `chatops.tsx`,
  drifted from Go. v1: fetched from `/api/meta`.
- **Frontend: `window.prompt`/`window.alert`** — v1: shadcn Dialog.
- **Frontend: `typescript@^6`/`@types/node@^26`** — both unreleased. v1: `^5`/`^22`.
- **Frontend: 0 tests, 0 ESLint** — v1: 87 vitest tests + ESLint flat config.
- **CI: trivy pinned to `@master`** — supply-chain risk. v1: `@v0.36.0`.
- **CI: no image signing** — v1: cosign keyless + SPDX SBOM.
- **CI: no CodeQL/gosec** — v1: both added.
- **`log.Printf` everywhere** — v1: structured slog throughout.
- **AI-slop comments** — every "v3.x.x", "H30 -", "the previous default was",
  "was missing", etc. stripped. Comments explain WHY, never what was wrong before.

### Removed

- All v5.x history (orphan branch, no shared commits).
- `my-values.yaml.example` (operators use `--set` or a values file).
- `agent-ctx/` directory (AI-agent context files — not needed in v1).
- `examples/python-crashloop/` (replaced by `sentinel-bench/` scenarios).
- Stale `CHANGELOG.md` entries (v5.x and earlier).
- `businessHoursOnly`/`Timezone` fields from chaos config (operators use cron).

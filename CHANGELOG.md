# Changelog

All notable changes to Sentinel are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

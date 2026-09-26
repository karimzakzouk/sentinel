# Sentinel

**The autonomous AI SRE.** Sentinel watches your Kubernetes cluster for production crashes, finds the root cause, writes the fix, verifies it in a sandbox, and opens a Pull Request — without waking you up.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![Image Size](https://img.shields.io/badge/image-43MB-success)](https://hub.docker.com/r/karimzakzouk/sentinel)
[![Pods](https://img.shields.io/badge/pods-1-blue)]()
[![Arch](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success)]()
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Version](https://img.shields.io/badge/version-1.7.0-cyan)]()

[Install](#install) · [How It Works](#how-it-works) · [Architecture](#architecture) · [Configuration](#configuration) · [CLI](#cli) · [Web UI](#web-ui)

---

Most SRE tooling stops at *alerting*. PagerDuty pages you. Datadog graphs it. Copilot suggests a fix. Sentinel **closes the loop**: it detects the crash, investigates the code, proposes and verifies a fix, and opens a PR — in one pipeline, on one pod, with no external dependencies.

**One binary. One pod. 43MB image. ~50MB RAM. 9 direct Go dependencies.**

## Install

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 1.7.17 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

That's it. One pod, ready in under a second:

```bash
kubectl -n sentinel get pods
# sentinel-xxx   1/1 Running   0   1s
```

## Enroll your services

Annotate your Deployments so Sentinel knows what to watch:

```yaml
metadata:
  annotations:
    sentinel.io/repo: myorg/my-app           # GitHub repo (required)
    sentinel.io/branch: main                  # Branch that's deployed
    sentinel.io/git-commit: abc123...         # Exact commit SHA (optional)
```

Sentinel then live-streams the pod's logs and status. When a crash appears — `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, or a stack trace in the logs — the full pipeline runs. No manual triggers needed.

## How It Works

```
                        ┌─────────────────────────────────────────────────────────┐
                        │                     Sentinel pod                         │
                        │                                                         │
  Kubernetes ──watch──▶ │  ┌───────────┐   ┌────────────┐   ┌───────────┐        │
  (pod status, logs)    │  │ DETECT    │──▶│ INVESTIGATE│──▶│ ANALYZE   │        │
  Alertmanager ─hook──▶ │  │           │   │            │   │           │        │
                        │  │ log stream│   │ clone repo │   │ Bayes +   │        │
                        │  │ pod status│   │ agentic LLM│   │ falsify   │        │
                        │  │ webhooks  │   │ search+read│   │ hypotheses│        │
                        │  └───────────┘   └────────────┘   └─────┬─────┘        │
                        │                                        │              │
                        │                        ┌───────────────▼──────────┐   │
                        │                        │ GOVERN (5 embedded rules)│   │
                        │                        │ prod ns · blast radius · │   │
                        │                        │ confidence · migrations  │   │
                        │                        └───────────────┬──────────┘   │
                        │                                        │              │
                        │  ┌───────────────┐   ┌─────────────────▼──────────┐   │
                        │  │ OPEN PR       │◀──│ EXECUTE: 5-level sandbox   │   │
                        │  │ sandbox-      │   │ Kaniko canary → verify →   │   │
                        │  │ verified:L1-L5│   │ retry with LLM feedback    │   │
                        │  └───────┬───────┘   └────────────────────────────┘   │
                        │          │                                              │
                        │          ▼                                              │
                        │  ┌──────────────────────────────────────────────────┐ │
                        │  │ SQLite: incidents · events · beta priors ·       │ │
                        │  │ vector memory · tracked PRs (restart-safe)       │ │
                        │  └──────────────────────────────────────────────────┘ │
                        └─────────────────────────────────────────────────────────┘
```

**Detect** — live log streaming + pod status watching + Alertmanager webhooks.

**Investigate** (agentic) — clones the repo locally (~2s with go-git), then runs an LLM agent loop: the agent emits searches, reads files, forms hypotheses, and can revise when evidence contradicts it (planning, stuck-detection, a critic pass on low confidence, and context condensation on long investigations).

**Analyze** — pure math, no LLM: falsification + Bayes' rule + Beta-distribution priors pick the surviving hypothesis.

**Govern** — 5 embedded Go rules: production namespace, high-risk service, blast radius, schema-migration rollback block, confidence thresholds. No OPA sidecar.

**Execute** — pushes the fix to a branch, builds a canary image with Kaniko, verifies it in an isolated namespace, opens the PR.

### The 5-level sandbox

Sentinel doesn't open a PR and hope. It verifies the fix in a disposable K8s namespace:

| Level | What it checks | Failure feedback to LLM |
|---|---|---|
| **L1: Pod Ready** | Canary becomes Ready AND stays Ready | "Startup crash not fixed" |
| **L2: Health endpoints** | `/`, `/healthz`, `/readyz` return 2xx/3xx | "App starts but health endpoints fail" |
| **L3: Error gone** | Original error absent from canary logs | "Fix only masked the symptom" |
| **L4: Regression test** | LLM-generated test that reproduces the bug PASSES | "You masked the symptom, didn't fix the bug" |
| **L5: Reproducer** | LLM-generated reproducer script exits 0 | "Strongest signal the fix is incomplete" |

The PR label tells you exactly how far verification went: `sandbox-verified:L1-L5` (full suite) vs `sandbox-verified:L1-L3` (no canary image — L4/L5 skipped). Auto-merge enforces a configurable minimum (`SENTINEL_AUTOMERGE_MIN_SANDBOX_LEVEL`, default 3). When a level fails, Sentinel feeds the specific failure back to the LLM and retries.

### Prompt-injection defense (in depth)

Sentinel reads attacker-controlled pod logs. Five layers prevent a tampered log from becoming a malicious PR:

1. **Repo allowlist** — only investigates repos in the configured allowlist
2. **Path-confined reads** — LLM file paths cannot escape the cloned repo
3. **19-pattern security scanner** — blocks `os.system`, `eval`, `pickle.loads`, `curl|bash`, hardcoded secrets, Dockerfile tampering, env exfiltration
4. **Reviewer agent** — a second LLM with a different prompt audits every fix
5. **Sandbox NetworkPolicy default-deny** — and since 1.7 the sandbox **fails closed**: if the policy can't be created, the run aborts instead of deploying a canary with unrestricted egress

### Durability

- **Restart-safe auto-merge** — tracked PRs are persisted to SQLite and re-adopted on boot. A pod restart (or node upgrade) no longer orphans in-flight PRs; PRs merged by a human during downtime are detected and pruned.
- **Post-merge regression watcher** — every merged PR is monitored (10 min + 24 h) and auto-reverted via `git revert` if metrics regress.
- **SLO burn-rate watcher** — Google SRE multi-window burn-rate rules fire synthetic incidents *before* the crash.
- **Causal attribution** — Granger-causality + topological sort on the dependency DAG identifies which service caused a cascade.
- **Validating admission webhook** — blocks deploys that look like past incidents.

### Tier 2: the brain

- **Ed25519-signed audit trail** — every autonomous action emits a signed attestation with LLM provenance (model, prompt hash, tokens, cost) + SHA256 hash chain. EU AI Act / SOC 2 oriented.
- **Cost-aware LLM routing** — complexity classifier + cosine-similarity semantic cache. Target: ~70% cost reduction per incident.
- **Self-chaos mode** — scheduled chaos experiments against staging; Sentinel investigates its own injections and feeds sentinel-bench.

## Architecture

```
sentinel/
├── cmd/
│   ├── sentinel/              # Server binary (entrypoint)
│   ├── sentinelctl/           # CLI binary
│   └── sentinel-bench/        # Eval harness binary
├── internal/
│   ├── agent/                 # Agentic investigation loop (v3: MCTS, critic, condensation)
│   ├── agents/                # Classic pipeline + security scanner
│   ├── api/                   # HTTP API + SSE + embedded web UI
│   ├── attest/                # Ed25519-signed audit trail
│   ├── automerge/             # 11-gate auto-merger (restart-safe since 1.7)
│   ├── causal/                # Granger-causality RCA
│   ├── chaos/                 # Self-chaos mode
│   ├── chatops/               # 19 /sentinel commands
│   ├── detection/             # Live log streaming + pod status
│   ├── executors/             # Sandbox, Kaniko, GitHub PRs, K8s patches
│   ├── feedback/              # SQLite store + HNSW vector memory
│   ├── llm/                   # Multi-provider router + circuit breaker
│   └── ...                    # 38 packages total, all documented + tested
├── web/                       # Next.js frontend (12 sections)
├── chart/                     # Helm chart
├── sentinel-bench/            # Eval harness + scenarios
└── .github/workflows/         # CI (lint/test/scan) + release + kind e2e
```

**Numbers that matter:** 43MB image (distroless, nonroot, read-only rootfs) · 1 pod · ~50MB RAM at idle · <1s cold start · 9 direct deps · 20+ LLM providers.

## Configuration

### LLM providers

20+ providers natively via [GoAI](https://github.com/zendev-sh/goai) — Anthropic, OpenAI, Google, Azure, Bedrock, Vertex, OpenRouter, Groq, Mistral, xAI, DeepSeek, Ollama, vLLM, and more:

```bash
SENTINEL_LLM_PROVIDER=openrouter SENTINEL_LLM_API_KEY=sk-or-...
```

Stack multiple providers with priority-ordered failover and a circuit breaker (auth errors trip 1h, rate limits 60s, network errors 15s):

```bash
SENTINEL_LLM_PROVIDERS='[{"provider":"openrouter","api_key":"...","fast_model":"...","priority":1},{"provider":"groq","api_key":"...","fast_model":"...","priority":2}]'
```

### Key environment variables

| Variable | Default | Description |
|---|---|---|
| `SENTINEL_GITHUB_TOKEN` | (required) | GitHub PAT or GitHub App token |
| `SENTINEL_LLM_API_KEY` | (required) | LLM provider API key |
| `SENTINEL_LLM_PROVIDER` | `zai` | LLM provider |
| `SENTINEL_API_TOKEN` | (empty) | Admin API bearer token — **set in production** |
| `SENTINEL_SANDBOX_ENABLED` | `true` | Run sandbox verification before opening PR |
| `SENTINEL_SANDBOX_REQUIRED` | `false` | Block PR if sandbox fails |
| `SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED` | `true` | Fail closed if the sandbox NetworkPolicy can't be created |
| `SENTINEL_AUTO_MERGE_ENABLED` | `false` | Enable auto-merge after all 11 gates pass |
| `SENTINEL_AUTOMERGE_MIN_SANDBOX_LEVEL` | `3` | Minimum sandbox depth for auto-merge (3 or 5) |
| `SENTINEL_PRODUCTION_NAMESPACES` | `production` | CSV of production namespaces (governance) |
| `SENTINEL_RBAC_WRITE_NAMESPACES` | `sim` | CSV of namespaces Sentinel can write to |

Full list (~50 knobs): [chart/values.yaml](chart/values.yaml).

### Manual triggers

```bash
cat <<EOF | kubectl apply -f -
apiVersion: sentinel.io/v1alpha1
kind: SentinelIncident
metadata:
  name: manual-investigation
  namespace: sim
spec:
  service: my-app
  namespace: sim
  reason: Investigating elevated 5xx rate
  trigger: manual
  repo: myorg/my-app
EOF
```

## Auto-merge

Opt-in (global env + namespace allowlist + per-service annotation), then 11 gates: sandbox verified to the required level · confidence ≥ threshold · CI green · no cooldown · rate limit (3/h default) · namespace allowed · `sentinel.io/auto-merge: "true"` annotation · approved · no `hold` label · no `no-merge` label · branch protection passes. Schema migrations and rollbacks are never auto-merged. Tracked PRs survive Sentinel restarts.

## CLI

```bash
go install ./cmd/sentinelctl

sentinelctl incidents list [--status X] [--service Y] [--last 7d]
sentinelctl incidents get <id>
sentinelctl trigger <service> --namespace sim [--reason "..."]
sentinelctl pause [--duration 60m] [--reason "..."] / resume
sentinelctl stats · config view · config set <json> · metrics
sentinelctl app setup / status     # GitHub App manifest flow
```

Flags: `--server URL` (default `http://localhost:8000`), `--token T`, `--json`, `--version`.

## Web UI

Built-in dark dashboard at `http://sentinel.sentinel.svc:8000`:

- **Dashboard** — KPI cards, success-rate gauge, live activity feed
- **Incidents** — filterable table + detail drawer (hypotheses, root cause, fix, sandbox L1-L5, PR link)
- **Providers** — LLM pool with circuit-breaker states
- **Auto-merge** — queue, rate-limit gauge, cooldown, stats
- **Sandbox** — recent runs with expandable pod logs
- **Chatops** — all 19 `/sentinel` commands, fetched from the API
- **Settings** — every configurable knob

```bash
kubectl -n sentinel port-forward svc/sentinel 8000:8000
```

## Chatops

Sentinel responds to `/sentinel <command>` comments on its PRs. Read: `help`, `status`, `explain`, `trace`. Write: `retry-sandbox`, `alternatives`, `apply <A|B|C>`, `hold`, `approve`, `retest`, `verify`, `investigate`. Destructive (admin): `revert`, `rollback`, `pause`, `resume`, `override`. Every command ACKs with a reaction; state-changing commands take `cancel` to undo. Run `/sentinel help` on any Sentinel PR.

## sentinel-bench

Public eval harness with SHA256-pinned ground truths. Current scenarios: S001 crashloop-image-typo, S002 python-keyerror-env-var, S003 go-nil-pointer-panic. Metrics: DBR (Detection-to-Branch Rate), VPR (Verified-Pass Rate), TAR (Truth-Aligned Rate), FMR (Full-Merge Rate).

```bash
go install ./cmd/sentinel-bench
sentinel-bench list · validate · run
```

## Development

```bash
make build          # Build (includes web UI)
make build-no-web   # Build without web UI (faster)
make test           # Tests with race detector
make lint           # golangci-lint
make helm-lint      # Validate Helm chart
```

CI runs lint, race tests, helm lint/template, Trivy, govulncheck, gosec, and CodeQL on every PR, plus a weekly **kind-cluster e2e**: install the chart, deploy a deliberately-crashing service, assert the incident fires.

## Versioning & releases

The `VERSION` file is canonical; `scripts/sync-version.py` keeps Chart.yaml, values.yaml, README, and web/package.json in sync (CI fails on drift).

```bash
make release VERSION=1.8.0    # Bump + commit + tag + push (CI does the rest)
```

Releases are cosign-signed (keyless OIDC) with SBOM attached.

## Community

- **Bugs & features**: [GitHub Issues](https://github.com/karimzakzouk/sentinel/issues)
- **Security reports**: see [SECURITY.md](SECURITY.md) — reported privately
- **Contributing**: see [CONTRIBUTING.md](CONTRIBUTING.md)

## License

MIT. See [LICENSE](LICENSE).

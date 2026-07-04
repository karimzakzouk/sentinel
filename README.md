<div align="center">

# Sentinel

**The Autonomous AI SRE.**

Detect. Diagnose. Fix. Verify. — autonomously.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![Image Size](https://img.shields.io/badge/image-43MB-success)](https://hub.docker.com/r/karimzakzouk/sentinel)
[![Pods](https://img.shields.io/badge/pods-1-blue)]()
[![Arch](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success)]()
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Version](https://img.shields.io/badge/version-5.10.1-cyan)]()

[Install](#install) · [How It Works](#how-it-works) · [Configuration](#configuration) · [Architecture](#architecture) · [CLI](#cli) · [Web UI](#web-ui)

</div>

---

Sentinel is an autonomous AI SRE that watches your Kubernetes cluster for production crashes, diagnoses the root cause with an LLM, writes the fix, verifies it in a sandbox, and opens a Pull Request — all on its own.

**One binary. One pod. 43MB image. ~50MB RAM. Zero external dependencies.**

## Why Sentinel

Every SRE tool on the market either alerts you (PagerDuty, Datadog) or recommends fixes (Copilot, K8sGPT). None of them **fix** the problem. Sentinel does.

| What others do | What Sentinel does |
|---|---|
| Alert you at 3am | **Fix it at 3am** |
| Suggest a fix | **Write the fix + verify it + open a PR** |
| Need a human to investigate | **Investigate autonomously (clone repo, search code, LLM root-cause)** |
| Stop at "here's the error" | **Continue through sandbox verification + auto-merge** |

## Install

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 5.10.1 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

That's it. One pod. 43MB image. ~50MB RAM. Ready in <1 second.

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

Sentinel then automatically:
1. Live-streams the pod's logs
2. Watches the pod's status (CrashLoopBackOff, OOMKilled, ImagePullBackOff)
3. When an error is detected → runs the full pipeline autonomously

**No manual triggers needed.** Sentinel catches errors the instant they appear.

## How It Works

### The 6-Layer Pipeline

```
┌──────────────────────────────────────────────────────────────────┐
│                         Sentinel Pod                              │
│                                                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌───────────┐  ┌───────────┐ │
│  │  Detection  │→ │  Investigate│→ │  Govern   │→ │  Execute  │ │
│  │             │  │             │  │           │  │           │ │
│  │ Live logs   │  │ Clone repo  │  │ Embedded  │  │ Kaniko    │ │
│  │ Pod status  │  │ Search code │  │ safety    │  │ Sandbox   │ │
│  │ Alertmanager│  │ LLM agents  │  │ policy    │  │ GitHub PR │ │
│  └─────────────┘  └─────────────┘  └───────────┘  └───────────┘ │
│       │                                              │           │
│       ▼                                              ▼           │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │  SQLite (incidents + events + beta priors + vector memory)  │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

**Layer 1 — Detection:** Live log streaming + pod status watching + Alertmanager webhooks. Auto-triggers on any error.

**Layer 2 — Investigation (agentic, like SWE-agent):**
1. Clone the repo locally (go-git, ~2 seconds)
2. Walk the file tree (filtered: no node_modules, vendor, .git)
3. LLM Round 1: "Given these logs + this file tree, what should I search for?" → emits search queries
4. Run searches locally (Go regexp) — instant, no API calls
5. LLM Round 2: "Given these search results, what's the root cause + which files should I read?" → returns hypotheses + file paths
6. Read those files from disk
7. Fallback: if LLM returns 0 files, parse the stack trace from the logs and fetch those files

**Layer 3 — Evidence Gathering:** LLM-generated queries run against the K8s API (events, pods, deployments) in parallel with 15s per-query timeout.

**Layer 4 — Analysis:** Pure math — falsification + Bayes' rule + Beta-distribution priors. Picks the survivor hypothesis.

**Layer 5 — Governance:** 5 embedded Go rules (production namespace, high-risk service, blast radius, schema-migration rollback block, confidence threshold). No OPA sidecar.

**Layer 6 — Execution (5-level sandbox verification):**
1. Prepare branch (push fix files to a new Git branch)
2. Build canary image with Kaniko
3. Run sandbox verification (5 levels — see below)
4. Open PR with `sandbox-verified` label

### The 5-Level Sandbox

Sentinel doesn't just open a PR and hope. It verifies the fix in an isolated K8s namespace:

| Level | What it checks | Failure feedback to LLM |
|---|---|---|
| **L1: Pod Ready** | Canary pod becomes Ready AND stays Ready for 30s | "Startup crash not fixed — app still crashes on boot" |
| **L2: Health endpoints** | `/`, `/healthz`, `/readyz` return 2xx/3xx | "App starts but health endpoints return 4xx/5xx" |
| **L3: Error gone** | Original error (TypeError/panic/KeyError) absent from canary logs | "Original error still present — fix only masked the symptom" |
| **L4: Regression test** | LLM-generated test that reproduces the bug PASSES against the fix | "Fix passed L1-3 but regression test still FAILS — you masked the symptom, didn't fix the bug" |
| **L5: Reproducer** | LLM-generated reproducer script exits 0 | "Fix passed L1-4 but reproducer still crashes — strongest signal the fix is incomplete" |

When a level fails, Sentinel feeds the failure back to the LLM and retries (up to `SandboxMaxRetries` times). The LLM gets **level-specific feedback** — it knows exactly which level failed and why.

### The Agent Loop

Sentinel does what real coding agents (SWE-agent, OpenHands, Aider) do:

```
1. Clone the repo locally (go-git, ~2 seconds)
2. Walk the file tree (filtered: no node_modules, vendor, .git)
3. LLM Round 1: "what should I search for?"
   → emits 3-8 search queries: ["DB_PASSWORD", "KeyError", "def main"]
4. Run the searches locally (Go regexp)
   → instant, no API calls, no rate limits
5. LLM Round 2: "given these results, what's the root cause?"
   → returns hypotheses + files to read
6. Read those files from disk (zero API calls)
7. Pass source to Fix Proposer → writes the patch (SEARCH/REPLACE blocks)
8. Security scan (18 dangerous patterns) + reviewer-agent audit
9. Open PR with sandbox-verified label
```

Works for any repo, any language, any layout — because the LLM searches by content, not by filename.

## Configuration

### LLM Providers

Sentinel supports 20+ LLM providers natively via [GoAI](https://github.com/zendev-sh/goai):

| Provider | Env var |
|---|---|
| Anthropic, Bedrock, Azure, OpenAI, Google, Vertex | `SENTINEL_LLM_PROVIDER=anthropic\|bedrock\|azure\|openai\|google\|vertex` |
| OpenRouter, Groq, Mistral, Cohere, xAI, DeepSeek | `SENTINEL_LLM_PROVIDER=openrouter\|groq\|mistral\|cohere\|xAI\|deepseek` |
| Ollama, vLLM, LiteLLM, ZAI, custom | `SENTINEL_LLM_PROVIDER=ollama\|vllm\|litellm\|zai\|custom` |

**Multi-provider pool** — stack multiple API keys with priority-ordered failover + circuit breaker:

```bash
SENTINEL_LLM_PROVIDERS='[{"provider":"openrouter","api_key":"sk-or-...","fast_model":"nemotron-3-super-120b:free","priority":1},{"provider":"groq","api_key":"gsk_...","fast_model":"llama-3.1-70b","priority":2}]'
```

Auth errors trip the circuit breaker for 1h, rate limits for 60s, network errors for 15s.

### Alertmanager integration

Sentinel accepts Alertmanager webhooks at `POST /webhook/alertmanager`:

```yaml
# alertmanager.yml
receivers:
  - name: sentinel
    webhook_configs:
      - url: http://sentinel.sentinel.svc.cluster.local/webhook/alertmanager
        http_config:
          authorization:                     # shared-secret auth
            type: Bearer
            credentials: "$SENTINEL_ALERTMANAGER_WEBHOOK_SECRET"
route:
  receiver: sentinel
  group_by: [service, namespace]
  group_wait: 0s        # don't wait — Sentinel dedupes
```

Set `SENTINEL_ALERTMANAGER_WEBHOOK_SECRET` to require the secret in the `X-Sentinel-Webhook-Secret` header.

### Key environment variables

| Variable | Default | Description |
|---|---|---|
| `SENTINEL_GITHUB_TOKEN` | (required) | GitHub PAT or GitHub App token |
| `SENTINEL_LLM_API_KEY` | (required) | LLM provider API key |
| `SENTINEL_LLM_PROVIDER` | `openai` | LLM provider (see above) |
| `SENTINEL_LLM_FAST_MODEL` | provider default | Fast/cheap model for investigation |
| `SENTINEL_LLM_FRONTIER_MODEL` | provider default | Frontier model for fix proposal |
| `SENTINEL_API_TOKEN` | (empty) | Admin API bearer token — **set in production** |
| `SENTINEL_ALERTMANAGER_WEBHOOK_SECRET` | (empty) | Shared secret for Alertmanager webhook |
| `SENTINEL_SANDBOX_ENABLED` | `true` | Run sandbox verification before opening PR |
| `SENTINEL_SANDBOX_REQUIRED` | `false` | Block PR if sandbox fails (vs. open with needs-review) |
| `SENTINEL_SANDBOX_MAX_RETRIES` | `2` | Max LLM retries on sandbox failure |
| `SENTINEL_AUTO_MERGE_ENABLED` | `false` | Enable auto-merge after 11 gates pass |
| `SENTINEL_PRODUCTION_NAMESPACES` | `production` | CSV of production namespaces (governance) |
| `SENTINEL_RBAC_WRITE_NAMESPACES` | `sim` | CSV of namespaces Sentinel can write to |
| `SENTINEL_PROMETHEUS_URL` | (empty) | Prometheus URL for evidence gathering |

See [chart/values.yaml](chart/values.yaml) for the full list (~50 knobs).

### Manual Triggers

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

## Architecture

```
sentinel/
├── cmd/
│   ├── sentinel/              # Server binary (entrypoint)
│   └── sentinelctl/           # CLI binary
├── internal/
│   ├── alertmanager/          # Alertmanager webhook receiver
│   ├── source/                # Source interface (extensible alert sources)
│   ├── sandbox/               # Backend interface (extensible sandbox backends)
│   ├── agents/                # 4-agent pipeline + security scanner
│   ├── api/                   # HTTP API + SSE + web UI
│   ├── automerge/             # 11-gate auto-merger
│   ├── chatops/               # 19 /sentinel commands
│   ├── config/                # Env var loading
│   ├── correlation/           # Cross-service cascade detection
│   ├── depanalysis/           # Shared library dependency analysis
│   ├── detection/             # Live log streaming + pod status
│   ├── events/                # SSE broadcaster
│   ├── executors/             # GitHub PRs, K8s patches, sandbox, Kaniko
│   ├── feedback/              # SQLite store + HNSW vector memory
│   ├── github/                # GitHub App authentication
│   ├── k8s/                   # K8s client
│   ├── llm/                   # Multi-provider router + circuit breaker
│   ├── logging/               # Structured logging (slog)
│   ├── metrics/               # Prometheus metrics + cost tracking
│   ├── prreview/              # Predictive PR review
│   ├── repo/                  # go-git cloner + local search
│   ├── runtimecfg/            # Hot-reloadable runtime config
│   ├── safety/                # 5 embedded policy rules
│   ├── schemas/               # All shared types
│   ├── slackbot/              # Interactive Slack bot
│   └── watcher/               # CRD watcher
├── web/                       # Next.js frontend (12 sections)
├── chart/                     # Helm chart
├── examples/                  # 60-second kind walkthrough
└── scripts/                   # sync-version.py
```

## Numbers

| Metric | Sentinel | Typical AIOps tool |
|---|---|---|
| Image size | **43MB** | 1–5GB |
| Pods | **1** | 5–20 |
| RAM at idle | **~50MB** | 2–8GB |
| Disk | **~5GB** | 50–200GB |
| Cold start | **<1s** | 30–120s |
| Direct deps | **9** | 50–200 |
| LLM providers | **20+** | 1–3 |

## CLI

```bash
# Install
go install ./cmd/sentinelctl

# Usage
sentinelctl incidents list [--status X] [--service Y] [--last 7d]
sentinelctl incidents get <id>
sentinelctl trigger <service> --namespace sim
sentinelctl pause [--duration 60m]
sentinelctl resume
sentinelctl stats
sentinelctl config view
sentinelctl config set <json-file-or-inline>
sentinelctl metrics
sentinelctl app setup          # GitHub App manifest flow
sentinelctl app status
```

Flags: `--server URL` (default: http://localhost:8000), `--token T` (admin API token), `--json` (raw JSON output).

## Web UI

Sentinel ships with a built-in dark-themed dashboard at `http://sentinel.sentinel.svc:8000`:

- **Dashboard** — KPI cards, success rate gauge, live activity feed
- **Incidents** — filterable table + detail drawer (hypotheses, root cause, fix, sandbox L1-L5, PR link)
- **Providers** — LLM pool with circuit-breaker state indicators
- **Auto-merge** — queue, rate limit gauge, cooldown, stats
- **Sandbox** — recent runs with expandable pod logs
- **Metrics** — CSS-only charts (no chart library, keeps bundle small)
- **Chatops** — all 19 `/sentinel` commands with permissions + search
- **Settings** — 11 cards covering every configurable knob (LLM, timeouts, confidence, sandbox, auto-merge, K8s, observability, GitHub App, Slack, Kaniko)

```bash
kubectl -n sentinel port-forward svc/sentinel 8000:8000
# Open http://localhost:8000
```

## Chatops (19 `/sentinel` commands)

Sentinel responds to `/sentinel <command>` comments on its PRs:

| Permission | Commands |
|---|---|
| **Read** | `help`, `status`, `explain`, `trace` |
| **Write** | `retry-sandbox`, `alternatives`, `apply <A\|B\|C>`, `hold`, `approve`, `no-merge`, `merge`, `investigate`, `retest`, `verify` |
| **Destructive** | `revert`, `rollback`, `pause`, `resume`, `override` |

Every command ACKs with a 👍 reaction. State-changing commands take `cancel` to undo. Run `/sentinel help` on any Sentinel PR to see the full list.

## Auto-merge

Sentinel can auto-merge PRs that pass 11 gates:

1. Sandbox verified (L1-L5)
2. Confidence ≥ threshold
3. CI green (if `require_ci=true`)
4. Not in cooldown (1h after regression)
5. Rate limit not exceeded (3/hour default)
6. Namespace allowed
7. PR has `sentinel.io/auto-merge: "true"` annotation
8. PR approved
9. No `hold` label
10. No `no-merge` label
11. Branch protection checks pass

Three opt-in layers: global (`SENTINEL_AUTO_MERGE_ENABLED=true`) + namespace (`autoMerge.namespaces`) + per-service annotation.

## Development

```bash
git clone https://github.com/karimzakzouk/sentinel.git
cd sentinel

make build          # Build (includes web UI)
make build-no-web   # Build without web UI (faster)
make test           # Run tests with race detector
make lint           # Run golangci-lint
make run            # Run locally (needs kubeconfig)
make helm-lint      # Validate Helm chart
```

## Versioning & Releases

The `VERSION` file is the single source of truth. `scripts/sync-version.py` keeps every other file in sync (Chart.yaml, values.yaml, README, web/package.json).

```bash
make release VERSION=4.7.0    # Bump + commit + tag + push (CI does the rest)
make bump-version VERSION=4.7.0  # Sync files only (no commit)
make check-version               # CI mode: fail if drift detected
```

CI runs `make check-version` on every PR. See [Versioning & Releases](#versioning--releases) above for the full flow.

## Community

- **Bug reports & feature requests**: [GitHub Issues](https://github.com/karimzakzouk/sentinel/issues) — use the templates
- **Questions & discussion**: [GitHub Discussions](https://github.com/karimzakzouk/sentinel/discussions)
- **Security vulnerabilities**: See [SECURITY.md](SECURITY.md) — report privately
- **Contributing**: See [CONTRIBUTING.md](CONTRIBUTING.md)
- **Code of Conduct**: See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## Examples

See [`examples/python-crashloop/`](examples/python-crashloop/) for a complete 60-second walkthrough: install Sentinel → deploy a broken Flask app → watch Sentinel auto-fix it → merge the PR.

## License

MIT. See [LICENSE](LICENSE).

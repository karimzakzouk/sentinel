<div align="center">

# Sentinel

**The Autonomous AI SRE.**

Detect. Diagnose. Fix. Verify. — autonomously.

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![Image Size](https://img.shields.io/badge/image-~28MB-success)](https://hub.docker.com/r/karimzakzouk/sentinel-v3)
[![Pods](https://img.shields.io/badge/pods-1-blue)]()
[![Arch](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success)]()
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

[Install](#install) · [How It Works](#how-it-works) · [Architecture](#architecture) · [Configuration](#configuration) · [CLI](#cli) · [Web UI](#web-ui)

</div>

---

Sentinel is an autonomous AI Site Reliability Engineer that plugs into any Kubernetes cluster, watches for incidents in real-time, diagnoses them with an LLM, writes fixes, tests them in a sandbox, and opens Pull Requests — all on its own.

**One binary. One pod. ~28MB image. ~50MB RAM. Zero external dependencies.**

## Why Sentinel

Every SRE tool on the market either alerts you (PagerDuty, Datadog) or recommends fixes (Copilot, K8sGPT). None of them **fix** the problem. Sentinel does:

- **Detects** incidents the instant they happen — live log streaming, not polling
- **Diagnoses** the root cause with a 4-agent LLM pipeline that clones your repo and searches the source code
- **Writes** the actual fix — file changes, not recommendations
- **Verifies** the fix in a sandbox namespace with 3-level testing (pod ready → endpoints respond → original error gone)
- **Opens** a Pull Request with a `sandbox-verified` label — ready for you to merge

All in ~4 minutes. While you sleep. No human intervention required.

## Install

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 3.9.8 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

That's it. One pod. ~28MB image. ~50MB RAM. Ready in <1 second.

```bash
kubectl -n sentinel get pods
# sentinel-xxx   1/1 Running   0   1s
```

## Enroll your services

Annotate your Deployments so Sentinel knows what to watch and where to find the source code:

```yaml
metadata:
  annotations:
    sentinel.io/repo: myorg/my-app           # GitHub repo (required)
    sentinel.io/branch: main                  # Branch that's deployed (required for accurate diagnosis)
    sentinel.io/git-commit: abc123...         # Exact commit SHA (optional, most precise)
```

Once annotated, Sentinel automatically:
1. Starts live-streaming the pod's logs
2. Watches the pod's status (CrashLoopBackOff, OOMKilled, ImagePullBackOff)
3. When an error is detected → runs the full pipeline autonomously

**No manual triggers needed.** Sentinel catches errors the instant they appear in the logs.

## How It Works

### Autonomous Detection (live, zero-delay)

Sentinel runs two live detectors continuously:

| Detector | What it catches | How |
|----------|----------------|-----|
| **Log Stream Manager** | Runtime errors (ZeroDivisionError, panic, NullPointerException, etc.) | Opens a persistent `follow=true` log stream per pod, scans every line in real-time |
| **Pod Status Watcher** | CrashLoopBackOff, OOMKilled, ImagePullBackOff | Watches pod status changes via K8s Watch stream |

Both are **live** — not polling. The moment an error appears in a pod's logs, Sentinel sees it. Zero delay.

**Opt-in:** Sentinel only watches pods with the `sentinel.io/repo` annotation. System pods are ignored. No noise.

**Deduplication:** Each error is fingerprinted (service + error type + file). The same error won't trigger multiple incidents within a 5-minute cooldown window.

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
│  │             │  │ LLM agents  │  │ policy    │  │ GitHub PR │ │
│  └─────────────┘  └─────────────┘  └───────────┘  └───────────┘ │
│       │                                              │           │
│       ▼                                              ▼           │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │  SQLite (incidents + events + beta priors + vector memory)  │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │  HTTP API (healthz, readyz, incidents, settings)            │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

**Layer 1 — Detection:** Live log streaming + pod status watching. Auto-triggers on any error.

**Layer 2 — Investigation (agentic, like SWE-agent):**
1. Clone the repo locally (go-git, pure Go, ~2 seconds)
2. Walk the file tree (filtered: no node_modules, vendor, .git)
3. LLM Round 1: "Given these logs + this file tree, what should I search for?" → emits search queries
4. Run searches locally (Go regexp) — instant, no API calls
5. LLM Round 2: "Given these search results, what's the root cause + which files should I read?" → returns hypotheses + file paths
6. Read those files from disk
7. Fallback: if LLM returns 0 files, parse the stack trace from the logs and fetch those files

**Layer 3 — Evidence Gathering:** LLM-generated queries run against the K8s API (events, pods, deployments) in parallel with 15s per-query timeout.

**Layer 4 — Analysis:** Pure math — falsification + Bayes' rule + Beta-distribution priors. Picks the survivor hypothesis.

**Layer 5 — Governance:** 5 embedded Go rules (production namespace, high-risk service, blast radius, schema-migration rollback block, confidence threshold). No OPA sidecar.

**Layer 6 — Execution (4-phase code fix):**
1. Prepare branch (push fix files to a new Git branch)
2. Build canary image with Kaniko
3. Run sandbox verification (3 levels: pod ready → endpoints respond → original error gone)
4. Open PR with `sandbox-verified` label

### The Agent Loop

Sentinel doesn't guess which files to look at. It does what real coding agents (SWE-agent, OpenHands, Aider) do:

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
7. Pass source to Fix Proposer → writes the patch
8. Open PR with sandbox-verified label
```

Works for any repo, any language, any layout — because the LLM searches by content, not by filename.

## Architecture

```
sentinel/
├── cmd/sentinel/              # Entrypoint
├── internal/
│   ├── schemas/               # All types (Go structs)
│   ├── config/                # Env var loading + LLM provider resolution
│   ├── llm/                   # OpenAI-compatible client (net/http, no SDK, retry/backoff)
│   ├── k8s/                   # Workload annotation lookup, pod logs, CR status
│   ├── watcher/               # CRD watcher (manual trigger via SentinelIncident CR)
│   ├── detection/             # Autonomous detection (live log streaming + pod status)
│   ├── repo/                  # go-git cloner + local search engine
│   ├── agents/                # 4-agent pipeline + state machine + error parser
│   ├── safety/                # Embedded policy (5 if-statements, replaces OPA)
│   ├── executors/             # GitHub PRs, K8s patches, sandbox, Kaniko
│   ├── feedback/              # SQLite store + HNSW vector memory + outcome watcher
│   ├── correlation/           # v3.5: Cross-service cascade detection
│   ├── depanalysis/           # v3.5: Shared library dependency analysis
│   ├── prreview/              # v3.5: Predictive PR review (GitHub webhook)
│   ├── slackbot/              # v3.5: Interactive Slack bot (two-way commands)
│   ├── logging/               # v3.4: Structured logging (slog + JSON + incident_id)
│   ├── metrics/               # v3.4: Prometheus metrics (counters/histograms/gauges)
│   └── api/                   # HTTP API + webhook receivers
├── chart/                     # Single-pod Helm chart
├── Dockerfile                 # Distroless static, ~28MB
├── go.mod
└── README.md
```

## Numbers

| Metric | Sentinel v3 | Typical AIOps tool |
|--------|-------------|-------------------|
| Image size | **~28MB** | 1–5GB |
| Pods | **1** | 5–20 |
| RAM at idle | **~50MB** | 2–8GB |
| Disk | **~5GB** | 50–200GB |
| Cold start | **<1s** | 30–120s |
| Dependencies | **6 Go modules** | 50+ packages |
| External services | **0** (SQLite embedded) | Postgres, Redis, Kafka, ES |
| Detection latency | **~0s** (live streaming) | 30–60s (polling) |
| Manual intervention | **0** (fully autonomous) | Required (acknowledge, triage, assign) |

## Configuration

### LLM Providers

Sentinel uses the OpenAI-compatible API. Any provider works:

| Provider | `llm.provider` | Notes |
|----------|----------------|-------|
| OpenAI | `openai` | GPT-4o, o1, etc. |
| Google Gemini | `gemini` | Gemini 2.0 Flash / Pro |
| z.ai GLM | `zai` | GLM-4.5, GLM-4-Flash |
| OpenRouter | `custom` | Set `llm.baseUrl=https://openrouter.ai/api/v1` |
| Ollama (local) | `ollama` | Run LLMs locally — no API key needed |
| vLLM | `vllm` | Self-hosted inference |
| LiteLLM proxy | `litellm` | Proxy for Anthropic, Bedrock, etc. |
| Any OpenAI-compatible | `custom` | Set `llm.baseUrl` to your endpoint |

### Workload Annotations (REQUIRED for autonomous detection)

```yaml
metadata:
  annotations:
    sentinel.io/repo: myorg/my-app           # GitHub repo (required)
    sentinel.io/branch: main                  # Branch that's deployed (required for accurate diagnosis)
    sentinel.io/git-commit: abc123...         # Exact commit SHA (optional, most precise)
    sentinel.io/runbook: https://wiki/...     # Runbook URL (optional)
    sentinel.io/team: platform                # Owning team (optional)
    sentinel.io/ignore: "true"                # Opt out of Sentinel (optional)
```

**Priority:** `sentinel.io/git-commit` (exact commit) > `sentinel.io/branch` (branch tip) > `main` (fallback with warning).

### RBAC

Sentinel needs cluster-wide read on pods/deployments/events to investigate incidents. By default it can't write anywhere — set `sentinel.rbac.writeNamespaces` to allow fixes:

```bash
--set 'sentinel.rbac.writeNamespaces={sim,staging}'   # can fix in sim + staging
--set 'sentinel.rbac.writeNamespaces={prod}'          # can fix in prod
```

### Manual Triggers

Sentinel auto-detects incidents, but you can also trigger manually via CR:

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

## Development

```bash
git clone https://github.com/karimzakzouk/sentinel.git
cd sentinel

# Build
go build -ldflags='-s -w -X main.Version=dev' -trimpath -o sentinel ./cmd/sentinel

# Test
go test ./... -race

# Run locally (needs kubeconfig)
./sentinel --addr :8000 --db /tmp/sentinel.db

# Helm lint
helm lint chart/
```

## What's New in v3.8.2

- **Professional Web UI** — Dark-themed dashboard embedded in the binary. Sidebar navigation, real-time incident feed, pipeline trace visualization, hypothesis confidence bars, code diff view, live log streaming, memory/RAG viewer, editable settings page. One binary, one pod, UI included.
- **CLI (`sentinelctl`)** — Zero-dependency binary for incidents list/get, trigger investigation, pause/resume, stats, config view, metrics. Table output by default, `--json` for scripts.
- **GitHub App** — Verified bot identity, auto-webhook registration, installation token management. One-click install from GitHub Marketplace.
- **Settings persistence** — Edit any config from the web UI. Changes persist to `/data/runtime-config.json`. No restart needed.
- **SSE real-time updates** — Pipeline trace animates live as each step completes. Status indicator shows LIVE when connected.
- **24-hour feedback loop** — Two-phase outcome watcher: Phase 1 (10min recovery) + Phase 2 (24h re-occurrence). Learns from real production outcomes.
- **Predictive PR review** — Scores PR diffs against incident memory. Posts warnings on changes that resemble past incidents.
- **Interactive Slack bot** — Two-way commands: pause, resume, investigate, list incidents, status.
- **Cross-service correlation** — Detects cascading failures (3+ services in 60s), triggers automatic rollback.
- **Shared library support** — Redirects investigation to library repo when error is in vendor/node_modules.
- **Concurrency limiter** — Max 3 concurrent investigations to prevent LLM quota exhaustion.
- **Arm64 support** — Multi-arch Docker images (amd64 + arm64).
- **Production hardening** — LLM retry/backoff, structured logging (slog), Prometheus metrics, golangci-lint CI, govulncheck CVE scanning, configurable timeouts, Makefile, goreleaser config.

## CLI

```bash
# Build
make build-cli

# Or install
make install-cli

# Usage
sentinelctl incidents list
sentinelctl incidents get <id>
sentinelctl trigger demo-app --namespace sim
sentinelctl pause --duration 60
sentinelctl resume
sentinelctl stats
sentinelctl config view
sentinelctl metrics

# JSON output for scripts
sentinelctl incidents list --json
```

## Web UI

The dashboard is embedded in the Go binary via `go:embed`. No separate deployment.

```bash
# Port-forward to access
kubectl -n sentinel port-forward deployment/sentinel 8000:8000

# Open http://localhost:8000
```

Pages:
- **Dashboard** — Metric cards, recent incidents, system status, resolution rate donut chart
- **Incidents** — Full table with search, click for full-page detail view (pipeline trace, root cause, hypotheses, fix diff, execution, agent trace)
- **Live Logs** — Terminal-style log viewer with filtering
- **Memory** — HNSW vector store viewer showing learned incidents + Beta priors
- **Settings** — Editable config for everything (LLM, pipeline, GitHub, Slack, observability, RBAC)

To rebuild the UI:
```bash
make web    # Build Next.js static export + copy to embed directory
make build  # Compile Go binary with embedded UI
```

## What's Next

- [ ] Real embedding model (sentence-transformers via ONNX) for semantic RAG
- [ ] HTTP prober (detect 5xx errors that don't show in logs)
- [ ] Loki + Jaeger evidence backends (Prometheus is wired; Loki/Jaeger are stubs)
- [ ] Integration tests with mock K8s API
- [ ] Homebrew tap for `brew install sentinel`

## License

MIT — see [LICENSE](LICENSE)

<div align="center">

# Sentinel

**The Autonomous AI SRE.**

Detect. Diagnose. Fix. Verify. — autonomously.

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![Image Size](https://img.shields.io/badge/image-43MB-success)](https://hub.docker.com/r/karimzakzouk/sentinel)
[![Pods](https://img.shields.io/badge/pods-1-blue)]()
[![Arch](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success)]()
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

[Install](#install) · [How It Works](#how-it-works) · [Architecture](#architecture) · [Configuration](#configuration) · [CLI](#cli) · [Web UI](#web-ui)

</div>

---

Sentinel is an autonomous AI Site Reliability Engineer that plugs into any Kubernetes cluster, watches for incidents in real-time, diagnoses them with an LLM, writes fixes, tests them in a sandbox, and opens Pull Requests — all on its own.

**One binary. One pod. 43MB image. ~50MB RAM. Zero external dependencies.**

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

helm install sentinel sentinel/sentinel --version 4.6.0 \
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
├── Dockerfile                 # Distroless static, 43MB
├── go.mod
└── README.md
```

## Numbers

| Metric | Sentinel v3 | Typical AIOps tool |
|--------|-------------|-------------------|
| Image size | **43MB** | 1–5GB |
| Pods | **1** | 5–20 |
| RAM at idle | **~50MB** | 2–8GB |
| Disk | **~5GB** | 50–200GB |
| Cold start | **<1s** | 30–120s |
| Dependencies | **7 direct Go dependencies** | 50+ packages |
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
| OpenRouter | `openrouter` | Native GoAI provider, no baseUrl needed |=https://openrouter.ai/api/v1` |
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

### Advanced Configuration (v3.11.0)

All v3.11.0 features are configurable via Helm values or env vars. Defaults preserve backwards compatibility.

#### GitHub Enterprise / GitLab / Bitbucket

Sentinel no longer hardcodes `github.com`. Set these to use a self-hosted GitHub Enterprise, GitLab, or any other git host:

```bash
--set sentinel.gitHost=gh.mycorp.internal \
--set sentinel.githubApiUrl=https://gh.mycorp.internal/api/v3
```

| Helm value | Env var | Default | Purpose |
|------------|---------|---------|---------|
| `sentinel.gitHost` | `SENTINEL_GIT_HOST` | `github.com` | Git host for clone URLs |
| `sentinel.githubApiUrl` | `SENTINEL_GITHUB_API_URL` | `https://api.github.com` | Base API URL for PR/label operations |
| `sentinel.defaultBranch` | `SENTINEL_DEFAULT_BRANCH` | `main` | Default branch fallback (override for repos with `master`/`develop`/`trunk`) |

Per-repo override via the `sentinel.io/branch` annotation takes precedence over `defaultBranch`.

#### Registry-aware Kaniko (ECR / GCR / ACR / Harbor / Quay)

The sandbox canary image no longer force-pushes to `docker.io`. The canary's registry is derived from the prod image's registry (so prod pull secrets work). Set `kanikoRegistry` to control the `--cache-repo` + the `dockerconfigjson` Secret auth URL:

```bash
--set sentinel.kanikoRegistry=123456789.dkr.ecr.us-east-1.amazonaws.com \
--set sentinel.dockerHubUsername=myorg \
--set sentinel.dockerHubToken=...
```

| Helm value | Env var | Default | Purpose |
|------------|---------|---------|---------|
| `sentinel.kanikoRegistry` | `SENTINEL_KANIKO_REGISTRY` | `docker.io` | Registry for Kaniko `--cache-repo` + dockerconfigjson auth URL |
| `sentinel.kanikoInitImage` | `SENTINEL_KANIKO_INIT_IMAGE` | `alpine/git:2.43.0` | Kaniko init container image (clone-and-patch) |
| `sentinel.kanikoExecutorImage` | `SENTINEL_KANIKO_EXECUTOR_IMAGE` | `gcr.io/kaniko-project/executor:v1.24.0` | Kaniko executor image |

#### Safety Policy

The demo's `payments` namespace and `payment-api`/`billing-api`/`auth-service` high-risk services no longer leak into every install. Set your own:

```bash
--set sentinel.productionNamespaces=prod,production,staging \
--set sentinel.highRiskServices=payment-api,billing-api,auth-service
```

| Helm value | Env var | Default | Purpose |
|------------|---------|---------|---------|
| `sentinel.productionNamespaces` | `SENTINEL_PRODUCTION_NAMESPACES` | `prod,production` | Namespaces treated as production (stricter governance) |
| `sentinel.highRiskServices` | `SENTINEL_HIGH_RISK_SERVICES` | (empty) | Services that always require human approval in prod |

#### Detection

The namespace skip list is now configurable (was hardcoded to `kube-system`/`kube-public`/`kube-node-lease`). Default expanded to 23 common system namespaces (`calico-system`, `cert-manager`, `ingress-nginx`, `monitoring`, etc.).

```bash
--set sentinel.skipNamespaces=kube-system,kube-public,kube-node-lease,my-custom-system-ns
```

| Helm value | Env var | Default | Purpose |
|------------|---------|---------|---------|
| `sentinel.skipNamespaces` | `SENTINEL_SKIP_NAMESPACES` | (23 common system namespaces) | Namespaces Sentinel will not watch |
| `sentinel.maxConcurrentInvestigations` | `SENTINEL_MAX_CONCURRENT_INVESTIGATIONS` | `3` | Max concurrent investigations (prevents LLM quota exhaustion) |

#### LLM Embeddings

The default `embeddingModel` is now empty (was `text-embedding-3-small`). When empty, Sentinel uses a hash fallback for the HNSW vector store — honest about what's happening instead of silently failing against a non-OpenAI provider. Set both `embeddingModel` AND `embeddingDim` to use real embeddings:

```bash
# OpenAI text-embedding-3-small (dim 1536)
--set sentinel.llm.embeddingModel=text-embedding-3-small \
--set sentinel.llm.embeddingDim=1536

# OpenAI text-embedding-3-large (dim 3072)
--set sentinel.llm.embeddingModel=text-embedding-3-large \
--set sentinel.llm.embeddingDim=3072
```

| Helm value | Env var | Default | Purpose |
|------------|---------|---------|---------|
| `sentinel.llm.embeddingModel` | `SENTINEL_LLM_EMBEDDING_MODEL` | (empty) | Embedding model name. Empty = hash fallback. |
| `sentinel.llm.embeddingDim` | `SENTINEL_LLM_EMBEDDING_DIM` | `1536` | Embedding dimension (must match the real model's dim) |

#### LLM Provider Support (v3.12.0+)

Sentinel uses [GoAI](https://github.com/zendev-sh/goai) for 25+ native LLM providers. No proxy needed — each provider speaks its own native API:

| Provider | `llm.provider` | Auth | Notes |
|----------|----------------|------|-------|
| OpenAI | `openai` | Bearer | GPT-4o, o1, etc. |
| Anthropic | `anthropic` | x-api-key | Claude (native /v1/messages) |
| AWS Bedrock | `bedrock` | SigV4 | Claude/Llama on AWS |
| Azure OpenAI | `azure` | api-key | Per-deployment URLs |
| Google Gemini | `google` | API key | Native Gemini API |
| Google Vertex | `vertex` | API key | Vertex AI |
| OpenRouter | `openrouter` | Bearer | Access 100+ models |
| z.ai GLM | `zai` | Bearer | Free tier available |
| Groq | `groq` | Bearer | Fast inference |
| Mistral | `mistral` | Bearer | |
| Cohere | `cohere` | Bearer | |
| xAI | `xai` | Bearer | Grok |
| DeepSeek | `deepseek` | Bearer | |
| Ollama | `ollama` | None | Local LLMs |
| vLLM | `vllm` | Bearer | Self-hosted |
| Any OpenAI-compatible | `custom` | Bearer | Set `llm.baseUrl` |

### Additional Configuration (v3.13.0)

These env vars are supported but not commonly needed. All have sensible defaults.

| Env var | Default | Purpose |
|---------|---------|---------|
| `SENTINEL_SANDBOX_NAMESPACE_PREFIX` | `sentinel-sandbox` | Prefix for sandbox namespaces |
| `SENTINEL_ENABLE_TRAFFIC_MIRRORING` | `true` | Enable traffic mirroring in sandbox |
| `SENTINEL_RELEASE_NAME` | `sentinel` | Helm release name |
| `SENTINEL_INVESTIGATION_TIMEOUT` | `600` (10min) | Max investigation duration (seconds) |
| `SENTINEL_PANIC_CPU_THRESHOLD_PCT` | `95.0` | CPU % that triggers panic mode |
| `SENTINEL_PANIC_CONNECTIONS_THRESHOLD` | `9000` | Connection count that triggers panic mode |
| `SENTINEL_INVESTIGATOR_TIMEOUT` | `120` | Investigator agent timeout (seconds) |
| `SENTINEL_EVIDENCE_GATHERER_TIMEOUT` | `90` | Evidence gatherer timeout (seconds) |
| `SENTINEL_ANALYST_TIMEOUT` | `15` | Analyst agent timeout (seconds) |
| `SENTINEL_FIX_PROPOSER_TIMEOUT` | `120` | Fix proposer timeout (seconds) |
| `SENTINEL_CONFIDENCE_LOOP_BACK` | `0.3` | Confidence threshold to loop back to investigator |
| `SENTINEL_MAX_PIPELINE_ITERATIONS` | `5` | Max pipeline rewind iterations |
| `SENTINEL_OUTCOME_WATCH_DURATION_MIN` | `10` | Phase 1 outcome watch (minutes) |
| `SENTINEL_OUTCOME_CHECK_INTERVAL_SEC` | `30` | Phase 1 poll interval (seconds) |
| `SENTINEL_DETECTION_COOLDOWN_SEC` | `300` (5min) | Detection cooldown (seconds) |
| `SENTINEL_DETECTION_TAIL_LINES` | `100` | Pod log tail lines to stream |
| `SENTINEL_SCALE_FIX_REPLICAS` | `2` | Default replicas for scale fixes |
| `SENTINEL_LLM_PRIOR_WEIGHT` | `0.4` | LLM prior weight in Bayesian blend |
| `SENTINEL_SKIP_SANDBOX_NON_PROD` | `false` | Skip sandbox for non-prod namespaces |
| `SENTINEL_LLM_MAX_RETRIES` | `3` | LLM retry count |
| `SENTINEL_LLM_RETRY_BASE_DELAY_MS` | `500` | LLM retry base delay (ms) |
| `SENTINEL_LOG_LEVEL` | `INFO` | Log level (DEBUG/INFO/WARN/ERROR) |
| `AWS_REGION` | `us-east-1` | AWS region (Bedrock provider only) |

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

### Alertmanager integration

Sentinel accepts Alertmanager webhooks at `POST /webhook/alertmanager`. Configure Alertmanager to send firing alerts to Sentinel:

```yaml
# alertmanager.yml
receivers:
  - name: sentinel
    webhook_configs:
      - url: http://sentinel.sentinel.svc.cluster.local/webhook/alertmanager
        send_resolved: false
route:
  receiver: sentinel
  group_by: [service, namespace]
  group_wait: 0s        # don't wait — Sentinel dedupes
  group_interval: 5m
  repeat_interval: 1h
```

Sentinel extracts the `service`, `namespace`, `alertname`, and `summary` from each alert and feeds it to the same investigation pipeline as log-detected incidents. See [`internal/alertmanager/receiver.go`](internal/alertmanager/receiver.go) for the payload format.

### Examples

See [`examples/python-crashloop/`](examples/python-crashloop/) for a complete 60-second walkthrough: install Sentinel → deploy a broken Flask app → watch Sentinel auto-fix it → merge the PR.

### Community

- **Bug reports & feature requests**: [GitHub Issues](https://github.com/karimzakzouk/sentinel/issues) — use the templates
- **Questions & discussion**: [GitHub Discussions](https://github.com/karimzakzouk/sentinel/discussions)
- **Security vulnerabilities**: See [SECURITY.md](SECURITY.md) — report privately
- **Contributing**: See [CONTRIBUTING.md](CONTRIBUTING.md)
- **Code of Conduct**: See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

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

## Versioning & Releases

Sentinel uses a **single source of truth** versioning system. The `VERSION` file at the repo root is canonical — every other file that needs the version is auto-synced from it. There is no manual editing of `Chart.yaml`, `values.yaml`, `README.md`, or `web/package.json` when bumping the version.

### Files synced from `VERSION`

| File | Field |
|------|-------|
| `VERSION` | canonical (e.g. `4.4.0`) |
| `chart/Chart.yaml` | `version:` + `appVersion:` |
| `chart/values.yaml` | `image.tag:` + comment header |
| `my-values.yaml` | `image.tag:` |
| `web/package.json` | `"version":` |
| `README.md` | `helm install ... --version X.Y.Z` |
| `internal/version/version.go` | injected at build time via `-ldflags` (source always says `"dev"`) |

The sync logic lives in [`scripts/sync-version.py`](scripts/sync-version.py). CI runs `make check-version` on every PR and fails the build if any file drifts from `VERSION`.

### Releasing a new version

```bash
# 1. Make sure main is clean and up to date
git checkout main && git pull

# 2. Run the release target (bump + commit + tag + push)
make release VERSION=4.5.0
```

That's it. `make release` does:
1. Rewrites every synced file to `4.5.0` via `scripts/sync-version.py --bump 4.5.0`
2. `git commit -m "release: v4.5.0"`
3. `git tag v4.5.0`
4. `git push origin main v4.5.0`

The tag push triggers the **Release workflow** (`.github/workflows/release.yml`), which automatically:
- Runs `go test -race` + `helm lint`
- Builds multi-arch Docker images (`linux/amd64` + `linux/arm64`) without QEMU
- Pushes to Docker Hub (`karimzakzouk/sentinel:4.5.0` + `:latest`) and GHCR
- Packages the Helm chart (`sentinel-4.5.0.tgz`)
- Creates the GitHub Release with auto-generated notes
- Publishes the chart to the `gh-pages` branch (Helm repo at `https://karimzakzouk.github.io/sentinel/`)

Total time: ~4 minutes from tag push to live Helm repo.

### Other version commands

```bash
make bump-version VERSION=4.5.0   # sync files only (no commit, no tag) — useful for testing
make check-version                 # CI mode: exit 1 if any file drifted from VERSION
```

### Guard rails

- **CI version-sync check** — every PR runs `make check-version`. Drift = build fail. You cannot merge a PR with a desynced version.
- **Tag-vs-VERSION match** — the Release workflow aborts if the pushed git tag (e.g. `v4.5.0`) does not equal `cat VERSION`. You cannot accidentally tag the wrong version.
- **Universal tag pattern** — the Release workflow fires on `v*.*.*`, so it works for any future major version (v5, v6, …). No `v3`/`v4`/`v5`-specific patterns anywhere in the repo.

### For AI agents / automation

If you are an automated agent tasked with cutting a release, the entire flow is:

```bash
# 1. Ensure working tree is clean
git status --porcelain  # must be empty

# 2. Pull latest main
git checkout main && git pull --ff-only

# 3. Run the release
make release VERSION=X.Y.Z

# 4. Monitor CI
#    https://github.com/karimzakzouk/sentinel/actions/workflows/release.yml
#    Wait for the "Release" workflow to complete. The Release job's last step
#    publishes the Helm chart to gh-pages — once that's green, the new version
#    is live at: https://karimzakzouk.github.io/sentinel/

# 5. Verify
helm repo update
helm search repo sentinel/sentinel --versions | head -3
# Should show X.Y.Z at the top of the list.
```

**Do NOT** manually edit `Chart.yaml`, `values.yaml`, `my-values.yaml`, `web/package.json`, or `README.md` version strings — `scripts/sync-version.py` owns those. Editing them by hand will cause `make check-version` to fail in CI.

**Do NOT** create git tags by hand — `make release` does it correctly. A hand-created tag without the matching `VERSION` file bump will cause the Release workflow to abort.

## What's New

### v3.18.0 — Full Codebase Audit Round 2

73 findings addressed across security, correctness, robustness, observability, frontend, and CI:

**Security (P0):**
- SSRF + GitHub PAT exfiltration via `diff_url` from webhook body — rebuild URL from trusted apiURL
- Cluster-wide `secrets: delete` RBAC removed (was wiping cluster TLS if compromised)
- GitHub PAT leaked to logs via URL-embedded token — switched to `http.BasicAuth{}` + error sanitization
- `validateFilePath` now actually blocks Dockerfile / package.json / go.mod / pyproject.toml (the docstring always claimed it did — switch was missing them)
- Slack bot signature bypass on empty signing secret — now refuses event callbacks
- `Config.Validate()` — fail fast on missing LLM/GitHub creds + range checks (refuses to start)
- Kaniko can now clone private repos (passes GitHub token via SecretKeyRef env var)

**Sandbox correctness:**
- `probePaths` data race + cross-incident contamination — local slice copy + mutex
- Level 2 treats 4xx (404/401/405) as failure (was: only 5xx; 404 falsely passed)
- Trigger endpoint probed with correct HTTP method (was: always GET → 405 → false pass)
- Sandbox feedback now includes canary pod logs + probe results (was one-line Summary)
- Sustained window restored from 10s to 30s (missed delayed crashes)

**LLM pipeline:**
- Token + cost tracking (`sentinel_llm_input/output_tokens_total`, `sentinel_llm_cost_usd_total`)
- Fuzzy match threshold raised from 70% to 85% (was applying fixes to wrong function)
- Indent corruption fix in whitespace-insensitive + fuzzy match (was 4+8=12 spaces)
- GitHub API retry with exponential backoff on 429/403 secondary rate limit
- Syntax validators added for Java/Rust/Ruby/C#/C/C++/PHP/Kotlin/Swift/Scala

**Robustness:**
- Pod log streamer leaks goroutine per deleted pod — fixed (call cancel on DELETED, break on IsNotFound)
- `ready` race fixed (atomic.Bool)
- WaitGroup tracking in-flight investigations (15s shutdown deadline)
- Clusterer.Stop() cancels pending timers on shutdown
- Goroutine panic recovery in clusterer handler + ready-setter
- Sandbox/Kaniko cleanup uses fresh context.Background() (was: cancelled parent ctx → leaked namespaces)

**Observability:**
- Per-Level (1/2/3) sandbox failure metrics + retry counter
- Histogram buckets (cumulative) for p50/p95/p99 computation
- Secret redaction in logging (api_key, token, secret, password, etc.)
- ServiceMonitor template for Prometheus Operator
- PodDisruptionBudget template
- HTTP server timeouts (ReadTimeout=10s, IdleTimeout=60s)

**Frontend:**
- Settings "Save Changes" button now POSTs to /settings (was decorative)
- TypeScript + ESLint checks re-enabled (was silently shipping typos)
- Removed `payments` from production_namespaces default (was undoing v3.11.0 fix)
- LLM Router status shows actual configured model (was hardcoded openai/gpt-oss-120b:free)
- Version strings synced across sidebar / package.json / mock logs

**Helm/Docker/CI:**
- Dockerfile uses `npm ci` (was silent lockfile drift)
- Build fails loudly if next build produces no `out/index.html` (was silent placeholder HTML)
- Trivy container CVE scan in CI
- Dependabot.yml for gomod + npm + docker + github-actions
- Web build verified in CI (was: only at release time, too late)
- release-bump syncs web/package.json + my-values.yaml + sidebar version string
- `terminationGracePeriodSeconds: 45` (was 30s, killed SQLite mid-commit)

**Config & API:**
- `/incidents` now gated behind auth (was unauthenticated — incident data exposed)
- Sandbox config fields now actually take effect via runtime config callback (was dead code)
- Runtime config validation (reject negative timeouts, invalid log levels, etc.)
- CRD `spec.service` is now required (was: empty CR triggered investigation against "unknown")
- CRD status now writes incidentId / rootCause / confidence / prUrl (was only phase)

### v3.16.0 — Production Hardening

- **3-layer fix verification** — stronger prompts (replacement completeness), trigger endpoint testing (Layer C), sandbox failure feedback loop (Layer B: generate → verify → feed error back → retry)
- **Sandbox config model** — `sandbox.enabled` + `sandbox.required` + `sandbox.maxRetries` (Fast / Lenient / Strict modes)
- **Operator-controlled RBAC** — sandbox write permissions gated behind `sandbox.enabled` in Helm chart
- **Security hardening** — MaxBytesReader on all webhook handlers, PodSecurity baseline, no forced RunAsNonRoot on canaries
- **Speed** — sustained-Ready window 30s → 10s, no retry on infrastructure errors, current-file-state fetch on retry
- **Bug fixes** — patch CR status error, uvicorn false positive, Layer C trigger endpoint, /healthz version
- **Dead code cleanup** — removed IsUnsupportedNativeProvider, HandleInstallationEvent, hand-rolled base64Encode, stale router.go retry functions

### v3.12.0 — GoAI: 25+ Native LLM Providers

- Replaced hand-rolled OpenAI-only HTTP router with GoAI (github.com/zendev-sh/goai)
- 25+ providers natively: Anthropic (/v1/messages), Bedrock (SigV4), Azure, Google, Vertex, OpenRouter, Groq, Mistral, Cohere, xAI, DeepSeek, Cerebras, Fireworks, Together, Perplexity, Ollama, vLLM, + any OpenAI-compatible
- Pure Go, stdlib-only, +6MB binary size, no Python, no sidecar
- Native ARM64 CI runners (no QEMU) — release time halved

### v3.11.0 — Codebase Audit Fixes

A full codebase audit identified 16 issues (10 CRITICAL, 6 HIGH). All are fixed in this release. Key changes:

- **Syntax validation fixed (CRITICAL)** — The previous syntax validators used `exec.Command("python3"/"gofmt"/"node")` which silently failed in the distroless runtime image (no toolchain) AND when `readOnlyRootFilesystem:true` made `/tmp` unwritable. The validators swallowed the failure with `return nil`, so broken code flowed straight to GitHub PRs. Now uses pure-Go validators (`go/parser`, `encoding/json`, `gopkg.in/yaml.v3`) + mounts `/tmp` as an emptyDir. Regression tests prevent the silent-pass bug from returning.
- **Multi-language sandbox Level 3 (CRITICAL)** — The sandbox's Level 3 verification used a Python-only regex to check if the original error was gone. Go panics, Java `NullPointerException`, JS `TypeError`, etc. silently passed Level 3 — the PR shipped with a `sandbox-verified` label even when the error wasn't fixed. Now uses `agents.ParseError()` which is already multi-language (Python/Go/Java/JS/Rust/Ruby/C/signals).
- **GitHub Enterprise / GitLab / Bitbucket support (CRITICAL)** — `github.com` was hardcoded in 14+ sites across 6 files. Now configurable via `cfg.GitHost` + `cfg.GitHubAPIURL`.
- **Registry-aware Kaniko (CRITICAL)** — `docker.io` was hardcoded in 4 sites. ECR/GCR/ACR/Harbor/Quay users got an invalid destination image. Now the canary's registry is derived from the prod image's registry (so prod pull secrets work), and `cfg.KanikoRegistry` controls the cache repo + dockerconfigjson auth URL.
- **Configurable default branch (HIGH)** — `main` was hardcoded in 4 sites. Repos with `master`/`develop`/`trunk` got the wrong code cloned. Now configurable via `cfg.DefaultBranch`.
- **`cfg.Namespace` everywhere (HIGH)** — The sandbox NetworkPolicy + manual/Slack triggers hardcoded `"sentinel"`. Broke when the Helm release wasn't named `sentinel`. Now uses `cfg.Namespace`.
- **6 dead config fields wired (HIGH)** — `DetectionCooldownSec`, `DetectionTailLines`, `LLMPriorWeight`, `MaxPipelineIterations`, `OutcomeWatchDurationMin`, `OutcomeCheckIntervalSec` were declared and loaded from env but silently discarded. Now actually consumed by their packages.
- **Demo defaults removed (HIGH)** — The demo's `payments` namespace and `payment-api`/`billing-api`/`auth-service` high-risk services leaked into every install. Now the defaults are clean; operators set their own.
- **Language-agnostic prompts (MEDIUM)** — The Investigator + FixProposer prompts used Python-only examples (`def functionName`, `KeyError: 99`, `app/main.py`, `FastAPI/Flask`). LLMs anchor on few-shot examples, so non-Python services got Python-flavored fixes. Now multi-language + injects the detected language into the prompt.
- **Honest embedding default (MEDIUM)** — The default `embeddingModel` was `text-embedding-3-small` (OpenAI-specific) but the default provider is `zai`, so every default install silently fell back to hash embeddings. Now the default is empty = explicit hash fallback. Also added `cfg.LLMEmbeddingDim` to fix a hardcoded-1536 dim corruption bug.
- **Configurable namespace skip list (MEDIUM)** — Was hardcoded to `kube-system`/`kube-public`/`kube-node-lease`. Default expanded to 23 common system namespaces (`calico-system`, `cert-manager`, `ingress-nginx`, `monitoring`, etc.).
- **Configurable Kaniko images + concurrency limit (MEDIUM/LOW)** — Kaniko init/executor images and the max-concurrent-investigations limit (was hardcoded to 3) are now configurable.
- **LLM router limitation documented (MEDIUM)** — The router is OpenAI-compatible only. Native Anthropic/Bedrock/Azure APIs are unsupported and now trigger an ERROR-level startup warning with the LiteLLM/OneAPI proxy workaround.

### v3.8.2 — UI + GitHub App + Slack Bot

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
- [ ] Sandbox Level 4/5 (log error rate + probe latency) — strengthens the gate that auto-merge depends on
- [ ] Regression test generation — convert each incident into permanent coverage

## v4.0.0 — Chatops + Auto-merge + Multi-provider LLM Pool

### Chatops in PR (`/sentinel` commands)

Sentinel now responds to `/sentinel <command>` comments on its PRs. Every command is parsed, permission-checked, executed, and the result is fed back via emoji reactions + an in-place `[SENTINELSTATUS]` comment.

**Informational (read access):**
- `/sentinel help` — auto-generated list, filtered by your permission (🔒 for locked commands)
- `/sentinel status` — re-render the in-place status comment
- `/sentinel explain` — LLM re-explains the root cause in plain English
- `/sentinel trace` — dump the full agent reasoning trace (hypotheses, evidence, decisions)

**Operational (write access):**
- `/sentinel retry-sandbox` — re-run sandbox verification on the current branch
- `/sentinel alternatives` — generate 3 alternative fix approaches via LLM
- `/sentinel apply <A|B|C>` — switch the PR to the chosen alternative (placeholder)
- `/sentinel hold` / `/sentinel hold cancel` — block auto-merge (keeps approval)
- `/sentinel approve` / `/sentinel approve cancel` — approve for auto-merge (audit logged)
- `/sentinel no-merge` / `/sentinel no-merge cancel` — remove from auto-merge queue entirely
- `/sentinel merge` / `/sentinel merge cancel` — merge now through Sentinel (evaluates gates, starts monitoring)
- `/sentinel investigate` — full re-investigation from scratch
- `/sentinel retest` — re-run LLM pipeline keeping the same root cause
- `/sentinel verify <paths>` / `/sentinel verify cancel` — add extra probe paths to sandbox

**Destructive (admin access):**
- `/sentinel revert` — revert this PR (calls kubectl rollout undo)
- `/sentinel rollback <deployment> [--namespace <ns>]` — rollback a specific deployment
- `/sentinel pause [duration]` / `/sentinel resume` — pause autonomous mode globally
- `/sentinel override <sandbox|ci|cooldown>` — force-pass a check (audit logged)

Design principles (informed by Prow + Mergify research):
- Always ACK with a 👍 reaction immediately (Prow's #1 complaint was silence)
- One negation convention: every state-changing command takes `cancel`
- Labels as the gate, a separate merger as the actor (commands never merge directly)
- Auto-generated command registry — `/sentinel help` is generated from code
- In-place editable `[SENTINELSTATUS]` comment (Prow's `[APPROVALNOTIFIER]` pattern)
- Permission errors are actionable — say *why* and *what to do*
- No fun commands — Sentinel is a prod tool

### Auto-merge

When all gates pass, Sentinel can auto-merge PRs — closing the loop from detection to deployment without human intervention.

**Three opt-in layers (all must be true):**
1. Global: `SENTINEL_AUTO_MERGE_ENABLED=true` (or `sentinel.autoMerge.enabled: true` in Helm)
2. Namespace: `SENTINEL_AUTO_MERGE_NAMESPACES=sim,staging`
3. Per-service annotation: `sentinel.io/auto-merge: "true"`

**Three gates (all must pass):**
1. Sandbox verified (hard)
2. Confidence ≥ 0.85 + safety policy allowed (hard)
3. CI green (configurable — soft by default, hard in strict mode)

**Safety valves:**
- Rate limit: max 3 auto-merges per hour (prevents cascade → many bad merges)
- Cooldown: 1 hour pause after any regression detected
- Escape hatch: `/sentinel no-merge` (chatops)

Monitor via the `/automerge` API endpoint (policy status + tracked PRs).

### Multi-provider LLM Pool

Stack multiple API keys with priority-ordered failover + circuit breaker:

```yaml
sentinel:
  llm:
    providers:
      - id: or-1
        provider: openrouter
        apiKey: sk-or-...
        fastModel: anthropic/claude-3.5-sonnet
        frontierModel: anthropic/claude-3.5-sonnet
        priority: 1
      - id: groq-1
        provider: groq
        apiKey: gsk_...
        fastModel: llama-3.1-70b-versatile
        frontierModel: llama-3.1-70b-versatile
        priority: 2
      - id: gemini-1
        provider: gemini
        apiKey: AIza...
        fastModel: gemini-2.0-flash
        frontierModel: gemini-2.0-pro
        priority: 3
```

- Provider 1 fails → automatic failover to provider 2 (within the same LLM call)
- Auth errors trip the breaker for 1 hour; rate limits for 60s; network for 15s
- 2 consecutive failures needed to trip (single flake doesn't bench a provider)
- Pool status exposed via `/providers` API endpoint + `pool.Status()` for the web UI
- Empty config = legacy single-provider mode (backwards compatible)

### Bot Identity (GitHub App)

By default, Sentinel uses your PAT — PRs and comments appear as your personal GitHub account. To make them appear as **`Sentinel[bot]`** instead, configure a GitHub App.

**Three ways to set it up:**

| Path | When | Effort |
|------|------|--------|
| `sentinelctl app setup` | Self-hosted / local dev / GitHub Enterprise | 30 sec, one click on GitHub |
| Public Sentinel App | Cloud-hosted, you want zero-config for users | 2 clicks per user |
| Manual env vars | You want full control, no CLI | 5 min |
| PAT (default) | Quick demo, testing | 0 min |

**Path A — `sentinelctl app setup` (recommended):**

```bash
kubectl -n sentinel port-forward svc/sentinel 8000:8000
sentinelctl app setup --server http://localhost:8000 --token $SENTINEL_API_TOKEN
# Browser opens → click "Create GitHub App" → click "Install" → copy install ID from URL
sentinelctl app set-install <install-id> --server http://localhost:8000 --token $SENTINEL_API_TOKEN
sentinelctl app status --server http://localhost:8000 --token $SENTINEL_API_TOKEN
```

**Path B — Public App (for maintainers hosting Sentinel as a service):**

Create one public GitHub App at https://github.com/settings/apps/new with the permissions listed in [docs/SENTINEL_GITHUB_APP.md](docs/SENTINEL_GITHUB_APP.md). Users install it in 2 clicks at `https://github.com/apps/yourname-sentinel/installations/new`.

**Path C — Manual env vars:**

```bash
SENTINEL_GITHUB_APP_ID=1234567
SENTINEL_GITHUB_APP_PRIVATE_KEY=$(cat sentinel-private-key.pem)
SENTINEL_GITHUB_APP_INSTALL_ID=12345678
```

Or via Helm:

```yaml
sentinel:
  githubAppId: "1234567"
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
  githubAppInstallId: "12345678"
```

**Fallback behavior:** if the App is not configured OR the App token call fails, Sentinel silently uses the PAT. Nothing breaks. Full docs at [docs/SENTINEL_GITHUB_APP.md](docs/SENTINEL_GITHUB_APP.md).

## License

MIT — see [LICENSE](LICENSE)

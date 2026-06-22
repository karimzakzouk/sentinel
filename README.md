<div align="center">

# Sentinel

**The Autonomous AI SRE.**

Detect. Diagnose. Fix. Verify. — autonomously.

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)]()
[![Image Size](https://img.shields.io/badge/image-~28MB-success)]()
[![Pods](https://img.shields.io/badge/pods-1-blue)]()
[![License](https://img.shields.io/badge/license-MIT-green)]()

</div>

---

Sentinel is an autonomous AI Site Reliability Engineer that plugs into any Kubernetes cluster, detects incidents, diagnoses them with an LLM, writes fixes, tests them in a sandbox, and opens Pull Requests — all on its own.

**One binary. One pod. ~28MB image. ~50MB RAM.**

## How it works

```
┌─────────────────────────────────────────────────────────────────┐
│                        Sentinel v3 Pod                          │
│                                                                 │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐  │
│  │ Watcher  │───▶│ Agents   │───▶│ Safety   │───▶│Execution │  │
│  │ (K8s     │    │ (4-agent │    │ (embedded│    │ (GitHub +│  │
│  │  Watch)  │    │  loop)   │    │  policy) │    │  sandbox)│  │
│  └──────────┘    └──────────┘    └──────────┘    └──────────┘  │
│       │                                              │           │
│       ▼                                              ▼           │
│  ┌──────────┐                                 ┌──────────┐      │
│  │  Clone   │                                 │   PR     │      │
│  │  repo    │                                 │  opened  │      │
│  │ (go-git) │                                 └──────────┘      │
│  └──────────┘                                                   │
│       │                                                         │
│       ▼                                                         │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  SQLite (incidents + events + beta priors + vectors)     │  │
│  └──────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  HTTP API (healthz, incidents, settings, investigate)    │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### The 6-layer pipeline

1. **Watch** — Real Kubernetes `Watch()` stream on `SentinelIncident` CRs. Detection latency: ~1 second.
2. **Investigate** — Two-round agentic loop: clone the repo (go-git, pure Go), LLM emits search queries, we run them locally (regexp), LLM forms hypotheses from the results. Same pattern as SWE-agent / OpenHands / Aider.
3. **Gather Evidence** — LLM-generated queries run against the K8s API (events, pods, deployments) in parallel.
4. **Analyze** — Pure math: falsification + Bayes' rule + Beta-distribution priors. Picks the survivor hypothesis.
5. **Govern** — 5 embedded Go rules (production namespace, high-risk service, blast radius, schema-migration rollback block, confidence threshold). No OPA sidecar.
6. **Execute** — 4-phase code fix: prepare branch → Kaniko build canary → 3-level sandbox verification (pod ready → endpoints respond → original error gone) → open PR with `sandbox-verified` label.

### The agent loop (what makes it accurate)

Sentinel doesn't guess which files to look at. It does what real coding agents do:

```
1. Clone the repo locally (go-git, ~2 seconds)
2. Walk the file tree (filtered: no node_modules, vendor, .git, etc.)
3. LLM Round 1: "Given these logs + this file tree, what should I search for?"
   → LLM emits 3-8 search queries: ["DB_PASSWORD", "KeyError", "def main"]
4. Run the searches locally (Go regexp over the cloned files)
   → instant, no API calls, no rate limits
5. LLM Round 2: "Given these search results, what's the root cause + which files should I read?"
   → LLM returns hypotheses + file paths
6. Read those files from disk (zero API calls)
7. Pass source code to the Fix Proposer → writes the patch
8. Open PR with sandbox-verified label
9. Clean up the cloned repo
```

This is the **agentic loop** — the same pattern SWE-agent, OpenHands, and Aider use. It works for any repo, any language, any layout, because the LLM searches by content, not by filename.

## Install

```bash
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 3.0.2 \
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

## Trigger an incident

Apply a `SentinelIncident` CR:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: sentinel.io/v1alpha1
kind: SentinelIncident
metadata:
  name: test-crashloop
  namespace: sim
spec:
  service: my-app
  namespace: sim
  reason: CrashLoopBackOff detected on my-app
  trigger: manual
  repo: myorg/my-app
EOF
```

Watch Sentinel investigate, diagnose, build a canary, verify it in a sandbox, and open a PR — all autonomously:

```bash
kubectl -n sentinel logs -f deploy/sentinel
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
| External services | **0** (SQLite embedded) | Postgres, Redis, Kafka, ES... |

## Configuration

### LLM providers

Sentinel uses the OpenAI-compatible API. Any provider works:

| Provider | `llm.provider` | `llm.baseUrl` |
|----------|----------------|---------------|
| OpenAI | `openai` | (auto) |
| Google Gemini | `gemini` | (auto) |
| z.ai GLM | `zai` | (auto) |
| OpenRouter | `custom` | `https://openrouter.ai/api/v1` |
| Ollama (local) | `ollama` | (auto) |
| vLLM | `vllm` | (auto) |
| LiteLLM proxy | `litellm` | (auto) |
| Any OpenAI-compatible | `custom` | your URL |

### Workload annotations (REQUIRED for code fixes)

Annotate your Deployments/StatefulSets so Sentinel knows what code is running. **The `sentinel.io/branch` annotation is critical** — without it, Sentinel clones `main` which may not match the deployed code, leading to wrong diagnoses.

```yaml
metadata:
  annotations:
    sentinel.io/repo: myorg/my-app           # GitHub repo (required for code fixes)
    sentinel.io/branch: bug/crashloop-typo   # Branch that's actually deployed (REQUIRED)
    sentinel.io/git-commit: abc123...        # Exact commit SHA (optional, most precise)
    sentinel.io/runbook: https://wiki/...    # Runbook URL (optional)
    sentinel.io/team: platform               # Owning team (optional)
    sentinel.io/ignore: "true"               # Opt out of Sentinel (optional)
```

**Priority:** `sentinel.io/git-commit` (exact commit) > `sentinel.io/branch` (branch tip) > `main` (fallback with warning).

If neither annotation is set, Sentinel clones `main` and logs a warning. The diagnosis may be inaccurate if the deployed code differs from `main`.

### RBAC

Sentinel needs cluster-wide read on pods/deployments/events to investigate incidents. By default it can't write anywhere — set `sentinel.rbac.writeNamespaces` to allow fixes:

```bash
--set 'sentinel.rbac.writeNamespaces={sim,staging}'   # can fix in sim + staging
--set 'sentinel.rbac.writeNamespaces={prod}'          # can fix in prod (be careful)
```

## Development

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel

# Build
go build -ldflags='-s -w' -trimpath -o sentinel ./cmd/sentinel

# Test
go test ./... -race

# Run locally (needs a kubeconfig)
./sentinel --addr :8000 --db /tmp/sentinel.db

# Helm lint
helm lint chart/
```

## Architecture

```
sentinel/
├── cmd/sentinel/          # Entrypoint (~250 lines)
├── internal/
│   ├── schemas/           # All types (Pydantic → Go structs)
│   ├── config/            # Env var loading + LLM provider resolution
│   ├── llm/               # OpenAI-compatible client (net/http, no SDK)
│   ├── k8s/               # Workload annotation lookup, pod logs, CR status
│   ├── watcher/           # Real K8s Watch stream on SentinelIncident CRD
│   ├── repo/              # go-git cloner + local search engine
│   ├── agents/            # 4-agent pipeline + state machine
│   │   ├── pipeline.go    #   Wires agents + handles rewinds
│   │   ├── investigator.go#   Two-round agentic loop (clone → search → read)
│   │   ├── evidence_gatherer.go # K8s API queries in parallel
│   │   ├── analyst.go     # Bayes + Beta priors → root cause
│   │   └── fix_proposer.go# Root cause → FixProposal (LLM)
│   ├── safety/            # Embedded policy (5 if-statements, replaces OPA)
│   ├── executors/         # GitHub PRs, K8s patches, sandbox, Kaniko
│   ├── feedback/          # SQLite store (incidents + events + beta + vectors)
│   └── api/               # HTTP API
├── chart/                 # Single-pod Helm chart
├── Dockerfile             # Distroless static, ~28MB
├── go.mod
└── README.md
```

## License

MIT

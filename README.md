# Sentinel v3

**The Autonomous AI SRE — rewritten in Go.**

One binary. One pod. ~25MB image. Same product.

---

## What's new in v3

v3 is a complete rewrite of Sentinel from Python to Go. The goal: make it **the smallest serious AIOps tool in existence**.

| Metric | v2 (Python) | v3 (Go) | Improvement |
|--------|-------------|---------|-------------|
| Image size | 600MB | **~25MB** | 24x smaller |
| Pods | 4 (core + postgres + weaviate + ui) | **1** | 4x fewer |
| RAM at idle | ~1.5GB | **~50MB** | 30x less |
| Disk | ~30GB | **~5GB** | 6x less |
| Cold start | 5–10s | **<1s** | 10x faster |
| Dependencies | 50+ Python packages | **6 Go modules** | 8x fewer |

### What we killed

- **Postgres pod** → SQLite (one file, embedded in the binary's pod)
- **Weaviate pod** → pgvector-style vector search in Go (bag-of-words, zero deps)
- **UI pod** → HTTP API + CLI (Slack integration coming)
- **OPA sidecar** → 5 embedded Go if-statements (same rules, no JVM)
- **Kafka** → already gone in v2, stayed gone
- **Operator (kopf)** → already gone in v2, replaced by real K8s Watch
- **LangGraph** → plain Go state machine (4 nodes, 200 lines)
- **PyGithub** → direct net/http calls
- **sqlalchemy** → modernc.org/sqlite (pure Go, no CGO)

### What we kept

The full pipeline. v3 does everything v2 did:
- Watch a K8s cluster for `SentinelIncident` CRs (real Watch stream, ~1s detection)
- Fetch real pod logs from crashing pods
- Run the 4-agent reasoning pipeline (Investigator → Evidence Gatherer → Analyst → Fix Proposer)
- Embedded safety policy (allow / require_human / deny)
- Open PRs via the GitHub API
- Build canary images with Kaniko
- 3-level sandbox verification (pod ready → endpoints respond → original error gone)
- Beta-distribution confidence tracker (Bayesian priors)
- Vector-similarity RAG over past incidents

### What's NOT in v3.0 (coming in v3.1)

- The CLI (`sentinel incidents list`, etc.) — the HTTP API works, CLI is next
- Slack webhook integration — the API has the hooks, Slack is next
- Real embedding model (v3.0 uses bag-of-words; v3.1 will use sentence-transformers via ONNX)
- Prometheus + Loki evidence queries (v3.0 returns "not configured"; v3.1 wires observability stack)

---

## Install

```bash
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 3.0.0 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

That's it. One pod. ~25MB image. ~50MB RAM. Ready in <1 second.

```bash
kubectl -n sentinel get pods -w
# sentinel-xxx   1/1 Running   0   1s
```

## Verify it works

```bash
# Health check
kubectl -n sentinel port-forward svc/sentinel 8000:8000 &
curl http://localhost:8000/healthz
# → {"status":"ok","service":"core","version":"3.0.0"}

curl http://localhost:8000/readyz
# → {"status":"ready","ready":true}

curl http://localhost:8000/incidents
# → []

kill %1
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
  service: sentinel-sim
  namespace: sim
  reason: CrashLoopBackOff detected on sentinel-sim
  trigger: manual
EOF
```

Watch Sentinel pick it up (~1 second) and run the full pipeline:

```bash
kubectl -n sentinel logs -f deploy/sentinel
# [investigation] sim/test-crashloop — service=sentinel-sim
# [investigation] Phase 1: preparing fix branch...
# [investigation] Phase 2: building canary image with Kaniko...
# [investigation] Phase 3: running sandbox verification...
# [investigation] Phase 4: opening PR...
# [investigation] <id> resolved (status=verified, alpha=2, beta=1)
```

---

## Architecture (v3)

```
┌─────────────────────────────────────────────────────────────┐
│                   Sentinel v3 Pod (~25MB)                    │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Go binary (sentinel)                                │   │
│  │                                                       │   │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐     │   │
│  │  │  Watcher   │→│  Agents    │→│  Safety    │     │   │
│  │  │  (real     │  │  (4-agent  │  │  (embedded │     │   │
│  │  │  Watch)    │  │  pipeline) │  │  policy)   │     │   │
│  │  └────────────┘  └────────────┘  └────────────┘     │   │
│  │         ↓                                    ↓        │   │
│  │  ┌────────────┐                     ┌────────────┐    │   │
│  │  │  SQLite    │←───────────────────│  Execution │    │   │
│  │  │  (incidents│                     │  (GH+K8s+  │    │   │
│  │  │   + vectors│                     │   sandbox+ │    │   │
│  │  │   + beta)  │                     │   kaniko)  │    │   │
│  │  └────────────┘                     └────────────┘    │   │
│  │                                                       │   │
│  │  ┌──────────────────────────────────────────────┐    │   │
│  │  │  HTTP API (healthz, incidents, settings)     │    │   │
│  │  └──────────────────────────────────────────────┘    │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────┐                                           │
│  │  PVC (5GB)   │  ← SQLite DB + incident memory            │
│  └──────────────┘                                           │
└─────────────────────────────────────────────────────────────┘
```

### Code structure

```
v3/
  cmd/sentinel/main.go           # entrypoint (~250 lines)
  internal/
    schemas/                     # all types (Pydantic → Go structs)
    config/                      # env var loading + LLM provider resolution
    llm/                         # OpenAI-compatible client (net/http, no SDK)
    k8s/                         # workload annotation lookup, pod logs, CR status
    watcher/                     # real K8s Watch stream on SentinelIncident CRD
    agents/                      # 4-agent pipeline + state machine
      pipeline.go                #   wires agents + handles rewinds
      investigator.go            #   forms hypotheses from logs + source code
      evidence_gatherer.go       #   queries K8s API to test hypotheses
      analyst.go                 #   Bayes + Beta priors → root cause
      fix_proposer.go            #   root cause → FixProposal (LLM)
    safety/                      # embedded policy (5 if-statements, replaces OPA)
    executors/                   # GitHub PRs, K8s patches, sandbox, Kaniko
      github.go                  #   opens PRs via REST API
      kubernetes.go              #   restart/scale/rollback
      sandbox.go                 #   3-level verification
      kaniko.go                  #   builds canary images
    feedback/                    # SQLite store (replaces Postgres + Weaviate)
      store.go                   #   incidents + events + beta + vector search
    api/                         # HTTP API (replaces UI pod)
  chart/                         # single-pod Helm chart
  Dockerfile                     # distroless static, ~25MB
```

---

## Development

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel
git checkout v3

# Build
cd v3
go build ./cmd/sentinel

# Test
go test ./... -race

# Run locally (needs a kubeconfig)
./sentinel --addr :8000 --db /tmp/sentinel.db

# Helm lint
helm lint chart/
```

---

## License

MIT

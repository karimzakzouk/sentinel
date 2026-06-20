# Sentinel — The Autonomous AI Site Reliability Engineer

<p align="center">
  <strong>Detect. Diagnose. Fix. Verify. — autonomously, safely, at 3am with no human involved.</strong>
</p>

Sentinel sits on top of your existing Kubernetes infrastructure and watches everything — logs, metrics, traces, deployment events. When something looks wrong it doesn't just fire an alert. It investigates. It forms hypotheses, gathers evidence, rules things out, and lands on a root cause. Then it proposes a fix, tests that fix in an isolated environment before touching production, and if the test passes, deploys it.

Engineers wake up to a resolved incident, a full audit trail, and a written post-mortem. **The page never fires.**

This is **not** a SaaS product. Sentinel is an **on-prem Kubernetes operator** — it runs inside your cluster, so your Prometheus metrics, Loki logs, and code never leave your firewall.

---

## Install

```bash
# 1. Add the Helm repo
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm repo update

# 2. Copy the values file and edit it
cp examples/my-values.yaml my-values.yaml
$EDITOR my-values.yaml      # fill in githubToken, llm.apiKey, pick llm.provider

# 3. Install
helm install sentinel sentinel/sentinel \
  -f my-values.yaml \
  --namespace sentinel --create-namespace

# 4. Watch it come up
kubectl -n sentinel get pods -w

# 5. Open the UI
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
```

Everything you need to configure is in `examples/my-values.yaml` — secrets, LLM provider, observability URLs, ingress, storage, governance thresholds. One file, one install command.

---

## Architecture

Six layers. Each has a clear job and a clear output that feeds the next.

| Layer | Job | Tech |
|-------|-----|------|
| **1. Ingestion** | Collect raw data from many sources, normalize to one format, publish to Kafka | OpenTelemetry collectors, Kafka, Confluent Schema Registry |
| **2. Detection** | Watch the cleaned data and flag when something looks wrong. No AI — just math. | Isolation Forest (sklearn), CUSUM, rules-based panic button |
| **3. Reasoning** | Figure out what broke and what to do about it | LangGraph multi-agent state machine, Pydantic state |
| **4. Safety & Governance** | Before any fix reaches execution, verify it is permitted, understand its blast radius, confirm a rollback path exists | Open Policy Agent (Rego), Istio traffic mirroring |
| **5. Execution** | Execute approved fixes universally — Kubernetes API for restart/scale, GitHub PR for code fixes, Terraform for infrastructure | PyGithub, Kubernetes Python client, Terraform CLI |
| **6. Feedback Loop** | Turn every incident into a lesson | Weaviate (vector DB), custom Beta-distribution confidence tracker |

### Agent Architecture (Layer 3)

The reasoning engine uses a **state machine** (LangGraph) where agents pass a structured JSON object validated by Pydantic. The graph supports the `RewindEvent` escape hatch — any agent can wipe the hypotheses and restart the Investigator if it discovers the current investigation is on the wrong track.

| Agent | Job |
|-------|-----|
| **Investigator** | Forms up to 5 root cause hypotheses based on logs and vector DB priors |
| **Evidence Gatherer** | Queries Loki/Prometheus/Jaeger to try and *disprove* the hypotheses |
| **Analyst** | Kills falsified hypotheses, computes a strict math-based confidence score (Beta posterior), picks the survivor |
| **Fix Proposer** | Translates the root cause into a concrete action (Code fix, Scale, Restart, Roll-forward) |

---

## Quick Start

Once Sentinel is installed, it watches your cluster for problems. You can also trigger an investigation manually by creating a `SentinelIncident` CR:

```yaml
apiVersion: sentinel.io/v1alpha1
kind: SentinelIncident
metadata:
  name: manual-investigation
  namespace: default
spec:
  service: payment-api
  namespace: prod
  reason: "5xx errors on /pay endpoint"
```

```bash
kubectl apply -f - <<EOF
apiVersion: sentinel.io/v1alpha1
kind: SentinelIncident
metadata:
  name: manual-investigation
  namespace: default
spec:
  service: payment-api
  namespace: prod
  reason: "5xx errors on /pay endpoint"
EOF
```

Watch Sentinel work:

```bash
kubectl get sentinelincidents -A -w
```

### Auto-discovery

If your pods are annotated with `sentinel.io/repo`, Sentinel will auto-discover the source code:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payment-api
  annotations:
    sentinel.io/repo: "yourorg/payment-api"
spec:
  # ...
```

When a pod crashes, Sentinel reads the annotation, clones the repo, parses the stack trace from the logs to find the exact crashing files, and uses RAG to fetch only the relevant code.

---

## LLM Provider

Sentinel uses the OpenAI Python SDK with `AsyncOpenAI(base_url=..., api_key=...)`, so **any OpenAI-compatible endpoint works**. Set `sentinel.llm.provider` in `my-values.yaml` to one of:

| Provider | Notes |
|----------|-------|
| `openai` | GPT-4o, GPT-4o-mini |
| `gemini` | Gemini 2.0 Flash, 2.5 Pro (native OpenAI endpoint) |
| `zai` | GLM-4.5, GLM-4-flash (default) |
| `litellm` | For **Anthropic Claude** and **AWS Bedrock** — auto-deploys a LiteLLM sidecar pod that translates calls |
| `ollama` | Local models, fully air-gapped |
| `vllm` | Self-hosted HuggingFace models |
| `azure` | Azure OpenAI (set `baseUrl` explicitly) |
| `custom` | Any OpenAI-compatible endpoint (set `baseUrl` explicitly) |

`examples/my-values.yaml` contains commented-out blocks for each provider — uncomment the one you want and edit the model names.

---

## Enterprise Edge Cases

Six advanced fail-safes make Sentinel bulletproof in chaotic, stateful microservice environments.

### 1. The Database Schema Problem (Stateful Rollbacks)

Rolling back a code deployment (`v2` → `v1`) will cause a catastrophic outage if `v2` included a stateful database migration (e.g., dropping a column).

**Solution:** Every deployment is tagged in CI/CD with `has_schema_migration: true/false`. When the Fix Proposer attempts a rollback, OPA checks this flag. If a schema change occurred, autonomous rollbacks are **hard-blocked**, and the agent is forced to execute a "Roll-Forward" hotfix or escalate to a human.

### 2. The Sandbox Illusion (Shadow Traffic)

Booting a sandbox namespace with mocked secrets tests nothing if there is no live traffic to trigger the bug.

**Solution:** Sentinel uses Istio to mirror 1% of live production traffic to the Sandbox namespace. The sandbox processes the exact malformed payload that caused the crash in production (while isolating DB writes). If the Sandbox handles the shadow traffic without throwing a 500, the fix is proven mathematically safe.

### 3. The "Linear Graph" Brittleness (Recursive Reasoning)

A strict DAG pipeline (Investigator → Gatherer → Analyst) cannot pivot if the Gatherer discovers a new symptom mid-investigation.

**Solution:** The `RewindEvent`. Any agent can throw one. If the Evidence Gatherer realizes a critical dependency is down, it halts the linear path, wipes the current `hypotheses` array, and sends the new context back to the Investigator.

### 4. Context Window Explosion (Cascading Failures)

In a massive outage, parsing 50,000 generic `504 Gateway Timeout` logs will overflow the LLM context window and hide the root cause.

**Solution:** Trace-Anchoring. Sentinel anchors the investigation on OpenTelemetry / Jaeger Traces. It uses the Trace ID to find the *very first span* that failed in the call chain. It discards the thousands of downstream errors and zooms straight into the single `Connection Refused` log deep inside the database proxy.

### 5. Vector Database Poisoning (Data Drift)

Architectures evolve. A runbook that worked 6 months ago might now be catastrophic because the infrastructure has changed.

**Solution:** Code-Churn Decay. When saving an incident to Weaviate, it is tagged with the exact `git_commit_hash`. During future retrievals, relevance scores are decayed based on code churn. If the file the historical runbook references has been modified 50 times since the incident, its relevance score is artificially reduced to `0.0`.

### 6. Time-to-Mitigation vs. LLM Latency

Waiting 5 minutes for an LLM to generate hypotheses is unacceptable during a massive DDoS or OOM spike.

**Solution:** The "Panic Button" Fast Path. Sentinel separates Mitigation from Investigation. If CPU > 95% or connections > max, a deterministic rules-engine (no AI) immediately applies mitigation in 5 seconds. The LLM is only invoked *after* stabilization to figure out *why* it happened.

---

## CI/CD

GitHub Actions handle everything — you never build Docker images or package Helm charts yourself:

- **`test.yml`** — runs on every push/PR. Executes pytest, `helm lint`, `helm template` with default + litellm providers, validates `examples/my-values.yaml`.
- **`docker-images.yml`** — runs when you push a `v*.*.*` tag. Builds all 8 service images (ingestion, detection, reasoning, safety, execution, feedback, operator, ui) and pushes them to GitHub Container Registry at `ghcr.io/karimzakzoukz/sentinel/*`. Also tags `:latest`.
- **`release.yml`** — runs when you push a `v*.*.*` tag. Packages the Helm chart, creates a GitHub Release with the .tgz attached, and publishes to GitHub Pages so `helm repo add sentinel https://karimzakzoukz.github.io/sentinel/` works.

The chart defaults to pulling images from GHCR — no manual image building needed.

To cut a new release:

```bash
# Bump chart version in chart/Chart.yaml + appVersion + image tags in values.yaml
git commit -am "Bump to v1.0.5"
git tag v1.0.5
git push origin main --tags
# GitHub Actions builds all 8 Docker images + Helm chart + Release automatically
```

---

## Development

```bash
# Install Python deps in your venv
pip install -e services/common
pip install -e services/ingestion services/detection services/reasoning \
            services/safety services/execution services/feedback services/operator

# Run the test suite
pytest tests/

# Run the live GitHub integration test (actually opens a PR!)
GITHUB_TOKEN=ghp_xxx python scripts/live-test-github.py

# Validate the Helm chart
helm lint chart/
helm template chart/ -f examples/my-values.yaml | less
```

### Project structure

```
sentinel/
├── .github/workflows/         # CI/CD: test + release workflows
├── chart/                     # Helm chart
│   ├── Chart.yaml
│   ├── values.yaml            # defaults
│   ├── policies/              # Rego policies (mounted into OPA)
│   └── templates/
│       ├── _helpers.tpl
│       ├── _deployment.tpl
│       ├── crds/              # SentinelIncident CRD
│       ├── operator/          # kopf operator + RBAC
│       ├── ingestion/         # Layer 1
│       ├── detection/         # Layer 2
│       ├── reasoning/         # Layer 3 + litellm sidecar (optional)
│       ├── safety/            # Layer 4
│       ├── execution/         # Layer 5
│       ├── feedback/          # Layer 6
│       ├── ui/                # Next.js dashboard
│       ├── opa/               # OPA sidecar
│       ├── kafka/             # Strimzi Kafka cluster + topics
│       ├── postgres/          # Bitnami Postgres subchart
│       ├── weaviate/          # Weaviate subchart
│       └── secrets/           # GitHub + LLM API keys
├── examples/
│   └── my-values.yaml         # ← ONE values file. Copy it, edit, install.
├── services/
│   ├── common/                # Shared Pydantic schemas, Kafka client, DB
│   ├── ingestion/             # Real OpenTelemetry-style collectors
│   ├── detection/             # Real Isolation Forest + CUSUM
│   ├── reasoning/             # Real LangGraph + 4 agents
│   ├── safety/                # Real OPA client + Rego policies
│   ├── execution/             # Real PyGithub + K8s + Terraform
│   ├── feedback/              # Real Weaviate + Beta tracker
│   └── operator/              # Real kopf operator
├── ui/                        # Next.js 16 dashboard
├── tests/                     # 50+ unit tests + live GitHub test
└── scripts/                   # Live integration tests
```

---

## Testing

| Test file | What it covers |
|-----------|---------------|
| `test_schemas.py` | Pydantic schema validation — every model rejects malformed input |
| `test_detection.py` | Isolation Forest, CUSUM, Rules detector (panic button), clusterer |
| `test_context.py` | Stack-trace parsing regex (Python/Go/Rust/Java) |
| `test_reasoning_graph.py` | LangGraph edge logic + RewindEvent recursion |
| `test_github_executor.py` | GitHub PR creation with mocked PyGithub |
| `test_beta_tracker.py` | Beta-distribution confidence math |
| `test_llm_providers.py` | Provider URL resolution |
| `test_helm_chart.py` | `helm lint` + `helm template` + CRD/policy/secret verification |
| `live-test-github.py` | **Actually opens a real PR on your GitHub** |

---

## License

MIT

---

Built by an SRE who got paged too many times at 3am.

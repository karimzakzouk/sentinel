# Sentinel — The Autonomous AI Site Reliability Engineer

<p align="center">
  <strong>Detect. Diagnose. Fix. Verify. — autonomously, safely, at 3am with no human involved.</strong>
</p>

Sentinel sits on top of your existing Kubernetes infrastructure and watches everything — logs, metrics, traces, deployment events. When something looks wrong it doesn't just fire an alert. It investigates. It forms hypotheses, gathers evidence, rules things out, and lands on a root cause. Then it proposes a fix, tests that fix in an isolated environment before touching production, and if the test passes, deploys it.

Engineers wake up to a resolved incident, a full audit trail, and a written post-mortem. **The page never fires.**

This is **not** a SaaS product. Sentinel is an **on-prem Kubernetes operator** — it runs inside your cluster, so your Prometheus metrics, Loki logs, and code never leave your firewall.

---

## Table of Contents

- [Architecture](#architecture)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [How It Works](#how-it-works)
- [Enterprise Edge Cases](#enterprise-edge-cases)
- [The UI](#the-ui)
- [Development](#development)
- [Testing](#testing)
- [Production Hardening](#production-hardening)

---

## Architecture

Six layers, each with a clear job and a clear output.

| Layer | Job | Tech |
|-------|-----|------|
| **1. Ingestion** | Collect raw data from many sources, normalize to one format, publish to Kafka | OpenTelemetry collectors, Kafka, Confluent Schema Registry |
| **2. Detection** | Watch the cleaned data and flag when something looks wrong. No AI — just math. | Isolation Forest (sklearn), CUSUM, rules-based panic button |
| **3. Reasoning** | Figure out what broke and what to do about it | LangGraph multi-agent state machine, Pydantic state, Qwen 8B (fast) + Gemini/Claude/GLM 4.5 (frontier) |
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

## Installation

### Prerequisites

- Kubernetes 1.27+ (K3s, AKS, EKS, GKE, self-managed — all work)
- Helm 3.12+
- The Strimzi Kafka operator (Sentinel installs it as a Helm dependency)
- A GitHub Personal Access Token with `repo` scope (for code-fix PRs)
- An LLM API key (any OpenAI-compatible endpoint — z.ai, OpenAI, Anthropic, Ollama, vLLM)

### Option A — Install from the packaged chart (recommended)

```bash
# Download the chart from the GitHub release
wget https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.0/sentinel-1.0.0.tgz

# Install
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel \
  --create-namespace \
  --set sentinel.githubToken=ghp_xxx \
  --set sentinel.llm.apiKey=sk-xxx

# Watch it come up
kubectl -n sentinel get pods -w
```

### Option B — Install from source

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel

helm dependency build chart/

helm install sentinel chart/ \
  --namespace sentinel \
  --create-namespace \
  --set sentinel.githubToken=ghp_xxx \
  --set sentinel.llm.apiKey=sk-xxx
```

### Option C — Add as a Helm repo (after you've hosted the chart)

```bash
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel \
  --namespace sentinel \
  --create-namespace \
  --set sentinel.githubToken=ghp_xxx \
  --set sentinel.llm.apiKey=sk-xxx
```

### Access the UI

```bash
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
# Open http://localhost:8080
```

Or enable the ingress:

```bash
helm upgrade sentinel chart/ \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=sentinel.yourcompany.com
```

---

## Quick Start

Once Sentinel is installed, it watches your cluster for problems. You can also trigger an investigation manually by creating a `SentinelIncident` CR:

```yaml
apiVersion: sentinel.io/v1alpha1
kind: SentinelIncident
metadata:
  name: manual-investigation-payment-api
  namespace: default
spec:
  service: payment-api
  namespace: prod
  reason: "5xx errors on /pay endpoint"
  trigger: manual
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
    sentinel.io/service: "payment-api"
spec:
  # ...
```

When a pod crashes, Sentinel reads the annotation, clones the repo, parses the stack trace from the logs to find the exact crashing files, and uses RAG to fetch only the relevant code.

---

## Configuration

All configuration is in `chart/values.yaml`. Key settings:

| Key | Default | Description |
|-----|---------|-------------|
| `sentinel.githubToken` | `""` | **Required** for code-fix PRs |
| `sentinel.llm.provider` | `zai` | `openai` / `zai` / `gemini` / `ollama` / `vllm` / `litellm` / `azure` / `custom` |
| `sentinel.llm.baseUrl` | `""` | If empty, derived from `provider`. Set explicitly for `azure`/`custom` |
| `sentinel.llm.apiKey` | `""` | **Required** for reasoning |
| `sentinel.llm.fastModel` | `glm-4-flash` | Used for log parsing, evidence gathering |
| `sentinel.llm.frontierModel` | `glm-4.5` | Used for root-cause synthesis, code patches |
| `sentinel.productionNamespaces` | `prod,production,payments` | Comma-separated — OPA enforces stricter rules here |
| `sentinel.confidenceThresholdAutonomous` | `0.92` | Confidence required for autonomous action in production |
| `sentinel.confidenceThresholdHumanReview` | `0.70` | Below this, fixes are denied entirely |
| `sentinel.panicCpuThresholdPct` | `95.0` | CPU% that triggers the deterministic fast-path |
| `ingestion.env.prometheusUrl` | `""` | Your Prometheus URL |
| `ingestion.env.lokiUrl` | `""` | Your Loki URL |
| `ingestion.env.jaegerUrl` | `""` | Your Jaeger URL |
| `ingress.enabled` | `false` | Enable the UI ingress |

### Picking your LLM provider

Sentinel uses the OpenAI Python SDK with `AsyncOpenAI(base_url=..., api_key=...)`. **Any OpenAI-compatible endpoint works.** Set `sentinel.llm.provider` to one of the known providers and Sentinel auto-configures the base URL — or set `sentinel.llm.baseUrl` directly for anything else.

#### OpenAI (GPT-4o, GPT-4o-mini)

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=openai \
  --set sentinel.llm.apiKey=$OPENAI_API_KEY \
  --set sentinel.llm.fastModel=gpt-4o-mini \
  --set sentinel.llm.frontierModel=gpt-4o
```

#### Google Gemini (gemini-2.0-flash, gemini-2.5-pro)

Google ships a native OpenAI-compatible endpoint:

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=gemini \
  --set sentinel.llm.apiKey=$GEMINI_API_KEY \
  --set sentinel.llm.fastModel=gemini-2.0-flash \
  --set sentinel.llm.frontierModel=gemini-2.5-pro
```

#### Anthropic Claude (claude-3-5-sonnet, claude-3-5-haiku)

Anthropic does **not** provide a native OpenAI-compatible endpoint. You need a proxy. Sentinel ships with an optional LiteLLM sidecar — set `provider=litellm` and the chart auto-deploys it:

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=litellm \
  --set sentinel.llm.apiKey=$ANTHROPIC_API_KEY \
  --set sentinel.llm.fastModel=anthropic/claude-3-5-haiku-20241022 \
  --set sentinel.llm.frontierModel=anthropic/claude-3-5-sonnet-20241022
```

The LiteLLM sidecar receives Sentinel's OpenAI-format calls and translates them to Anthropic's native format.

#### z.ai GLM (default — glm-4.5, glm-4-flash)

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=zai \
  --set sentinel.llm.apiKey=$ZAI_API_KEY \
  --set sentinel.llm.fastModel=glm-4-flash \
  --set sentinel.llm.frontierModel=glm-4.5
```

#### Ollama (local models — llama3.1, qwen2.5, etc.)

For fully air-gapped deployments. Run Ollama anywhere reachable from the cluster:

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=ollama \
  --set sentinel.llm.baseUrl=http://ollama.ollama.svc.cluster.local:11434/v1 \
  --set sentinel.llm.apiKey=dummy \
  --set sentinel.llm.fastModel=qwen2.5:7b \
  --set sentinel.llm.frontierModel=qwen2.5:32b
```

#### vLLM (self-hosted HuggingFace models)

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=vllm \
  --set sentinel.llm.baseUrl=http://vllm.vllm.svc.cluster.local:8000/v1 \
  --set sentinel.llm.apiKey=dummy \
  --set sentinel.llm.fastModel=Qwen/Qwen2.5-7B-Instruct \
  --set sentinel.llm.frontierModel=Qwen/Qwen2.5-72B-Instruct
```

#### Azure OpenAI

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=azure \
  --set sentinel.llm.baseUrl=https://myresource.openai.azure.com \
  --set sentinel.llm.apiKey=$AZURE_OPENAI_KEY \
  --set sentinel.llm.fastModel=gpt-4o-mini \
  --set sentinel.llm.frontierModel=gpt-4o
```

#### AWS Bedrock (Claude on Bedrock, etc.)

Use the LiteLLM sidecar:

```bash
helm install sentinel ./sentinel-1.0.0.tgz \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.provider=litellm \
  --set sentinel.llm.apiKey=$AWS_BEDROCK_KEY \
  --set sentinel.llm.fastModel=bedrock/anthropic.claude-3-5-haiku-20241022-v1:0 \
  --set sentinel.llm.frontierModel=bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0
```

#### Any other provider (Mistral, Cohere, Together, Groq, etc.)

Either find their OpenAI-compatible endpoint URL and set `provider=custom` + `baseUrl`, or run LiteLLM and set `provider=litellm`.

Override with `--set` or a custom `values.yaml`:

```bash
helm install sentinel chart/ \
  -f my-values.yaml \
  --set sentinel.githubToken=$GITHUB_TOKEN \
  --set sentinel.llm.apiKey=$LLM_API_KEY
```

---

## How It Works

### The full pipeline

```
Pod crash / 5xx spike / metric anomaly
              │
              ▼
    ┌─────────────────────┐
    │ 1. Ingestion        │  Prometheus / Loki / Jaeger / K8s API
    │ OpenTelemetry →     │  → normalized Signal → Kafka topic
    │   Kafka + Schema    │
    └─────────┬───────────┘
              │
              ▼
    ┌─────────────────────┐
    │ 2. Detection        │  Isolation Forest (multi-dim) + CUSUM (drift)
    │ sklearn + rules     │  + rules-based panic button
    └─────────┬───────────┘
              │
              ▼  AnomalyCluster
    ┌─────────────────────┐
    │ 3. Reasoning        │  Investigator → Evidence Gatherer → Analyst
    │ LangGraph + LLM     │  → Fix Proposer
    │                     │
    │ Trace parsing:      │  RewindEvent escape hatch for recursive reasoning
    │   regex extract     │
    │   source files      │
    │                     │
    │ RAG: Weaviate       │
    │   similar past      │
    │   incidents         │
    │   (with code-churn  │
    │   decay)            │
    └─────────┬───────────┘
              │
              ▼  Incident + FixProposal
    ┌─────────────────────┐
    │ 4. Safety           │  OPA / Rego policies:
    │                     │   - confidence ≥ 0.92 for autonomous prod action
    │                     │   - high-risk services → human approval
    │                     │   - schema migration → rollback hard-blocked
    │                     │   - >20% replica change → human approval
    └─────────┬───────────┘
              │
              ▼  GovernanceDecision (allow / require_human_approval / deny)
    ┌─────────────────────┐
    │ 5. Execution        │  First: sandbox test with 1% mirrored prod traffic
    │ GitOps-first        │  Then:
    │                     │   - restart/scale → K8s API
    │                     │   - code_fix → GitHub PR
    │                     │   - infrastructure → terraform plan → Slack → human click → apply
    └─────────┬───────────┘
              │
              ▼  ExecutionResult
    ┌─────────────────────┐
    │ 6. Feedback         │  Weaviate vector store (with git_commit_at_resolution
    │                     │  for code-churn decay)
    │                     │  Beta(α, β) tracker per (service, fix_pattern)
    │                     │  Postmortem generator (LLM-written markdown)
    └─────────────────────┘
```

### The context optimization

To operate universally across any language (Python, Go, Rust, Java) and scale to massive enterprise monoliths without crashing the LLM's context window, the reasoning engine **does not** read the whole codebase.

1. **Trace parsing** — uses the regex `([a-zA-Z0-9_.-]+\.[a-zA-Z]{1,5})` to parse the stack trace and extract the exact source files involved in the crash.
2. **Targeted fetch** — retrieves *only* those specific files. If the error spans multiple files, it uses RAG to fetch related dependency classes.

This is implemented in `services/reasoning/context.py`.

---

## Enterprise Edge Cases

Six advanced fail-safes make Sentinel bulletproof in chaotic, stateful microservice environments.

### 1. The Database Schema Problem (Stateful Rollbacks)

Rolling back a code deployment (`v2` → `v1`) will cause a catastrophic outage if `v2` included a stateful database migration (e.g., dropping a column).

**Solution:** Every deployment is tagged in CI/CD with `has_schema_migration: true/false`. When the Fix Proposer attempts a rollback, OPA checks this flag. If a schema change occurred, autonomous rollbacks are **hard-blocked**, and the agent is forced to execute a "Roll-Forward" hotfix or escalate to a human. Implemented in `services/safety/policies/schema_migration.rego`.

### 2. The Sandbox Illusion (Shadow Traffic)

Booting a sandbox namespace with mocked secrets tests nothing if there is no live traffic to trigger the bug.

**Solution:** Sentinel uses a service mesh (Istio/Envoy) to mirror 1% of live production traffic to the Sandbox namespace. The sandbox processes the exact malformed payload that caused the crash in production (while isolating DB writes). If the Sandbox handles the shadow traffic without throwing a 500, the fix is proven mathematically safe. Implemented in `services/execution/sandbox.py`.

### 3. The "Linear Graph" Brittleness (Recursive Reasoning)

A strict DAG pipeline (Investigator → Gatherer → Analyst) cannot pivot if the Gatherer discovers a new symptom mid-investigation.

**Solution:** The `RewindEvent`. Any agent can throw a `RewindEvent`. If the Evidence Gatherer realizes a critical dependency is down, it halts the linear path, wipes the current `hypotheses` array, and sends the new context back to the Investigator. This enables recursive debugging without losing the safety of strict Pydantic JSON schemas. Implemented in `services/reasoning/graph.py` + `services/reasoning/agents/`.

### 4. Context Window Explosion (Cascading Failures)

In a massive outage, parsing 50,000 generic `504 Gateway Timeout` logs will overflow the LLM context window and hide the root cause.

**Solution:** Trace-Anchoring. Sentinel anchors the investigation on OpenTelemetry / Jaeger Traces. It uses the Trace ID to find the *very first span* that failed in the call chain. It discards the thousands of downstream errors and zooms straight into the single `Connection Refused` log deep inside the database proxy. Implemented in `services/reasoning/context.py:anchor_span()`.

### 5. Vector Database Poisoning (Data Drift)

Architectures evolve. A runbook that worked 6 months ago might now be catastrophic because the infrastructure has changed.

**Solution:** Code-Churn Decay. When saving an incident to Weaviate, it is tagged with the exact `git_commit_hash`. During future retrievals, relevance scores are decayed based on code churn. If the file the historical runbook references has been modified 50 times since the incident, its relevance score is artificially reduced to `0.0`. Implemented in `services/reasoning/context.py:_compute_churn_decay()`.

### 6. Time-to-Mitigation vs. LLM Latency

Waiting 5 minutes for an LLM to generate hypotheses is unacceptable during a massive DDoS or OOM spike.

**Solution:** The "Panic Button" Fast Path. Sentinel separates Mitigation from Investigation. If CPU > 95% or connections > max, a deterministic rules-engine (no AI) immediately applies mitigation (e.g., shed load, horizontal scale) in 5 seconds. The LLM is only invoked *after* stabilization to figure out *why* it happened. Implemented in `services/detection/detectors/rules.py`.

---

## The UI

Sentinel ships with a Jenkins-style dashboard — utilitarian, dense, real-time. Built with Next.js 16 + Tailwind.

Features:
- **Live dashboard** — active incidents, resolved count, PRs opened, layer health
- **Incident list** — filterable by status / severity / service
- **Incident detail** — root cause, hypotheses, evidence, agent timeline, governance decision, PR link, postmortem
- **Policies viewer** — read the actual Rego policies
- **Live updates** — SWR polls every 3-5 seconds

The UI talks to the reasoning service's REST API (`/incidents`, `/incidents/:id`, `/metrics`). All API calls are proxied through a Next.js API route to avoid CORS issues.

---

## Development

### Prerequisites

- Python 3.12+
- Node.js 22+ (for the UI)
- Docker (for building service images)

### Run tests

```bash
# Install Python deps in your venv
pip install -e services/common
pip install -e services/ingestion services/detection services/reasoning \
            services/safety services/execution services/feedback services/operator

# Run the test suite
pytest tests/

# Run the live GitHub integration test (actually opens a PR!)
python scripts/live-test-github.py
```

### Build Docker images

Each service has its own Dockerfile. Build them with:

```bash
for svc in ingestion detection reasoning safety execution feedback operator ui; do
  docker build -t sentinel/$svc:1.0.0 -f services/$svc/Dockerfile .
done
```

### Validate the Helm chart

```bash
helm lint chart/
helm template chart/ --set sentinel.githubToken=test --set sentinel.llm.apiKey=test | less
```

### Project structure

```
sentinel/
├── chart/                          # Helm chart
│   ├── Chart.yaml
│   ├── values.yaml
│   ├── policies/                   # Rego policies (mounted into OPA)
│   └── templates/
│       ├── _helpers.tpl
│       ├── _deployment.tpl
│       ├── crds/                   # SentinelIncident CRD
│       ├── operator/               # kopf operator + RBAC
│       ├── ingestion/              # Layer 1
│       ├── detection/              # Layer 2
│       ├── reasoning/              # Layer 3
│       ├── safety/                 # Layer 4
│       ├── execution/              # Layer 5
│       ├── feedback/               # Layer 6
│       ├── ui/                     # Next.js dashboard
│       ├── opa/                    # OPA sidecar
│       ├── kafka/                  # Strimzi Kafka cluster + topics
│       ├── postgres/               # Bitnami Postgres subchart
│       ├── weaviate/               # Weaviate subchart
│       └── secrets/                # GitHub + LLM API keys
├── services/
│   ├── common/                     # Shared Pydantic schemas, Kafka client, DB
│   ├── ingestion/                  # Real OpenTelemetry-style collectors
│   ├── detection/                  # Real Isolation Forest + CUSUM
│   ├── reasoning/                  # Real LangGraph + 4 agents
│   ├── safety/                     # Real OPA client + Rego policies
│   ├── execution/                  # Real PyGithub + K8s + Terraform
│   ├── feedback/                   # Real Weaviate + Beta tracker
│   └── operator/                   # Real kopf operator
├── ui/                             # Next.js 16 dashboard
├── tests/                          # 40+ unit tests + live GitHub test
└── scripts/                        # Live integration tests
```

---

## Testing

The test suite covers:

| Test file | What it covers |
|-----------|---------------|
| `test_schemas.py` | Pydantic schema validation — every model rejects malformed input |
| `test_detection.py` | Isolation Forest, CUSUM, Rules detector (panic button), clusterer |
| `test_context.py` | Stack-trace parsing regex (Python/Go/Rust/Java) |
| `test_reasoning_graph.py` | LangGraph edge logic + RewindEvent recursion |
| `test_github_executor.py` | GitHub PR creation with mocked PyGithub |
| `test_beta_tracker.py` | Beta-distribution confidence math |
| `test_helm_chart.py` | `helm lint` + `helm template` + CRD/policy/secret verification |
| `live-test-github.py` | **Actually opens a real PR on your GitHub** |

Run everything:

```bash
pytest tests/
```

---

## Production Hardening

Before deploying to a real production cluster:

1. **Image registry** — Build and push all 8 Docker images (7 services + UI) to your private registry. Set `image.registry` in `values.yaml`.

2. **Database persistence** — Increase `postgres.primary.persistence.size` and `weaviate.storage.size` for production workloads.

3. **LLM** — Pick the right LLM endpoint for your latency/budget tradeoff. The `fastModel` is called ~5x per incident; the `frontierModel` ~2x. For an enterprise OpenAI-compatible endpoint, set:
   ```yaml
   sentinel:
     llm:
       baseUrl: "https://your-llm-endpoint/v1"
       fastModel: "gpt-4o-mini"
       frontierModel: "gpt-4o"
   ```

4. **Slack approval webhook** — Set `sentinel.approvalWebhookUrl` so human-approval requests land in Slack.

5. **Network policies** — The chart deploys with permissive RBAC (cluster-wide read). Tighten in production by limiting the ClusterRole to specific namespaces.

6. **Backups** — Postgres holds the incident + event log + governance audit trail. Configure regular backups. Weaviate holds the historical incident vectors — also back up.

7. **Monitoring Sentinel itself** — Each service exposes `/healthz` and `/metrics`. Wire these into your existing Prometheus.

---

## License

MIT

---

## Acknowledgments

Sentinel stands on the shoulders of giants:
- [LangGraph](https://github.com/langchain-ai/langgraph) for the agent state machine
- [Strimzi](https://strimzi.io/) for Kafka on Kubernetes
- [Open Policy Agent](https://www.openpolicyagent.org/) for governance
- [Weaviate](https://weaviate.io/) for the vector DB
- [kopf](https://kopf.readthedocs.io/) for the K8s operator framework
- [PyGithub](https://pygithub.readthedocs.io/) for GitHub API access
- [shadcn/ui](https://ui.shadcn.com/) patterns for the dashboard UI

Built by an SRE who got paged too many times at 3am.

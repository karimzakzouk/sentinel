# Sentinel

**The Autonomous AI Site Reliability Engineer.**

Detect. Diagnose. Fix. Verify. — autonomously.

---

## What's new in v2.0.0

This is a breaking refactor. See [MIGRATION.md](MIGRATION.md) for upgrade instructions.

- **Single-process core mode only** — distributed/Kafka mode removed. Simpler, faster, fewer moving parts.
- **Real K8s Watch on the CRD watcher** — incident detection latency: ~10s → ~1s.
- **Security hardened by default** — auto-generated Postgres password, UI auth on by default, NetworkPolicy deny-by-default, all containers run as non-root.
- **Structured logging** (structlog) — JSON output with incident_id/service/agent context.
- **83 tests** (up from 56), including 9 helm tests and 18 new unit tests.
- **Cleaner codebase** — `services/core/main.py` went from 1,217 lines to 200, split into `loops/`, `k8s/`, `app.py`, `runtime_settings.py`.

---

## Install

```bash
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm install sentinel sentinel/sentinel -n sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini
```

The `helm install` output will print:
- The auto-generated Postgres password
- The auto-generated UI password (credentials mode is default)

```bash
kubectl -n sentinel get pods -w
```

Wait for the 4 pods (`core`, `postgresql`, `weaviate`, `ui`) to be `Running`, then open the UI:

```bash
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
```

Open `http://localhost:8080` and log in with the printed password.

---

## Architecture

**Core mode** (default and only mode): ONE pod runs all 6 layers in a single process. No Kafka, no Zookeeper, no operators. 4 pods total:

| Pod | What it does |
|-----|-------------|
| `sentinel-core` | All 6 layers (ingestion, detection, reasoning, safety, execution, feedback) + OPA sidecar |
| `sentinel-postgresql` | Incident store, event log, Beta tracker |
| `sentinel-weaviate` | Vector DB for RAG (past incident memory) |
| `sentinel-ui` | Next.js dashboard |

### The 6 layers

1. **Ingestion** — Collects K8s events, Prometheus metrics, Loki logs, Jaeger traces
2. **Detection** — Isolation Forest + CUSUM + rules-based panic button
3. **Reasoning** — LangGraph multi-agent (Investigator → Evidence Gatherer → Analyst → Fix Proposer)
4. **Safety** — OPA Rego policies (confidence gates, blast radius, schema-migration blocks)
5. **Execution** — GitHub PRs, K8s API, Kaniko image build, 3-level sandbox verification
6. **Feedback** — Weaviate vector store + Beta-distribution confidence tracker

### Code structure (v2.0.0)

```
services/
  common/         # Shared Pydantic schemas, config, logging
  core/           # Single-process entrypoint
    main.py       # FastAPI app + lifespan (200 lines)
    app.py        # HTTP routes
    runtime_settings.py  # Pydantic Settings v2
    loops/        # 7 background asyncio tasks (one per file)
    k8s/          # Annotation lookup, pod logs, CR status patch
  detection/      # Detectors (rules, CUSUM, IsolationForest, clusterer)
  reasoning/      # LangGraph agents + context fetcher + LLM router
  safety/         # OPA Rego policies
  execution/      # GitHub executor, K8s executor, sandbox, Kaniko
  feedback/       # Beta tracker, Weaviate client, postmortem generator
chart/            # Helm chart
ui/               # Next.js dashboard
tests/            # 83 tests (unit + integration stubs)
```

---

## Configuration

All configuration is done from the UI at `/settings` after install. You can also use a values file:

```bash
helm install sentinel sentinel/sentinel -f my-values.yaml -n sentinel --create-namespace
```

### LLM providers

Sentinel uses the OpenAI-compatible API. Supported providers:
- OpenAI, Google Gemini, z.ai GLM, Anthropic (via LiteLLM), Ollama, vLLM, Azure, OpenRouter, any custom OpenAI-compatible endpoint.

Pick from the dropdown in the setup wizard or Settings page.

### Security defaults (v2.0.0)

| Setting | Default | Override |
|---------|---------|----------|
| Postgres password | Auto-generated 32-char | `postgres.auth.password=...` |
| UI auth mode | `credentials` (auto-generated password) | `ui.auth.mode=disabled\|credentials\|github` |
| NetworkPolicy | Enabled (deny-by-default) | `networkPolicy.enabled=false` |
| RBAC scope | Namespace-scoped | `rbac.clusterScope=true` |
| Weaviate | Enabled | `weaviate.enabled=false` |
| Container user | non-root (UID 1000) | n/a (always non-root) |

---

## Development

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel

# Install dev deps
pip install -e services/common
pip install pytest pytest-asyncio langgraph langchain-openai openai \
  pygithub kubernetes weaviate-client scikit-learn httpx tenacity structlog \
  "sqlalchemy[asyncio]" asyncpg fastapi uvicorn prometheus-client ruff mypy

# Run tests
PYTHONPATH=services/common:services/ingestion:services/detection:services/reasoning:services/safety:services/execution:services/feedback:services/core \
  pytest tests/

# Lint + format
ruff check services/ tests/
ruff format services/ tests/

# Type check (strict on common/)
mypy services/common/sentinel_common/

# Build the UI
cd ui && bun install && bun run build

# Lint the chart
helm lint chart/
```

### Pre-commit hooks

```bash
pip install pre-commit
pre-commit install
```

This runs ruff, mypy (on common/), and standard hygiene checks on every commit.

---

## License

MIT

# Sentinel

**The Autonomous AI Site Reliability Engineer.**

Detect. Diagnose. Fix. Verify. — autonomously.

---

## Install

```bash
helm repo add sentinel https://karimzakzoukz.github.io/sentinel/
helm install sentinel sentinel/sentinel -n sentinel --create-namespace
```

That's it. No values file needed. No Kafka. No operators to install first.

```bash
kubectl -n sentinel get pods -w
```

Wait for all 4 pods to be `Running`, then open the UI:

```bash
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
```

Open `http://localhost:8080` — the setup wizard will guide you through:
1. Paste your GitHub token
2. Pick your LLM provider + API key
3. Done

Settings persist to Kubernetes and survive pod restarts. Change anything later from the Settings page.

---

## Architecture

**Core mode** (default): ONE pod runs all 6 layers in a single process. No Kafka, no Zookeeper. 4 pods total:

| Pod | What it does |
|-----|-------------|
| `sentinel-core` | All 6 layers (ingestion, detection, reasoning, safety, execution, feedback) + OPA sidecar |
| `sentinel-postgresql` | Incident store, event log, Beta tracker |
| `sentinel-weaviate` | Vector DB for RAG (past incident memory) |
| `sentinel-ui` | Next.js dashboard |

**Distributed mode** (for horizontal scaling): 7 service pods + Kafka + Zookeeper. Set `mode: distributed` in values.yaml. Requires Strimzi Kafka operator.

### The 6 layers

1. **Ingestion** — Collects K8s events, Prometheus metrics, Loki logs, Jaeger traces
2. **Detection** — Isolation Forest + CUSUM + rules-based panic button
3. **Reasoning** — LangGraph multi-agent (Investigator → Evidence Gatherer → Analyst → Fix Proposer)
4. **Safety** — OPA Rego policies (confidence gates, blast radius, schema-migration blocks)
5. **Execution** — GitHub PRs, K8s API, Terraform, Istio traffic-mirror sandbox
6. **Feedback** — Weaviate vector store + Beta-distribution confidence tracker

---

## Configuration

All configuration is done from the UI at `/settings` after install. You can also use a values file:

```bash
helm install sentinel sentinel/sentinel -f my-values.yaml -n sentinel --create-namespace
```

See `values-example.yaml` in the chart for all options.

### LLM providers

Sentinel uses the OpenAI-compatible API. Supported providers:
- OpenAI, Google Gemini, z.ai GLM, Anthropic (via LiteLLM), Ollama, vLLM, Azure

Pick from the dropdown in the setup wizard or Settings page.

---

## CI/CD

GitHub Actions handle everything:
- **Test** — pytest + helm lint on every push
- **Docker images** — builds all images, pushes to Docker Hub + GHCR
- **Release** — packages Helm chart, publishes to GitHub Releases + GitHub Pages

Images are tagged `latest` + version number. The chart defaults to `latest`.

---

## Development

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel

# Run tests
pytest tests/

# Build the UI
cd ui && bun install && bun run build

# Lint the chart
helm lint chart/
```

---

## License

MIT

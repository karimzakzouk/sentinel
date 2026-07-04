# Sentinel Helm Chart

The autonomous AI SRE for Kubernetes. Detects incidents, reasons about root causes via a 4-agent LLM pipeline, writes code fixes, verifies them in a 5-level sandbox, and opens GitHub PRs.

## Quick Start

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=ghp_xxx \
  --set sentinel.llm.apiKey=sk-xxx \
  --set sentinel.llm.provider=openrouter
```

Get your API token + open the UI:

```bash
kubectl -n sentinel exec deploy/sentinel -- printenv SENTINEL_API_TOKEN
kubectl -n sentinel port-forward svc/sentinel 8000:8000
# Open http://localhost:8000
```

## Key Configuration

| Parameter | Description | Default |
|---|---|---|
| `sentinel.githubToken` | GitHub PAT for repo clone + PR creation | `""` |
| `sentinel.apiToken` | Admin API token for web UI auth | random 32-char hex |
| `sentinel.allowedGithubRepos` | Comma-separated repo allowlist | `""` |
| `sentinel.llm.provider` | LLM provider (openrouter, gemini, openai, etc.) | `zai` |
| `sentinel.llm.apiKey` | LLM API key | `""` |
| `sentinel.llm.fastModel` | Fast/cheap model for RCA | `""` |
| `sentinel.llm.frontierModel` | Strong model for code fixes | `""` |
| `sentinel.sandbox.enabled` | Enable 5-level sandbox verification | `true` |
| `sentinel.sandbox.required` | Require sandbox pass before PR | `true` |
| `sentinel.rbac.writeNamespaces` | Namespaces Sentinel can write to | `[]` |
| `sentinel.autoMerge.enabled` | Enable auto-merge | `false` |
| `sentinel.autoMerge.maxPerHour` | Max auto-merges per hour | `3` |
| `sentinel.autoMerge.minConfidence` | Min confidence for auto-merge | `0.50` |

For the full values reference, see `values.yaml` or `my-values.yaml`.

## GitHub App (Recommended)

For PRs to appear as `Sentinel[bot]` instead of your personal account, configure a GitHub App:

```bash
sentinelctl app setup
```

See `docs/SENTINEL_GITHUB_APP.md` for the full guide.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Detection (log streaming + pod status + Alertmanager)  │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│  4-Agent Pipeline                                        │
│  Investigator → EvidenceGatherer → Analyst → FixProposer │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│  5-Level Sandbox Verification                            │
│  L1: Pod Ready  L2: Health OK  L3: Error Gone            │
│  L4: Regression Test  L5: Reproducer                     │
└──────────────────┬───────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────┐
│  GitHub PR (as Sentinel[bot]) + optional Auto-Merge      │
└──────────────────────────────────────────────────────────┘
```

## Features

- **Autonomous detection** via live log streaming, pod status watching, Alertmanager webhooks
- **4-agent LLM pipeline** with multi-provider failover + circuit breaker
- **5-level sandbox verification** (Kaniko build → canary pod → health → error gone → regression test → reproducer)
- **19 chatops commands** via GitHub PR comments (`/sentinel help`, `/sentinel status`, etc.)
- **11-gate auto-merge policy** with rate limiting + cooldown
- **Web dashboard** with SSE real-time updates (12 sections)
- **Predictive PR review** — warns about similar past incidents on new PRs
- **Slack bot** for interactive investigation triggers

## Upgrade

```bash
helm repo update
helm upgrade sentinel sentinel/sentinel --namespace sentinel -f my-values.yaml
```

## Uninstall

```bash
helm uninstall sentinel -n sentinel
kubectl delete namespace sentinel
```

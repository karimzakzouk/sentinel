# Sentinel Helm Chart

Sentinel is an autonomous AI SRE that watches your Kubernetes cluster for
production crashes, diagnoses the root cause with an LLM, writes the fix,
verifies it in a sandbox, and opens a Pull Request — all on its own.

One binary. One pod. 43MB image. ~50MB RAM. Zero external dependencies.

## Install

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 1.0.0 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

## Required values

| Value | Description |
|-------|-------------|
| `sentinel.githubToken` | GitHub PAT for cloning repos + opening PRs |
| `sentinel.llm.apiKey` | LLM provider API key |
| `sentinel.rbac.writeNamespaces` | List of namespaces Sentinel can write fixes to |

## Common configurations

### GitHub App (verified bot identity)

```bash
--set sentinel.githubAppId=123 \
--set sentinel.githubAppPrivateKey="$(cat sentinel-private-key.pem)" \
--set sentinel.githubAppInstallId=456
```

### Multi-provider LLM pool

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
```

### Auto-merge (11 gates)

```yaml
sentinel:
  autoMerge:
    enabled: true
    namespaces: ["sim", "staging"]
    minConfidence: 0.85
    requireCI: true
    maxPerHour: 3
    cooldownMin: 60
```

### Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: sentinel.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: sentinel-tls
      hosts:
        - sentinel.example.com
```

### Sandbox secrets copy

By default, Sentinel does NOT have cluster-wide Secrets read access. The
sandbox runner needs to read prod secrets to copy them into the sandbox
namespace (so the canary can access the same env vars as prod). To enable:

```bash
--set sentinel.rbac.readSecrets=true
```

### Tier 1 features (close the autonomous loop)

```yaml
sentinel:
  tier1:
    postMergeWatch:
      enabled: true    # requires observability.prometheusUrl
    sloWatch:
      enabled: true    # requires observability.prometheusUrl
    causalRCA:
      enabled: true
    admissionWebhook:
      enabled: true    # requires TLS cert at certDir
```

### Tier 2 features (the brain)

```yaml
sentinel:
  tier2:
    attest:
      enabled: true    # Ed25519-signed audit trail
    llmCostRouter:
      enabled: true    # cost-aware LLM routing + semantic cache
    chaos:
      enabled: true
      namespaces: ["staging"]
      intervalMin: 60
```

## Values reference

See [values.yaml](values.yaml) for the full list (~50 knobs).

## RBAC

The chart creates:
- **ClusterRole** (read-only): pods, pods/log, services, configmaps, events,
  namespaces, nodes, deployments, statefulsets, daemonsets, replicasets,
  sentinelincidents. No secrets by default.
- **Per-namespace Role + RoleBinding** for each namespace in
  `sentinel.rbac.writeNamespaces`: create/update/patch/delete on
  deployments, statefulsets, pods, configmaps, services.
- **Role** in the Sentinel namespace for Kaniko builds.
- **Secrets read** is opt-in via `sentinel.rbac.readSecrets` (default false).

## Upgrade

```bash
helm repo update
helm upgrade sentinel sentinel/sentinel --version 1.0.0 \
  --namespace sentinel \
  -f my-values.yaml
```

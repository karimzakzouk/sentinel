# Security Policy

## Supported versions

Sentinel is actively developed. Security fixes are applied to the latest release only.

| Version | Supported          |
|---------|--------------------|
| 1.0.x   | Supported          |
| < 1.0   | Upgrade required   |

## Reporting a vulnerability

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please report vulnerabilities privately via GitHub Security Advisories:

1. Go to https://github.com/karimzakzouk/sentinel/security/advisories/new
2. Click "Report a vulnerability"
3. Fill in the details: description, reproduction steps, affected versions, suggested fix

You will receive a response within 48 hours. If the vulnerability is confirmed, we will:
- Publish a GitHub Security Advisory
- Release a patch version within 7 days (severity-dependent)
- Credit you in the advisory (unless you prefer to remain anonymous)

## Threat model

Sentinel runs in your Kubernetes cluster with elevated RBAC (it can read pods,
logs, events, and — if you enable it — secrets). The primary attack surfaces
are:

### 1. Prompt injection via pod logs

An attacker who can write to a watched pod's logs could trick the LLM into
generating malicious code. Sentinel defends against this with five layers:

1. **Repo allowlist** (`SENTINEL_ALLOWED_GITHUB_REPOS`) — only annotated repos
   are cloned.
2. **Path-confined ReadFile** (`internal/repo/cloner.go`) — LLM-returned file
   paths cannot escape the cloned repo (absolute paths, parent escapes, and
   symlinks pointing outside root are all rejected).
3. **19-pattern security scanner** (`internal/agents/security.go`) — blocks
   `os.system`, `eval`, `exec`, `pickle.loads`, `yaml.load`, `curl|bash`,
   reverse shells, hardcoded secrets, Dockerfile tampering, env exfiltration.
4. **Reviewer-agent LLM** (`internal/agents/security.go`) — a second LLM call
   with a different system prompt audits the fix for prompt-injection indicators.
5. **Sandbox NetworkPolicy default-deny** (`internal/executors/sandbox_k8s.go`)
   — the canary pod has no egress except what's explicitly allowed.

### 2. GitHub token exfiltration

The fix proposer has access to your GitHub token. Sentinel defends against
this by:
- Never logging the token (the logging package redacts any field whose key
  matches `api_key`, `token`, `secret`, `password`, etc.)
- Never passing it to the LLM as context
- The security scanner blocks any fix that tries to exfiltrate tokens
  (`requests.post`, `urllib.request.urlopen` to non-GitHub hosts, etc.)
- The cloner sanitizes error messages to strip embedded tokens

### 3. Sandbox escape

The sandbox namespace has a default-deny NetworkPolicy. The canary pod runs
with a forced security context:
- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: ["ALL"]`
- `seccompProfile: RuntimeDefault`

### 4. PromQL injection via prompt injection

The Evidence Gatherer agent sends LLM-generated PromQL queries to your
Prometheus. An attacker who can influence pod logs could prompt-inject the
LLM into generating queries that read any metric your Prometheus has (data
exfiltration via metric labels). Sentinel does NOT currently filter PromQL.

**Mitigations:**
- Run Sentinel's Prometheus queries through a read-only proxy that filters
  queries
- Use Prometheus RBAC to restrict which metrics Sentinel's token can read
- Set `SENTINEL_PROMETHEUS_URL` to a Prometheus instance that only has
  non-sensitive metrics
- Leave `SENTINEL_PROMETHEUS_URL` empty to disable Prometheus evidence
  gathering entirely

### 5. RBAC escalation

Sentinel's RBAC is scoped to the namespaces you specify in
`sentinel.rbac.writeNamespaces`. It does NOT have cluster-admin.

**Secrets read is opt-in** (`sentinel.rbac.readSecrets`, default false). The
sandbox runner needs to read prod secrets to copy them into the sandbox
namespace — operators who want this must explicitly enable it. The ClusterRole
has NO secrets access by default.

### 6. Alertmanager webhook injection

The `/webhook/alertmanager` endpoint accepts Alertmanager payloads and
triggers investigations. Without auth, anyone who can reach the endpoint can
trigger investigations. Set `SENTINEL_ALERTMANAGER_WEBHOOK_SECRET` and
configure Alertmanager to send it in the `X-Sentinel-Webhook-Secret` header.

### 7. Admin API

The admin API endpoints (`POST /incidents`, `POST /pause`, `POST /resume`,
`POST /settings`, `/admin/*`) require a Bearer token (`SENTINEL_API_TOKEN`).
Without it, the admin API is unauthenticated — set the token in production.

## What Sentinel cannot defend against

- A compromised LLM provider (OpenAI, Anthropic, etc.) that returns malicious
  completions despite your prompt — the security scanner catches known
  dangerous patterns but cannot detect novel attacks
- A compromised Kubernetes control plane
- A compromised GitHub account with write access to your repo (Sentinel's PRs
  would be indistinguishable from a malicious PR)

## Hardening recommendations

- Run Sentinel with `sentinel.rbac.writeNamespaces={sim}` — restrict write
  access to a single sandbox namespace
- Set `sentinel.apiToken` — protect the admin API (trigger/pause/resume/config)
- Set `sentinel.sandboxRequired=true` — refuse to open PRs that haven't passed
  sandbox verification
- Set `sentinel.autoMerge.enabled=false` initially — let humans review every
  PR for the first few weeks
- Set `sentinel.rbac.readSecrets=false` (default) — don't grant cluster-wide
  secrets read unless you need the sandbox to copy prod secrets
- Enable `sentinel.tier2.attest.enabled=true` — sign every autonomous action
  for audit compliance
- Audit the Sentinel GitHub App's permissions quarterly

## Supply chain

- **Container images are signed** via cosign (keyless OIDC). Verify with:
  ```bash
  cosign verify ghcr.io/karimzakzouk/sentinel:1.0.0 \
    --certificate-identity-regexp 'https://github.com/karimzakzouk/sentinel/.github/workflows/' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```
- **SBOM (SPDX)** is attached to every image. Download with:
  ```bash
  cosign download sbom ghcr.io/karimzakzouk/sentinel:1.0.0
  ```
- **Helm chart** is published to gh-pages. Chart signing is planned.
- **All GitHub Actions** are pinned to version tags (not `@master`).
- **Dependabot** keeps dependencies fresh (gomod, npm, docker, github-actions).

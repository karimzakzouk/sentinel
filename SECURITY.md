# Security Policy

## Supported versions

Sentinel is actively developed. Security fixes are applied to the latest release only.

| Version | Supported          |
|---------|--------------------|
| 4.5.x   | ✅                 |
| < 4.5   | ❌ Upgrade required |

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

Sentinel runs in your Kubernetes cluster with elevated RBAC (it can read pods, logs, events, and — if you enable it — secrets). The primary attack surfaces are:

1. **Prompt injection via pod logs**: An attacker who can write to a watched pod's logs could trick the LLM into generating malicious code. Sentinel defends against this with:
   - 18-pattern security scanner (`internal/agents/security.go`) that blocks dangerous APIs (`os.system`, `eval`, `pickle.loads`, `curl|bash`, etc.)
   - A separate reviewer-agent LLM call that audits the fix proposer's output for prompt-injection indicators
   - The fix is always applied via a PR — never directly to production — so you have a human review gate

2. **GitHub token exfiltration**: The fix proposer has access to your GitHub token. Sentinel defends against this by:
   - Never logging the token
   - Never passing it to the LLM as context
   - The security scanner blocks any fix that tries to exfiltrate tokens (`requests.post`, `urllib.request.urlopen` to non-GitHub hosts, etc.)

3. **Sandbox escape**: The sandbox namespace has a default-deny NetworkPolicy. The canary pod runs with a forced security context (non-root, read-only root filesystem, no privilege escalation).

4. **RBAC escalation**: Sentinel's RBAC is scoped to the namespaces you specify in `sentinel.rbac.writeNamespaces`. It does NOT have cluster-admin.

## What Sentinel cannot defend against

- A compromised LLM provider (OpenAI, Anthropic, etc.) that returns malicious completions despite your prompt — the security scanner catches known dangerous patterns but cannot detect novel attacks
- A compromised Kubernetes control plane
- A compromised GitHub account with write access to your repo (Sentinel's PRs would be indistinguishable from a malicious PR)

## Hardening recommendations

- Run Sentinel with `sentinel.rbac.writeNamespaces={sim}` — restrict write access to a single sandbox namespace
- Set `sentinel.apiToken` — protect the admin API (trigger/pause/resume/config)
- Set `sentinel.sandboxRequired=true` — refuse to open PRs that haven't passed sandbox verification
- Set `sentinel.autoMerge.enabled=false` initially — let humans review every PR for the first few weeks
- Audit the `sentinel-ai-bot[bot]` GitHub App's permissions quarterly

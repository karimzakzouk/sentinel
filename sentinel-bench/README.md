# Sentinel-Bench

A public benchmark for autonomous AI SRE agents.

Sentinel-Bench measures the thing that actually matters: **can the agent
autonomously detect a production incident, diagnose the root cause, write a
verified fix, and open a mergeable PR — without a human in the loop?**

No other public benchmark measures this. SWE-bench evaluates coding agents
on GitHub issues with known patches. K8sGPT and Komodor evaluate nothing
publicly — they only suggest fixes, they don't apply them. Sentinel-Bench is
the first benchmark that captures the full detect → diagnose → fix → verify
→ merge loop on real Kubernetes failure modes.

## Methodology

### Scenario format

Each scenario is a directory under `scenarios/` containing:

```
scenarios/
  S001-crashloop-image-typo/
    scenario.yaml      # metadata + setup + assertion
    cluster/           # kubectl-apply manifests that produce the broken state
    repo/              # the source repo the agent may clone and fix
    ground-truth.diff  # the human-verified correct patch
    evidence.md        # narrative: what broke, why, what the fix should be
```

### Scenario taxonomy

Scenarios are tagged along four axes:

| Axis | Values |
|---|---|
| `category` | crashloop, oom, config, network, rbac, perf, security, dependency, scaling, cert |
| `severity` | sev1, sev2, sev3 |
| `difficulty` | trivial, moderate, hard, adversarial |
| `origin` | real (from public postmortems), synthetic, mutated-real |

Target distribution: ~60% real, ~30% synthetic, ~10% mutated-real. Adversarial
scenarios contain deliberate red herrings (e.g. a misleading error log that
points to the wrong service) to test that the agent investigates rather than
pattern-matches.

### Evaluation protocol

For each scenario:

1. **Setup.** A clean KinD cluster is created. `cluster/*.yaml` is applied.
   The agent is given: cluster access (read-only + one write namespace for
   sandbox), the source repo URL, and a Slack/PagerDuty-style alert payload.
2. **Run.** The agent runs autonomously. Wall-clock budget: 15 min.
   LLM token budget: 200k. Cost budget: $2.
3. **Assert.** The agent must:
   - Open a PR against the source repo (not just print a diff)
   - The PR must pass CI (lint + tests)
   - The PR must apply cleanly to `main`
   - The PR diff must achieve >=80% structural similarity to `ground-truth.diff`
     (measured by tree-edit-distance on the AST of changed hunks)
   - (Optional, for full credit) The PR must be auto-merged by Sentinel's
     own 11-gate policy
4. **Score.** Binary pass/fail per scenario. Partial credit for:
   - Correct root-cause identification without a fix: 0.25
   - Correct fix proposal without verification: 0.50
   - Verified fix without PR: 0.75
   - Verified fix with mergeable PR: 1.00

### Metrics

Reported as four numbers:

- **DBR** — Detection-to-Branch Rate: % of scenarios where the agent opened
  any PR within the time budget.
- **VPR** — Verified-Pass Rate: % of scenarios where the PR passed CI.
- **TAR** — Truth-Aligned Rate: % of scenarios where the PR diff achieved
  >=80% structural similarity to ground truth.
- **FMR** — Full-Merge Rate: % of scenarios where Sentinel's own auto-merge
  policy approved the PR. FMR <= VPR <= TAR <= DBR always.

The headline number is **FMR**. It's the only one that means "the agent
fixed it, end-to-end, by its own standards."

### Reproducibility

- All scenarios are version-pinned in `scenarios/MANIFEST.yaml`.
- The runner uses deterministic LLM seeding (temperature=0, seed=42) for
  reproducibility across runs.
- Each run emits a signed attestation (see `internal/attest`) including
  model, prompt hash, token count, cost, and per-step decisions.
- Results are committed under `results/<sentinel-version>/<date>/` as JSON
  + a human-readable markdown summary.

## Current results

| Sentinel version | Scenarios | DBR | VPR | TAR | FMR |
|---|---|---|---|---|---|
| 5.13.0 (pre-bench) | -- | -- | -- | -- | -- |

The first official run will be published with the v5.14.0 release.

## Contributing a scenario

We especially want:

- **Real postmortems converted to scenarios.** If your team has published a
  postmortem (with redacted identifiers), open a PR adding it as a scenario
  under `scenarios/Sxxx-<slug>/`. Tag it `origin: real` and link the
  postmortem URL in `evidence.md`.
- **Adversarial scenarios.** Scenarios with misleading signals that test
  whether the agent investigates vs. pattern-matches.
- **Non-trivial-Go scenarios.** Currently over-indexed on Python and Node;
  we want Go, Rust, Java, Elixir.

## Why this matters

The 2026 AI-SRE market is crowded with vendors making unverified claims.
Sentinel-Bench is the public, reproducible bar. If a vendor's agent can't
be run against this benchmark, their marketing is fiction.

Sentinel is the only agent today with a detect->fix->verify->merge loop. The
benchmark exists to keep us honest about how often that loop actually closes.

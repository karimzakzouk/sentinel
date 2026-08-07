# Sentinel-Bench

A public benchmark for autonomous AI SRE agents.

Sentinel-Bench measures the thing that actually matters: **can the agent
autonomously detect a production incident, diagnose the root cause, write a
verified fix, and open a mergeable PR — without a human in the loop?**

No other public benchmark measures this. SWE-bench evaluates coding agents
on GitHub issues with known patches. K8sGPT and Komodor evaluate nothing
publicly — they only suggest fixes, they don't apply them. Sentinel-Bench is
the first benchmark that captures the full detect -> diagnose -> fix ->
verify -> merge loop on real Kubernetes failure modes.

## Repository layout

```
sentinel-bench/
  README.md                       # this file
  scenarios/
    MANIFEST.yaml                 # pinned list of scenarios + ground-truth SHA256s
    S001-crashloop-image-typo/
      scenario.yaml               # metadata + setup + trigger + assertions
      cluster/                    # kubectl-apply manifests that produce the broken state
      ground-truth.diff           # the human-verified correct patch
      evidence.md                 # narrative: what broke, why, what the fix should be
    S002-python-keyerror-env-var/
      ...
    S003-go-nil-pointer-panic/
      ...
  results/                        # runner output (one directory per Sentinel version)
    <sentinel-version>/<date>/
      results.json                # machine-readable
      summary.md                  # human-readable
```

The runner binary lives at `cmd/sentinel-bench/`. It is a standalone Go
program that talks to Sentinel via HTTP only (no `internal/*` imports
except `internal/logging` and `internal/version`), so it can be built and
shipped independently of the Sentinel server.

## Scenario format

Each scenario is a directory under `scenarios/` containing:

| File | Purpose |
|---|---|
| `scenario.yaml` | Metadata, setup, trigger payload, assertions |
| `cluster/` | kubectl-apply manifests that produce the broken state in KinD |
| `ground-truth.diff` | The human-verified correct patch (one fix per scenario) |
| `evidence.md` | Narrative: what broke, why, what the fix should be |

### Scenario taxonomy

Scenarios are tagged along four axes:

| Axis | Values |
|---|---|
| `category` | crashloop, oom, config, network, rbac, perf, security, dependency, scaling, cert |
| `severity` | sev1, sev2, sev3 |
| `difficulty` | trivial, moderate, hard, adversarial |
| `origin` | real (from public postmortems), synthetic, mutated-real |

Target distribution: ~60% real, ~30% synthetic, ~10% mutated-real.
Adversarial scenarios contain deliberate red herrings (e.g. a misleading
error log that points to the wrong service) to test that the agent
investigates rather than pattern-matches.

## Evaluation protocol

For each scenario:

1. **Setup.** A clean KinD cluster is created (or `--cluster` points at an
   existing one). `cluster/*.yaml` is applied. Sentinel is given: cluster
   access (read-only + one write namespace for sandbox), the source repo
   URL, and a Slack/PagerDuty-style alert payload.
2. **Run.** The runner calls Sentinel's `POST /admin/investigate` with the
   scenario's alert payload. Sentinel runs autonomously. Wall-clock budget:
   15 min (overridable per-scenario in `scenario.yaml` and via `--timeout`).
3. **Assert.** The runner polls Sentinel's `GET /incidents` endpoint until
   the incident reaches a terminal state or the budget elapses, then checks
   four assertions per scenario:
   - `pr_opened` — did Sentinel open a PR against the source repo?
   - `pr_ci_green` — did the PR's CI (lint + tests) pass?
   - `diff_similarity` — does the PR diff achieve >=80% line-recall vs
     `ground-truth.diff`? (See "Diff similarity metric" below.)
   - `sentinel_automerge_approved` — did Sentinel's own auto-merge policy
     approve the PR?
4. **Score.** Binary pass/fail per assertion, weighted per
   `scenario.yaml`. Partial credit per scenario is the weighted sum.

### Metrics

Reported as four numbers across all scenarios:

- **DBR** — Detection-to-Branch Rate: % of scenarios where the agent opened
  any PR within the time budget.
- **VPR** — Verified-Pass Rate: % of scenarios where the PR passed CI.
- **TAR** — Truth-Aligned Rate: % of scenarios where the PR diff achieved
  >=80% line-recall vs ground truth.
- **FMR** — Full-Merge Rate: % of scenarios where Sentinel's own auto-merge
  policy approved the PR. FMR <= VPR <= TAR <= DBR always.

The headline number is **FMR**. It's the only one that means "the agent
fixed it, end-to-end, by its own standards."

### Diff similarity metric

Line recall, not AST tree-edit-distance — v1 keeps the metric simple so the
runner stays under 1,000 LOC.

For each diff, collect the multiset of changed lines (lines starting with
`+` or `-`, excluding `+++` / `---` file headers). Line recall is the
multiset intersection size divided by the ground-truth multiset size:

```
                sum over distinct lines L of min(prCount[L], gtCount[L])
lineRecall = ----------------------------------------------------------------
                          sum over distinct lines L of gtCount[L]
```

- Identical PR diff and ground truth -> 1.0.
- Disjoint diffs -> 0.0.
- Empty ground truth -> 0.0 (the scenario is malformed).
- Threshold for the `diff_similarity` assertion: 0.80.

The metric is robust to extra whitespace in the PR diff (the metric
compares whole lines including the `+`/`-` marker) but does NOT normalize
indentation — a fix that rewrites the function with different indentation
will score low. That's intentional: the ground truth is the minimal patch.

## The runner

Build:

```
go build -o /tmp/sentinel-bench ./cmd/sentinel-bench
```

Commands:

```
sentinel-bench list                                  # list all scenarios from MANIFEST.yaml
sentinel-bench validate                              # verify structure + check ground-truth SHA256s
sentinel-bench run [--scenario S001] [--timeout 15m] # run Sentinel against scenarios
sentinel-bench results <version>                     # show historical results
```

Flags:

```
--server URL          Sentinel API URL (default: http://localhost:8000)
--token T             Bearer token for /admin/* (or SENTINEL_API_TOKEN env var)
--cluster NAME        Use an existing KinD cluster instead of creating one
--scenario ID         Run only the named scenario (repeatable)
--timeout D           Per-scenario wall-clock budget (default: 15m)
--scenarios-dir PATH  Path to scenarios/ (default: ./sentinel-bench/scenarios)
--results-dir PATH    Path to results/ (default: ./sentinel-bench/results)
--json                Output raw JSON instead of tables
```

The runner uses `os/exec` to call `kind` and `kubectl` (not the Go K8s
client — keeps it simple, keeps the binary small). The runner talks to
Sentinel via HTTP only.

## Reproducibility

- All scenarios are version-pinned in `scenarios/MANIFEST.yaml` with the
  SHA256 of each `ground-truth.diff`. `sentinel-bench validate` recomputes
  the hash and refuses to run if a hash drifts (someone edited the ground
  truth without updating the manifest).
- Sentinel's LLM calls use `temperature=0, seed=42` for reproducibility
  across runs (set in Sentinel's config, not the runner's).
- Each run emits a results JSON + markdown summary under
  `results/<sentinel-version>/<date>/`.

## Current results

| Sentinel version | Scenarios | DBR | VPR | TAR | FMR |
|---|---|---|---|---|---|
| dev (pre-bench)  | 3 | -- | -- | -- | -- |

The first official run will be published with the v1 release.

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

To add a scenario:

1. `mkdir sentinel-bench/scenarios/Sxxx-<slug>`
2. Write `scenario.yaml` (copy an existing one as a template).
3. Write `cluster/*.yaml` (kubectl-apply manifests that produce the broken
   state in KinD).
4. Write `ground-truth.diff` (the human-verified correct patch).
5. Write `evidence.md` (narrative: what broke, why, what the fix should be).
6. `sha256sum sentinel-bench/scenarios/Sxxx-<slug>/ground-truth.diff`
7. Add an entry to `sentinel-bench/scenarios/MANIFEST.yaml` with the hash.
8. `go run ./cmd/sentinel-bench validate` — must pass.

## Why this matters

The 2026 AI-SRE market is crowded with vendors making unverified claims.
Sentinel-Bench is the public, reproducible bar. If a vendor's agent can't
be run against this benchmark, their marketing is fiction.

Sentinel is the only agent today with a detect->fix->verify->merge loop. The
benchmark exists to keep us honest about how often that loop actually closes.

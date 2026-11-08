# Status

Last updated: Sunday 8 November 2026, 21:40 Cairo time (UTC+2).

## Where things stand

**Phase 0 (Foundations) is done and tagged `v2.0.0-alpha.0`.** Every gate
condition is proven in CI on GitHub. Phase 1 (Observe: the Watcher and the
Brain in shadow mode) is next.

## Phase 0 gate

| Gate condition | Result | Where it is proven |
|---|---|---|
| CI injects a scenario on kind and records ground truth | Pass | `TestScenarioInjectionRecordsGroundTruth` in the E2E workflow |
| The capability report is correct on kind, k3s and minikube | Pass on five cluster types: kind with kindnet, Calico and Flannel; k3s; minikube | E2E, E2E network plugins, and Capability report across distributions workflows |
| A test proves the fence blocks a forbidden write | Pass: eleven writes, each allowed or refused as designed, and each refusal checked to come from the fence | `TestFenceBlocksForbiddenWrites` |

## What Phase 0 built

| Item | Proof |
|---|---|
| Go module, layout, lint, unit tests, vulnerability scan, release tooling | `make lint vuln test build`; CI on every push |
| Core data model and the event-sourced, hash-chained Ledger on SQLite | Unit tests: tamper detection, restart continuity, all-or-nothing appends, concurrent writers |
| Slots: champion and shadow engines, typed probability contracts, record-or-refuse, replay | Unit tests, including shadows that fail or panic without affecting the decision |
| Capability detection and report, with an active NetworkPolicy probe | `sentinel capabilities`; checked on five cluster types |
| Charts: sentinel-crds, sentinel (least privilege, Lab namespace pool), sentinel-fence | End-to-end fence test |
| Harness: kind, Calico and Flannel setups, 11 scenarios (6 smoke, 5 Online Boutique), injector, ground truth | All scenarios run and reverted on a live cluster |
| Porting v1 code | Moved to the phases that use it (decision 12) |

## Things building found that the plan got wrong

- **Whether a cluster enforces NetworkPolicy cannot be read from its
  configuration.** The same kind cluster enforces policy on GitHub's runners
  and silently ignores it on the development VM, because kind's network plugin
  needs a kernel feature the VM lacks. The active probe answered correctly in
  both places, and on five cluster types in CI.
- **Policy controllers program new pods with a delay.** On k3s, a brand-new
  client pod was refused even by an allow policy. The probe now retries its
  control connection from one pod; a probe that started fresh pods for every
  retry would have reported k3s's networking as broken.
- **Probes must run where their component would run.** Testing the node agent
  in the locked-down Lab namespace made it fail for the wrong reason.
- **The development VM's kernel lacks pieces several network setups need**
  (ipset, NFQUEUE, the iptables statistic match, VXLAN). The test setups now
  avoid them, and CI covers the rest.
- **Porting v1 code in Phase 0 would ship it unused.** It is ported when a
  phase needs it, with its known bugs fixed.

Full reasoning in [decisions.md](decisions.md).

## Next: Phase 1 (Observe)

The Watcher and the Brain, running in shadow mode: they record what Sentinel
would have done, and change nothing.

- Tier-0 collection: trimmed informers, persisted events, log budget, deduplication.
- Log pipeline: multiline assembly, redaction, template mining, exception
  fingerprints, a new-error-after-deploy test.
- Adaptive detectors: count baselines, seasonal residuals with calibrated
  tails, false-discovery control, persistence and went-away checks, new-vs-old
  rollout comparison, cold start from sibling replicas.
- Dependency map, incident grouping, root-cause retrieval and reranking,
  change attribution.
- Shadow report, a read-only incident timeline UI, and the CLI.

Gate: detection precision and recall and localization accuracy on the
development fault pool, and false alarms per service per week in a 72-hour
noisy soak.

## Waiting on you

Nothing blocks Phase 1. When you have time:

- **Click Save on the Environment card** when it appears in the chat. It makes
  future cloud agents start with the toolchain ready.
- **Review the layout and data model** using [guides/phase-0.md](guides/phase-0.md).
  The plan calls them the hardest things to change later.

Needed later:

| When | What |
|---|---|
| Phase 1 gate | A machine that can run a 72-hour soak (the self-hosted runner below would do) |
| Phase 3 | A bigger machine (about 8 CPUs, 32 GB) as a self-hosted CI runner for the Lab's statistics suites |
| Phase 4 | A test GitHub organization and a GitHub App, so Sentinel can open real pull requests |
| Phase 6 | Azure for Students credits (managed-cluster run) and outside human evaluators |

## How to check

- **History:** commits on `main`; milestones are tagged `v2.0.0-alpha.N`.
- **CI:** the Actions tab. `CI` runs on every push, `E2E` on code changes, and
  `E2E network plugins` and `Capability report across distributions` when the
  probe's code changes and weekly.
- **Run it yourself:** `make e2e-up && make e2e` brings up a kind cluster with
  Sentinel and runs the end-to-end suite.

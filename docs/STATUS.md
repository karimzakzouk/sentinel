# Status

Last updated: Sunday 8 November 2026, 21:05 Cairo time (UTC+2).

## Where things stand

**Phase 0 (Foundations) is built and passes its gate locally.** CI on GitHub is
confirming it; when the end-to-end runs are green, the commit gets the
`v2.0.0-alpha.0` tag and Phase 1 (Observe) starts.

## Phase 0 checklist

| Item | State | Proof |
|---|---|---|
| Go module, layout, lint, unit tests, release tooling | Done | `make lint test build vuln`; CI on every push |
| Core data model and event-sourced Ledger on SQLite | Done | Unit tests: tamper detection, restart continuity, all-or-nothing appends, concurrent writers |
| Slot interfaces, engine registry, shadow runs, replay | Done | Unit tests, including shadows that fail or panic without affecting the decision |
| Capability detection and report | Done | `sentinel capabilities`; end-to-end tests as Sentinel's own identity |
| Charts: sentinel-crds, sentinel, sentinel-fence | Done | End-to-end fence test: eleven writes, each allowed or refused as designed |
| Test harness: kind setups, scenarios, injector, ground truth, CI | Done | 11 scenarios (6 smoke, 5 Online Boutique), all run and reverted on a live cluster |
| Port reusable v1 code | Moved | Each piece is ported in the phase that uses it (decision 12) |

## Phase 0 gate

| Gate condition | Result |
|---|---|
| CI injects a scenario on kind and records ground truth | Passes (`TestScenarioInjectionRecordsGroundTruth`) |
| The capability report is correct on kind | Passes on kindnet, Flannel and Calico; k3s and minikube not yet checked |
| A test proves the fence blocks a forbidden write | Passes (`TestFenceBlocksForbiddenWrites`) |

## Things building found that the plan got wrong

- **NetworkPolicy enforcement depends on the machine, not the cluster's
  configuration.** The same kind cluster enforces policy on GitHub's runners
  and silently ignores it on the development VM. The active probe answered
  correctly on both, and the Lab stays off wherever enforcement is not proven.
- **Probes must run where their component would run.** Testing the node agent
  in the locked-down Lab namespace made it fail for the wrong reason.
- **Kernel features matter for the test clusters too.** The VM's kernel lacked
  pieces that Calico, iptables-mode kube-proxy and Flannel's default VXLAN
  backend need; the test setups now avoid them, and Calico runs in CI.
- **Porting v1 code in Phase 0 would ship it unused.** It is ported when a
  phase needs it, with its known bugs fixed.

Full reasoning in [decisions.md](decisions.md).

## Waiting on you

Nothing blocks Phase 1. When you have time:

- **Click Save on the Environment card** when it appears in the chat. It makes
  future cloud agents start with the toolchain ready.
- **Review the layout and data model** using [guides/phase-0.md](guides/phase-0.md).
  The plan calls them the hardest things to change later.

Needed later:

| When | What |
|---|---|
| Phase 3 | A bigger machine (about 8 CPUs, 32 GB) as a self-hosted CI runner for the Lab's statistics suites |
| Phase 4 | A test GitHub organization and a GitHub App, so Sentinel can open real pull requests |
| Phase 6 | Azure for Students credits (managed-cluster run) and outside human evaluators |

## How to check

- **History:** commits on `main`.
- **CI:** the Actions tab. `CI` runs on every push, `E2E` on code changes,
  `E2E network plugins` when the probe's code changes and weekly.
- **Run it yourself:** `make e2e-up && make e2e` brings up a kind cluster with
  Sentinel and runs the end-to-end suite.

# Status

Last updated: Monday 9 November 2026, 00:40 Cairo time (UTC+2).

## Where things stand

**Phase 0 (Foundations) is done and tagged `v2.0.0-alpha.0`.** Phase 1
(Observe: the Watcher and the Brain in shadow mode) is under way. Slice 1
(Kubernetes facts into explained incidents) and slice 2 (logs) are done:
Sentinel now also catches a deploy whose pods stay up but start logging
errors, which Kubernetes itself cannot see. It records what it would do and
changes nothing.

## Phase 1 progress

| Slice | State | Proof |
|---|---|---|
| 1. Kubernetes facts → signals → incidents | Done | Smoke pool 6 of 6; Online Boutique below; `TestBadImageScenario` in CI |
| 2. Logs: evidence, redaction, new errors after a deploy | Done | `TestLeakedSecretsNeverReachTheLedger` in CI; the frontend scenario below |
| 3. Metrics, dependency map, localization | Next | |
| 4. Rate detectors | | |
| 5. Shadow report, CLI, read-only UI | | |
| 6. Scoring (`harness score`) | | |

## Scenario results

Each scenario is injected on the development cluster, observed, and reverted.
Sentinel's answer is what it logged: the fault class, the change it blames
and the action it would take.

| Scenario | Sentinel's answer | Right? |
|---|---|---|
| Smoke: bad image | bad image (1.00), blames `image:web` (0.97), roll back | Yes |
| Smoke: memory limit too low | memory limit (0.999), blames `resources:web` (0.98), roll back | Yes |
| Smoke: crash on start | crash on start (0.85), blames `args:web` (0.96), roll back | Yes |
| Smoke: bad readiness probe | probe (0.80, then 0.997), blames `probes:web` (0.98), roll back | Yes |
| Smoke: a pod deleted | nothing | Yes |
| Smoke: control | nothing | Yes |
| Boutique: cart image missing | bad image (1.00), blames `image:server` (0.985), roll back | Yes |
| Boutique: currency service crashes | crash on start (0.78), blames `env:server` (0.97), roll back | Yes |
| Boutique: product catalog memory | missed | No: one OOM kill every two minutes never puts two in the one-minute window the development cluster uses (decision 27) |
| Boutique: Redis scaled to zero | missed | Expected: the errors show in other services, not in Redis; slice 3's dependency map is for this |
| Boutique: control | nothing | Yes |
| Boutique: frontend's catalog address misspelled (no pod fails; every page logs an error) | new errors after a deploy (0.997), blames `env:server` (0.93), roll back; the new pods' error attached as evidence | Yes, from logs alone: the new revision's error rate against the old one's, p = 1.2×10⁻⁷ |

## Things building found

- **Kubernetes 1.37 shows a crash loop differently.** A crashing container
  spends its back-off as "terminated", not "CrashLoopBackOff", so crash loops
  went unnoticed until both Sentinel and the test harness learned the new
  shape.
- **One wrong signal can dominate.** Old readiness failures counted as new
  after a restart; a revert took the blame for the incident it fixed; an OOM
  incident's crash loops taught the baseline that crash loops were normal; a
  single OOM kill, too rare to alarm alone, was invisible to the classifier.
  Each is fixed with a regression test (decisions 18 to 23).
- **Logs need their own permission.** A cluster that denied `pods/log` lost
  all detection; logs are now a separate capability.
- **Secrets hide everywhere in logs.** The redactor catches every one of 46
  canary secrets in realistic lines, leaves stack traces and Kubernetes
  names alone, runs at 20 MB/s, and a second pass never changes its output.
  Fuzzing found two real bugs in it before they shipped.
- **Slow faults need longer windows.** See the product catalog row.

Full reasoning in [decisions.md](decisions.md).

## Next

Slice 3: metrics (metrics-server), a dependency map from configuration and
logs, and localization, so the Redis scenario is blamed on Redis.

## Waiting on you

Nothing blocks Phase 1. When you have time:

- **Click Save on the Environment card** if it is still there. It makes
  future cloud agents start with the toolchain ready.
- **Review the layout and data model** using [guides/phase-0.md](guides/phase-0.md).

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
  Sentinel and runs the end-to-end suite; `hack/run-scenarios.sh
  test/scenarios/smoke/*.yaml` runs the scenario pool against it.

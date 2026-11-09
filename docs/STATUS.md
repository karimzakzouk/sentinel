# Status

Last updated: Monday 9 November 2026, 04:45 Cairo time (UTC+2).

## Where things stand

**Phase 0 (Foundations) is done and tagged `v2.0.0-alpha.0`.** Phase 1
(Observe) has all six slices built at least in their core; its gate still
needs the 72-hour soak. **Phase 2 (Act) is under way:** where a namespace's
SentinelPolicy allows it, Sentinel now stops the bleeding itself. In the
scored pool it rolled back or restored every fault, each verified, a
median of 69 seconds after the fault appeared, with no wrong action
(below). Without a policy it still only records what it would do.

To look at it on a cluster where it runs:

- `kubectl -n sentinel port-forward deploy/sentinel 8090`, then open
  <http://localhost:8090>: incidents, why Sentinel blames what it blames,
  what it did or would do, every action it took, and what it can do on
  this cluster.
- `kubectl -n sentinel exec deploy/sentinel -- /sentinel incidents`, and
  `... /sentinel report shadow --since 24h` for what it would have done.
- To let it act in a namespace, a namespace admin creates a SentinelPolicy
  (for example `rollout.undo` and `scale` at `autonomous`, or at `approve`,
  then `sentinel approve NAMESPACE/REQUEST` for each one). For workloads
  Argo CD or Flux keeps in sync, `argocd.rollback` or `flux.rollback` lets
  Sentinel pause the owner first; `sentinel resume` undoes the pause once
  Git is fixed.

## Phase 1 progress

| Slice | State | Proof |
|---|---|---|
| 1. Kubernetes facts → signals → incidents | Done | Scored session below; `TestBadImageScenario` in CI |
| 2. Logs: evidence, redaction, new errors after a deploy | Done | `TestLeakedSecretsNeverReachTheLedger` in CI; the frontend scenario below |
| 3. Metrics, dependency map, localization | Done | The Redis and product-catalog memory scenarios below |
| 4. Rate detectors | Error rates done; seasonal detectors wait for the soak | In the Redis scenario, the cart service's and the frontend's errors join Redis's incident |
| 5. Shadow report, CLI, read-only UI | Done | `TestTheUIAnswersThroughPortForward` in CI |
| 6. Scoring (`harness score`) | Done | The sessions below |

## Phase 2 progress

| Slice | State | Proof |
|---|---|---|
| 1. Act where a policy allows: the actor, requests, verification | Done | The acting session below; `TestAssistModeRollsBackABadImage` in CI |
| 2. Guards | Budget, kill switch, capacity cap, breaker done | Unit tests; the fence test in CI |
| 3. Approval, veto and notices | Done | `TestAnApprovedRollbackRunsAndNamesItsApprover`; the fence refuses an approval in someone else's name; Slack notices when a webhook is set |
| 4. Owner adapters | Argo CD and Flux Kustomizations done; Flux HelmReleases left to people (decision 45) | `TestArgoCDRollbackPausesTheApplication` and `TestFluxRollbackSuspendsTheKustomization` in the E2E owners workflow |
| 5. Track records and autonomy | Not started | |

## Phase 3 progress

| Slice | State | Proof |
|---|---|---|
| 0. An enforcing development cluster | Not possible on the development VM (decision 46); Lab tests run in CI | The network plugins workflow, now with Cilium |
| 1. Seal and prove | Done, except the per-Lab DNS, teardown re-check and reaper, which move to slice 2 with the workloads that need them. The Lab runs as its own fenced identity (decision 48) and fails closed where enforcement is not proven | `TestTheLabSealHoldsOnlyWherePolicyIsEnforced`: on Flannel the matrix caught 16 leaks, as it must; in CI's enforcing clusters none may leak. `TestALabExperimentRunsOnlyBehindAProvenSeal`: on Flannel the Lab stays off and says why; in enforcing clusters a `prove` Experiment passes, and fails once an extra policy opens the namespace. Seven Lab cases in `TestFenceBlocksForbiddenWrites`. In CI, Calico and Cilium refused every forbidden destination; kindnet let Lab pods reach cloud metadata, so the Lab refuses it there (decision 49) |
| 2. Synthesize the stateless world | The world built and proven: synthesis, the Lab DNS (decision 51), production's images under the fence's bounds (decision 50), the seal proven before the world starts and again with it running; Online Boutique waits for the self-hosted runner | `TestTheLabBuildsAWorkloadsStatelessWorld`, as the Lab's identity: a front and the backend it calls answer by their production names, the API server's name resolves to nothing, and a canary Secret never reaches the Lab. `TestALabReproductionRunsTheWorldBehindAProvenSeal` in CI's enforcing clusters |
| 3. Reproduce and judge | Races and verdicts built (decisions 52 and 53): reproduce and verify-fix Experiments, the previous revision as the first candidate, a deletion control; the gate's repetitions wait for the self-hosted runner | `TestTheLabTellsAFixFromTheFailure` on the development cluster: against a front that crashes as it starts, its previous revision passed after 15 blocks (15 of 15 against 0 of 15), the deletion control failed. In CI's enforcing clusters `TestTheLabVerifiesTheRollbackOfACrashingRevision` runs the same through the Lab. Unit simulations: 3.4% false PASS at the null's edge for α = 5% |
| 4. Datastores and replay | Not started | |

## Acting session

The same twelve scenarios, with a SentinelPolicy allowing rollbacks and
replica restores in both namespaces. Sentinel detected each fault, asked
its actor, which undid the blamed change, and verified that the incident's
signals cleared:

| Scenario | Detected | Sentinel did | Recovered (fault to verified) |
|---|---|---|---|
| Smoke: bad image | 7s | rolled back | 1m7s |
| Smoke: memory limit too low | 32s | rolled back | 1m32s |
| Smoke: crash on start | 9s | rolled back | 1m9s |
| Smoke: bad readiness probe | 2s | rolled back | 1m7s |
| Smoke: a pod deleted (heals itself) | nothing, rightly | nothing | |
| Smoke: control | nothing, rightly | nothing | |
| Boutique: cart image missing | 4s | rolled back | 1m4s |
| Boutique: product catalog memory | 39s | rolled back | 1m44s |
| Boutique: currency service crashes | 0s | rolled back | 59s |
| Boutique: Redis scaled to zero | 9s | restored the replicas | 1m44s |
| Boutique: frontend's catalog address misspelled | 1m4s | rolled back | 1m9s |
| Boutique: control | nothing, rightly | nothing | |

9 of 9 faults acted on and verified, no wrong action, no false alarm. One
thing scoring caught: right after the product catalog's rollback, Sentinel
weighed its own rollback as a possible cause and its decision turned to
"escalate"; its own actions are no longer candidate causes. Recovery time
includes the minute the development cluster's detection window takes to
empty, which is when verification can tell the signals have ended.

## Scored session (observe only)

Twelve scenarios on the development cluster (kind, Kubernetes 1.37,
Flannel, Online Boutique and a smoke app), each injected, watched for 150
seconds and reverted, then scored by `harness score` against each scenario's
hidden ground truth. Every incident counts once; nothing is adjusted.

| Scenario | Detected | Time to detect | Class | Root cause rank | Would do |
|---|---|---|---|---|---|
| Smoke: bad image | yes | 8s | bad image ✓ | 1 | roll back ✓ |
| Smoke: memory limit too low | yes | 0s | memory limit ✓ | 1 | roll back ✓ |
| Smoke: crash on start | yes | 5s | crash on start ✓ | 1 | roll back ✓ |
| Smoke: bad readiness probe | yes | 1s | broken health check ✓ | 1 | roll back ✓ |
| Smoke: a pod deleted (heals itself) | nothing, rightly | | | | |
| Smoke: control | nothing, rightly | | | | |
| Boutique: cart image missing | yes | 7s | bad image ✓ | 1 | roll back ✓ |
| Boutique: product catalog memory | yes | 0s | memory limit ✓ | 1 | roll back ✓ |
| Boutique: currency service crashes | yes | 4s | crash on start ✓ | 1 | roll back ✓ |
| Boutique: Redis scaled to zero | yes | 7s | dependency unavailable ✓ | 1 | restore the replicas ✓ |
| Boutique: frontend's catalog address misspelled | yes | 1m5s | new errors after a deploy ✓ | 1 | roll back ✓ |
| Boutique: control | nothing, rightly | | | | |

Detection 9 of 9, precision 1.00, the root cause ranked first every time,
class, blamed change and decision right every time, a median of 5 seconds
to detect, and no false alarm in the three quiet runs.

**Read this with care.** These are the scenarios the engines were tuned
against, so a perfect score shows that nothing regressed, not that Sentinel
generalizes. Twelve runs give wide intervals, and the misspelled address
takes a minute because only a rollout comparison sees it. The held-out pool
and outside evaluators (phase 6) are the honest test.

Since the session, error sampling (decisions 36 to 38) also saw the Redis
fault where it hurts: rerun, the cart service's errors joined Redis's
incident five seconds after it opened, and the frontend's under two
minutes later, instead of opening incidents of their own.

## Things building found

- **Kubernetes 1.37 shows a crash loop differently.** A crashing container
  spends its back-off as "terminated", not "CrashLoopBackOff". Sentinel
  handles both, and CI also runs the end-to-end suite on Kubernetes 1.34.
- **One wrong signal can dominate.** Old readiness failures counted as new
  after a restart; a revert took the blame for the incident it fixed; an OOM
  incident's crash loops taught the baseline that crash loops were normal.
  Each is fixed with a regression test (decisions 18 to 23).
- **Error logs need their own statistics.** Crash counts are near zero, so
  any is news; error volumes are a workload's own, large and bursty. A test
  that is right for one alarms on routine noise in the other, so error
  series learn their level first, need a doubling as well as significance,
  and count the seconds of logs read rather than the clock (decision 36).
- **Every logging library is different.** .NET's console logger puts the
  message on the line after the level, so the cart service's every Redis
  failure looked like an information line until Sentinel learned the format.
- **A rare-event computation spun.** The detector's tail probability summed
  terms that all underflowed, ran to its cap, and took 39 seconds in a test
  of a busy workload; it now takes a quarter of a second.
- **Scoring needs care too.** The first score counted the next scenario's
  incident as the control's false alarm, and a pod deletion that heals
  itself as a missed fault (decision 39).
- **Secrets hide everywhere in logs.** The redactor catches every one of 46
  canary secrets in realistic lines, leaves stack traces and Kubernetes
  names alone, and a second pass never changes its output.

Full reasoning in [decisions.md](decisions.md).

## Next

- **Phase 2:** track records and earned autonomy (slice 5); Flux
  HelmReleases; an acting session through Argo CD and Flux.
- **Soak.** A long run with the scenario pool's controls and background
  traffic, to measure false alarms per service per week, the error-rate
  detector's especially. Shorter runs on the development cluster first; the
  gate's 72 hours need the machine listed below.
- **The rest of slice 4,** shaped by the soak: seasonal baselines (SPOT on
  decomposed residuals) if daily cycles cause false alarms, and dependency
  edges from connection errors in logs.
- **Smaller items:** exception fingerprints of logged .NET exceptions
  split one error into several (a fingerprint version 2), and baselines are
  relearned after every restart (decision 35).

## Waiting on you

Nothing blocks Phase 1. When you have time:

- **Click Save on the Environment card** if it is still there. It makes
  future cloud agents start with the toolchain ready.
- **Try it** with [the guide](guides/try-it.md), on a VM rather than your
  laptop, and say what reads badly in the UI.

Needed later:

| When | What |
|---|---|
| Phase 1 gate | A machine that can run a 72-hour soak (the self-hosted runner below would do) |
| Phase 3 | A bigger machine (about 8 CPUs, 32 GB) as a self-hosted CI runner for the Lab's statistics suites |
| Phase 4 | A test GitHub organization and a GitHub App, so Sentinel can open real pull requests |
| Phase 6 | Azure for Students credits (managed-cluster run) and outside human evaluators |

## How to check

- **History:** commits on `main`; milestones are tagged `v2.0.0-alpha.N`.
- **CI:** the Actions tab. `CI` runs on every push, `E2E` on code changes,
  `E2E Kubernetes versions` on code changes and weekly, and `E2E network
  plugins` and `Capability report across distributions` when the probe's
  code changes and weekly.
- **Run it yourself:** `make e2e-up && make e2e` brings up a kind cluster with
  Sentinel and runs the end-to-end suite; `hack/run-scenarios.sh
  test/scenarios/smoke/*.yaml` runs the scenario pool against it, and
  `make score SINCE=<time>` scores the runs since then.

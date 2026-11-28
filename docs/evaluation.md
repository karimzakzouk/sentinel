# Evaluation

What has been measured, what has not, and where Sentinel is known to fail.
Raw session logs are in [STATUS.md](STATUS.md).

## Setup

kind with Kubernetes 1.37, one control-plane and one worker node, Flannel, on a
constrained development VM. Workloads: a small smoke app and Google's
[Online Boutique](https://github.com/GoogleCloudPlatform/microservices-demo).
Load was not controlled or recorded. GitOps runs used Argo CD v3.6.1 and Flux
v2.10.0.

**Detection.** Per workload and fact kind, a Gamma-Poisson count detector
compares the current window against a learned baseline, with the false-alarm
rate budgeted at one per week across all series. Fact kinds include crash
loops, OOM kills, image-pull errors, probe failures, not-ready pods, Services
losing endpoints, memory near limit and node pressure. After a rollout, a
separate test checks whether any log error template appears at more than twice
the old revision's rate.

**Pool.** 12 scenarios ([test/scenarios](../test/scenarios)): 9 faults, 2
controls and 1 deleted pod that heals itself (doing nothing is right for the
last three). Faults: bad image (×2), memory limit too low (×2), crash on start
(×2), bad readiness probe, Redis scaled to zero, and a misspelled service
address in the frontend's environment.

## Results

| | Result |
|---|---|
| Faults acted on and verified | 9 of 9 (8 rollbacks, 1 replica restore) |
| Wrong actions | 0 |
| False alarms on the 3 quiet runs | 0 |
| Time from fault to detection | median 9 s |
| Time from fault to verified recovery | median 69 s (range 59–104 s) |
| GitOps faults (Argo CD, Flux HelmRelease, Flux Kustomizations) | 4 of 4, median 66 s |

"Verified" means every signal of the incident ended within ten minutes of the
action. Runs are scored against each scenario's recorded ground truth with
`make score`.

**What this does and does not show.** I wrote every scenario, and the detectors
were tuned on these same 12, so a perfect score shows that nothing regressed,
not that Sentinel generalizes. About a minute of each recovery time is the test
cluster's one-minute detection window draining, not action latency. n = 12 on
one cluster is a smoke test. No baseline has been run yet.

## Baselines (planned, not yet run)

- **Argo Rollouts**: canary with background analysis on HTTP error rate
  (`interval: 1m`, `failureLimit: 2`, `progressDeadlineSeconds: 600`), with
  `progressDeadlineAbort` both off (the default) and on.
- **Always roll back (ARB)**: undo the newest revision if, within 10 minutes of
  it, any of its pods shows CrashLoopBackOff, an image-pull error, an OOM kill,
  or stays not-ready for over 60 s; 15-minute cooldown. Robusta-style playbooks
  implement similar rules.
- **No action**: default Kubernetes (restarts, progress deadline).

Only the Sentinel column below is measured; the others are expectations from
the tools' documentation.

| Scenario | Sentinel (measured) | Argo Rollouts (expected) | ARB (expected) | No action (expected) |
|---|---|---|---|---|
| Bad image | Rolled back, 64–67 s | No abort by default; ~600 s with abort on | Rolls back within seconds | Stuck; old pods keep serving |
| Crash on start | Rolled back, 59–69 s | Same as bad image | Rolls back in ~10–30 s | Stuck; old pods keep serving |
| Bad readiness probe | Rolled back, 67 s | Same as bad image | Rolls back after ~60 s | Stuck; old pods keep serving |
| Memory limit too low | Rolled back, 92–104 s | Same as bad image if OOM at start; aborts in ~3 min if OOM under load | Rolls back on first OOM | Restarts continue |
| Redis scaled to zero | Replicas restored, 104 s | No action (no rollout) | No action (scaling creates no revision) | Redis stays down |
| Misspelled address (runs, errors) | Rolled back, 69 s | Aborts in ~3 min if errors reach the metric | Misses it (no error signal) | Errors continue |
| Controls and self-healing pod | No action (correct) | Promotes | No action | No action |

On faults that keep new pods out of service, ARB should match or beat 69 s, and
default Kubernetes already protects users: the 25% `maxUnavailable` default
keeps old pods serving. Argo Rollouts' error-rate analysis cannot see these
faults because unready pods get no traffic; that is its design scope, not a
flaw. Sentinel's case rests on three things: runtime-error faults (its log
comparison between revisions), not rolling back when the change is not the
cause, and GitOps workloads, where Argo CD's self-heal re-applies a manual undo
within about 5 s. The baseline pool therefore needs scenarios that separate
these: errors without crashes, a dependency outage during an unrelated deploy,
and GitOps-managed faults.

The plan: same cluster, scenarios, injection times and fixed load, one
controller installed at a time, 3 repetitions per cell. Each run records time
to detection, time to verified recovery (same signals for all), false rollbacks
and user-visible errors. Sentinel will also be run on
[AIOpsLab](https://github.com/microsoft/AIOpsLab)'s problems, which removes the
"self-written scenarios" objection.

## Known failures

- **Lab datastores hold no production data**, only up to 20 redacted marker
  records when optional body capture is on, so server errors that may come from
  missing data are forgiven. A data-dependent bad fix can pass.
- **Isolation leak on kindnet.** Lab pods reached cloud metadata endpoints
  despite default-deny; a per-pod nftables seal now blocks egress, not ingress.
- **Lab gate evidence is thin.** 0 false passes in 30 races bounds the
  false-pass rate below about 9.5%, not the 5% target.
- **StatefulSet rollbacks always wait for a person.**
- **Flux resume conflict.** After a person resumed a Flux object, Sentinel
  refused later rollbacks and its breaker locked the workload for 24 hours
  (fixed).
- **The Fixer refuses ConfigMap/Secret values** and multi-value changes, where
  many real config faults live.
- **Slow faults are missed**: anything after the 10-minute revert window, and
  recurrences slower than the detection window.
- **DNS, certificate and connection-refused faults are only escalated**, never
  acted on.
- **No completed long-run soak**, so the real false-alarm rate is unknown.

# Phase 3 plan: the Lab

The Lab reproduces an incident in a sealed copy of the workload's
environment and judges candidate fixes there, with a verdict that states
its error bounds: PASS, FAIL or NOT SURE. It is the project's headline: no
fix reaches a cluster, and no LLM-drafted patch is believed, until the Lab
has judged it. The design is the Lab tab of `end-to-end.canvas.tsx`; this
plan orders it into vertical slices, each proven before the next starts.

## What phases 0 to 2 left ready

- A pool of Lab namespaces created at install (`lab.poolSize`), with
  default-deny NetworkPolicies, and the `Experiment` custom resource.
- The NetworkPolicy probe, which proves enforcement with live traffic and is
  the only thing that can enable the Lab (decision 9).
- The dependency map from configuration, the incident's culprit change and
  the earlier revision's template, which the actor already reads from
  ReplicaSets.
- The rollout comparison's statistics (a conditional binomial rate test),
  the first of the Lab's tests run on live telemetry.

## Where it can run

The Lab refuses to run without proven NetworkPolicy enforcement. On
GitHub's runners kindnet enforces it, and CI also proves Calico and Cilium;
on the development VM no plugin can (decisions 11 and 46): its kernel lacks
what kindnet, Calico and Cilium each need, and Flannel never enforces. Lab
work is therefore tested in CI until the self-hosted runner listed in
`docs/STATUS.md` arrives, which the statistics suites of slice 3 need
anyway.

## Slices

### 0. An enforcing development cluster

Tried first, and not possible on the development VM (decision 46). Until
the self-hosted runner arrives, Lab tests run in CI's kindnet and Cilium
clusters. **Proof**: the network plugins workflow's Cilium job finds
enforcement.

### 1. Seal and prove

An `Experiment` takes a namespace from the pool and seals it before any
workload starts: quotas, the default-deny policy, a per-Lab CoreDNS that
answers only Lab names and never forwards, Pod Security `restricted` with
user namespaces where the nodes allow them. Then the probe matrix: positive
controls (a Lab echo service, a Lab DNS name) must answer, so "blocked" is
told from "probe broken"; the negative matrix must fail: the API server,
kube-dns and external resolution, the production twin's Service and pod
IPs, node IPs, cloud metadata addresses, the internet by IP, IPv6 where
dual-stack. Any failure ends the Experiment with the reason; the matrix runs
again at teardown. A TTL reaper returns namespaces the Experiment did not.
**Proof**: an end-to-end test where every negative probe is refused and
every positive one answers, and one where a deliberately missing policy
makes the Experiment fail closed.

**Built** (decisions 47 and 48): the seal (`lab-internal` on top of the
install's default-deny), the matrix from the live cluster with positive
controls, and the Experiment lifecycle for a new `prove` purpose, run by
the Lab as its own fenced identity after the active probe proves
enforcement. The fail-closed proof uses an extra allow-all policy rather
than a missing one: policies add up, so it opens the seal without a test
hook in the Lab. The per-Lab DNS, the teardown re-check and the reaper
move to slice 2, which first starts workloads that need names and outlive
a probe.

### 2. Synthesize the stateless world

Freeze the failing pod's template by digest; replace its service account
(no token, no RBAC); drop sidecars, autoscalers and disruption budgets.
Copy its stateless dependencies from the dependency map, recursing into
copies; fake what the map marks external; refuse what is neither (fail
closed: "a required dependency can't be classified"). Record every
fidelity difference from production. **Proof**: Online Boutique's frontend
and its stateless dependencies run in a Lab with none of their production
peers reachable.

**Design.** An Experiment names its workload and the failing revision; the
controller fills both from the incident.

- **Reading.** The Lab reads the workload's template at that revision (a
  Deployment's ReplicaSet, a StatefulSet's ControllerRevision), the image
  digests its running pods report, the namespace's Services and the
  ConfigMaps the template references. It never reads a Secret.
- **Synthesizing.** One bare pod per workload, so one replica. Containers
  and init containers are kept; mesh sidecars and their init containers are
  dropped. The service account becomes the Lab namespace's default, without
  a token. Images are pinned to the digests production runs. Secret values
  become placeholders and secret volumes empty directories. ConfigMaps are
  copied. A workload with persistent volumes is refused until slice 4.
  Services are copied with the same names, selectors and ports, and
  dependencies follow the topology map, recursively.
- **Names.** A Lab DNS (CoreDNS with only the hosts plugin, never
  forwarding, logging every query) answers each copied Service's short, Lab
  and production names with the Lab copy's address. Pods use it through
  `dnsConfig`, the production namespace first in their search path, so
  configuration is not rewritten. Any other name gets NXDOMAIN and is
  reported: a name the workload needed that the Lab could not provide makes
  the verdict NOT SURE, which catches dependencies the map missed
  (ConfigMap and Secret values, which the map does not read).
- **Proof inside.** With the world up, the matrix runs again, adding each
  copied Service by its production name as a positive control, which must
  reach the Lab copy.
- **Fidelity.** Every difference from production goes in the Experiment's
  status.
- **Cleanup.** An Experiment starts and ends by deleting everything the Lab
  labeled in its namespace; on start the Lab cleans every pool namespace
  without a running Experiment.

The CI proof uses a small two-tier app beside the smoke workloads; GitHub's
runners are too small for a second Online Boutique, which waits for the
self-hosted runner.

**Built** (decisions 50 and 51): reading, synthesis, the Lab DNS and the
world's build, with the matrix run before the world starts and again with
it running, each copied Service by its production name as a control. The
Lab runs the flow for `reproduce` Experiments and ends them NOT SURE with
the world's states and fidelity list until slice 3 judges. Still open: the
controller creating Experiments from incidents, and Online Boutique's
frontend on the self-hosted runner.

### 3. Reproduce and judge

Run the baseline (the failing revision) twice to learn the noise (A/A);
estimate its failure rate with a confidence sequence; race candidates
against it in randomized paired blocks, keeping discordant blocks, with an
e-process whose level holds under optional stopping; PASS, FAIL or NOT SURE
at α over K candidates. The first candidates are the ones Sentinel already
has: the previous revision's template, and a config value reverted. Deletion
controls (a "fix" that disables the failing path) must not pass, or the
oracle is too weak and the verdict is NOT SURE. **Proof**: on the scenario
pool's crash-on-start and bad-config faults, the previous revision passes,
the failing one fails, and the A/A run's false-PASS rate is at most α over
a few hundred repetitions on the self-hosted runner.

**Design.**

- **A run.** One copy of the target in its world, started fresh, with the
  world's other copies kept up across runs. The oracle reads the copy's
  outcome after a fixed window. It succeeds if the copy became ready, did
  not restart, and answers through its Service. It fails if it never got
  ready, restarted, or does not answer.
- **A block.** The baseline (the failing revision) and one candidate start
  together, in random order, so each pair shares conditions. Several
  blocks run at once, within the Lab's quota.
- **Three e-processes per candidate**, each a product of bets that is a
  nonnegative supermartingale under its null, so by Ville's inequality its
  level holds whenever the Lab stops:
  - *better*: over discordant blocks only, against "the candidate is no
    better than the baseline" (a win at most half the time);
  - *works*: over the candidate's own runs, against "it succeeds at most
    three times in four";
  - *fails*: over the same runs, against "it succeeds at least three times
    in four".

  Bets follow the Kelly fraction of a smoothed running estimate, clipped,
  and depend only on past runs.
- **Verdicts.** PASS needs both *better* and *works* to reach K/α. Both
  must reject, so the chance of passing a non-fix stays at most α/K
  without another correction. FAIL needs *fails* to reach K/α. NOT SURE
  comes at the run budget, or when the controls say the oracle cannot
  tell.
- **Controls.**
  - The failing revision raced against itself (A/A) must not PASS.
  - A deletion control (the target's containers replaced by a process
    that only sleeps) must not PASS either; if it does, the oracle cannot
    tell a fix from a disabled feature, and every verdict is NOT SURE.
- **Candidates.** The previous revision's template, read from the
  workload's own revision history, is the first. A config value reverted
  is next.
- **Proof.** The two parts check different things:
  - Unit simulations bound each e-process's false rejection at α over
    ten thousand sequences at the null's edge.
  - CI's enforcing clusters run the two-tier front with a crash-on-start
    revision: the previous revision must PASS, the A/A and deletion
    controls must not.
  - The few hundred repetitions of the gate wait for the self-hosted
    runner.

### 4. Datastores and replay

Throwaway Redis and Postgres seeded from what the failing requests read;
replay of recorded HTTP and gRPC at the frontier; the Lab certificate
authority with per-runtime trust injection; shape-preserving fake Secrets
carrying honeytokens, searched for in the Lab DNS log and the tripwire.
**Proof**: the Redis scenario reproduces in a Lab with a seeded Redis.

## Gate

- False-PASS rate at or below α on labeled good, bad and deletion-style
  patches.
- Isolation: every negative probe refused, in every Lab, on every plugin
  that enforces policy; the Lab refuses to start everywhere else.

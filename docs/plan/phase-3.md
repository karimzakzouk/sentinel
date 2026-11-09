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

### 2. Synthesize the stateless world

Freeze the failing pod's template by digest; replace its service account
(no token, no RBAC); drop sidecars, autoscalers and disruption budgets.
Copy its stateless dependencies from the dependency map, recursing into
copies; fake what the map marks external; refuse what is neither (fail
closed: "a required dependency can't be classified"). Record every
fidelity difference from production. **Proof**: Online Boutique's frontend
and its stateless dependencies run in a Lab with none of their production
peers reachable.

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

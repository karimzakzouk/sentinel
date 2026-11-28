# Phase 0 guide: the foundations

Phase 0 builds what every later module stands on. Nothing here detects or
fixes anything yet. The plan asks for a careful review of two parts, because
they are the hardest to change later: the **layout** and the **data model**.

## Layout

| Path | What lives there |
|---|---|
| `api/v1alpha1/` | The three custom resources: `SentinelPolicy`, `ActionRequest`, `Experiment` |
| `cmd/sentinel/` | The product binary: the controller (`sentinel run`) and the CLI |
| `cmd/harness/` | The evaluation tool that injects faults and records ground truth |
| `internal/model/` | The core records every module speaks |
| `internal/ledger/` | The append-only, hash-chained event log |
| `internal/slot/` | Swappable decision points: champion, shadows, contracts, replay |
| `internal/capability/` | What Sentinel can see and do on this cluster |
| `internal/config/` | Controller configuration, validated fail-closed |
| `internal/harness/` | Scenarios, injection, ground truth |
| `charts/` | `sentinel-crds`, `sentinel`, `sentinel-fence` |
| `test/e2e/`, `test/scenarios/`, `test/apps/` | End-to-end tests, the fault pool, test workloads |
| `hack/` | Toolchain, Docker and kind scripts |

The Watcher, Brain, Lab and Fixer get their own packages when their phase
starts, so each module stays a separate unit.

## Data model

```
Signal → Incident ← Evidence → Hypothesis → Action → Verdict → Fix → TrackRecord
```

- **Signal**: one anomaly or fact, with a *tail probability* (how surprising it
  is under normal behavior) instead of a hard-coded threshold, and a *trust*
  label. Text written by workloads, such as log lines, is `untrusted`: it can
  carry injected instructions and never drives a decision on its own.
- **Incident**: groups signals that share one problem, with a state machine
  (`open → mitigating → mitigated → fixing → resolved → closed`, plus
  `escalated`) whose allowed moves are enforced in code.
- **Evidence**: a redacted summary and a SHA-256 of the raw data; raw data
  stays out of the Ledger.
- **Hypothesis**: a candidate cause with a prior, a posterior and the readable
  likelihood ratios that moved it.
- **Action**: a mitigation and its contract state, from `proposed` to
  `verified` or `undone`.
- **Verdict**: a judgment with its error level α and an interval, never just
  a yes or no. `not-sure` is a valid outcome.
- **Fix**: a config or code change with the Lab verdict that judged it.
- **TrackRecord**: the Beta posterior of an action's success in one cell
  (action × fault class × service), which is how Sentinel earns autonomy.

## The Ledger

Every change to a record is an event; the current state of each record is a
projection that can be rebuilt from the events. Each event's hash covers all of
its fields and the previous event's hash, so editing any stored event breaks
the chain, and `sentinel ledger verify` names the first broken event. Database
triggers reject updates and deletes. The controller refuses to start on a
ledger that fails verification. Events export as standard CloudEvents
(`sentinel ledger export`).

## Slots

A slot is a decision point, such as "which fault class is this?". One
*champion* engine fills it and its answer is used. *Shadow* engines get a copy
of the same input; their answers are recorded next to the champion's and never
used. Outputs are typed (`Distribution`, `Ranking` with a confidence set,
`Estimate` with an interval) and checked: probabilities that do not sum to one
are refused. A decision that cannot be recorded is refused. `Replay` runs any
engine on recorded inputs and compares it with the champion, which is how a new
engine earns promotion.

## Capability report

`sentinel capabilities` reports what Sentinel can do on a cluster and why:
platform, permissions it has and the ones it must never have, dry-run probes
for the node agent, eBPF and user namespaces, sandbox runtimes, deploy tools,
and an active probe that proves NetworkPolicy enforcement with real traffic.
Every disabled feature says why. Building it showed why the active probe is
needed: the same kind cluster enforces NetworkPolicy on GitHub's runners and
silently does not on the development VM, because kind's network plugin needs a
kernel feature the VM lacks. The probe answered correctly on both.

## The fence

`sentinel-fence` is a set of ValidatingAdmissionPolicies that the API server
enforces on Sentinel's identities, whatever their RBAC allows. The controller
may only make dry-run pod creates and access reviews. The actor may only update
existing Deployments and StatefulSets in allowlisted namespaces, and only the
replica count (within bounds) and the restart annotation. The end-to-end suite
checks eleven writes against it.

## Harness

Scenarios in `test/scenarios/` describe a fault and its ground truth. The
harness injects the fault, waits until it visibly takes effect, writes the
ground truth outside the cluster and reverts it. Controls inject nothing and
measure false alarms.

## Things worth questioning

- The `Signal` carries one tail probability. Detectors that produce several
  scores per window may want a richer type.
- `Incident` transitions allow `resolved → open` (a regression). Should a
  reopened incident be a new incident instead?
- The fence fixes images completely until Phase 2 designs rollbacks.

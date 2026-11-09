# Phase 2 plan: Act

Sentinel stops the bleeding: for the incidents it can explain, it runs the
reversible action it already decides in Phase 1, through the workload's
owner, checks that the action helped, and undoes it when it did not. Nothing
acts unless a namespace's owner opts in with a SentinelPolicy; without one,
Sentinel stays in observe mode.

The design it implements is in `end-to-end.canvas.tsx` (Brain: decide, check,
act, verify; owner adapters; guards). Deviations get an entry in
`docs/decisions.md`. Like phase 1, it is built as vertical slices, each
working end to end and scored before the next starts.

## Who may do what

Phase 0 split the identities (decision 7): the controller reads, the actor
writes, and an admission fence the API server enforces bounds every write
the actor makes. Phase 2 keeps that split and makes the actor a process:

- **The controller decides and asks.** It creates an `ActionRequest` (spec
  immutable once created) naming the incident, the action type, the target
  and the action's arguments. It holds no write permission on workloads.
  The controller parses untrusted text all day (logs, events), so it is the
  component most likely to be fooled; it can only *name* what to do.
- **The actor checks and does.** A small Deployment in the actor's own
  namespace, running as the actor's service account, reconciles
  `ActionRequest`s. It checks the action's contract, the namespace's policy,
  its budgets and the kill switch again, independently of the controller,
  then runs the action and writes the request's status. It computes what to
  write from the cluster, not from the request: a rollback names a
  revision, and the actor reads that revision's pod template from the old
  ReplicaSet itself.
- **The fence bounds the actor.** Phase 0's policies allow only updates of
  existing Deployments and StatefulSets in allowlisted namespaces, replicas
  within bounds and the restart annotation. Phase 2 widens it by exactly
  what rollbacks need: a Deployment's pod template may change, and nothing
  else in its spec. That replaces decision 8's digest allowlist: no
  admission policy can tell that a template ran before, but an actor that
  only copies templates from the Deployment's own ReplicaSets can only
  restore what already ran there. A compromised controller can then at
  worst roll a workload back to an earlier revision of itself.

## Slices

### 1. Assist mode for the plain-Kubernetes actions

- **Policy.** The controller reads the namespace's `SentinelPolicy`: the
  highest autonomy per action type. Without a policy, or below `autonomous`,
  the decision stays a shadow record, as in phase 1. Approval flows
  (`approve`, `act-unless-vetoed`) come in slice 3.
- **Requests.** For `rollout.undo` and `scale.restore` at `autonomous`, the
  controller creates an `ActionRequest` in the target's namespace, so the
  namespace's own RBAC decides who can see and veto them.
- **The actor.** `sentinel actor` reconciles requests: preconditions (the
  target exists; its revision and replicas are what the request expected; no
  person changed it within the quiet period; no other action on it is in
  flight, held by a per-workload Lease), budgets (actions per namespace per
  hour), the kill switch, a server-side dry run, then the write with a
  resourceVersion precondition. `rollout.undo` restores the template of the
  revision before the culprit; `scale.restore` puts back the replica count
  the culprit removed.
- **Verification.** The controller watches the incident's signals after the
  action: the action is verified when every signal of the incident has ended
  within a deadline (the rollout's progress deadline, at most ten minutes),
  and undone, with an escalation, when one has not. Phase 1's signals are
  user-facing enough for these two actions (endpoints, readiness, error
  rates); request-level signals arrive with traffic data later.
- **Ledger.** Requests, executions, verdicts and undos are events of the
  incident, shown in the UI's timeline.
- **Proof**: in the scenario pool, with a policy at `autonomous` for both
  actions, the bad image, crash on start, probe, memory and Redis scenarios
  recover through Sentinel's action before the revert; the controls, the pod
  kill and the misspelled address's first minutes see no wrong action; the
  scorer adds time to recovery and wrong actions.

As built (decisions 40 to 42): the actor is `sentinel actor`, one replica
running requests one at a time; it marks a request Executing before it runs
it, so a restart never runs one twice. The controller verifies an action
when every signal of its incident has ended within ten minutes, and records
it as unverified, left to people, otherwise; it does not undo automatically
yet. The quiet period after a person's edit is the actor's precondition
that the workload is still as the decision saw it, because a bad deploy is
itself a person's edit. `TestAssistModeRollsBackABadImage` is the proof end
to end; the scored pool is in `docs/STATUS.md`.

### 2. Guards

Token buckets per scope; a cap on capacity removed per window; a circuit
breaker per (action, workload) that opens on a failed verification; no
restarts under overload; the kill switch as a field of the actor's own
configuration that only the actor's namespace can change.

As built: the hourly budget per namespace and the kill switch are the
actor's (`actor.actions` in the chart); the capacity cap reads the
policy's `maxCapacityRemovalPercent`; the breaker is the controller's,
opens for a day on a failure, a veto or an unverified action, and is
rebuilt from the Ledger at start. Until owner adapters exist, the actor
refuses plain actions on workloads Argo CD or Flux keeps in sync (decision
43).

### 3. Approval

`approve` and `act-unless-vetoed`: a person approves or vetoes a request
with an annotation, and the actor checks, with a SubjectAccessReview, that
whoever set it may approve requests in that namespace. Escalations and
approvals reach people through Slack or GitHub.

As built: no SubjectAccessReview is needed. The `sentinel-fence-approvals`
policy makes each annotation name the identity that sets it, RBAC decides
who may update requests (the CRD chart folds approving into namespace
admins' and editors' roles), and the actor refuses Sentinel's own
identities. `sentinel approve` and `sentinel veto` set the annotation as
the caller. An act-unless-vetoed request runs two minutes after it is
made. Slack and GitHub notifications are still to come.

### 4. Owner adapters

Resolution order from the design: Argo CD (tracking annotation or instance
label), then Flux labels, then Helm release annotations, then plain
Kubernetes. Argo CD: pause auto-sync, roll back by history ID, verify, open a
revert pull request (phase 4's Fixer), resume after merge. Flux: suspend,
roll back, verify, resume. Helm: roll back the release. Argo Rollouts and
Flagger: abort, or defer to their own analysis.

### 5. Track records and autonomy

Each (action, fault class, workload) cell keeps a Beta record of verified
successes, pooled across similar workloads and decayed; a cell may rise to
the level its policy allows once the 5th percentile of its success rate
clears the target, and any harm (a failed verification, a person reverting
the action) demotes it and opens its breaker.

## Gate

- Mitigation success and wrong-action rate for change-induced and resource
  incidents, scored by `harness score`.
- End-to-end tests of the Argo CD and Flux procedures, including
  ApplicationSet and parent-Kustomization cases.

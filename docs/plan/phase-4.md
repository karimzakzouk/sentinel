# Phase 4 plan: the Fixer

Mitigated is not fixed. A rollback, and the pause of the GitOps owner that
keeps it in place, buys time; the bad change is still in Git, and the next
person to resume the owner ships it again. The Fixer turns a mitigation
into a change at the source: it finds where the bad value was written,
proposes the fix as a pull request with the incident's evidence, and once
the fix is merged and synced, releases the mitigation and verifies. The
design is the Fixer tab of `end-to-end.canvas.tsx`; this plan orders it into
vertical slices, each proven before the next starts.

## What phases 0 to 3 left ready

- Incidents whose blamed change names the fields that changed
  (`image:podinfod`, `args:podinfod`), with the revision Sentinel rolled
  back from and the one it rolled back to, so the last known good value of
  each field is on record.
- Owner adapters that already find who writes a workload: Argo CD's
  tracking annotation leads to the Application, its repository, path and
  revision; Flux's labels lead to the Kustomization or HelmRelease and its
  source. The actor pauses them and `sentinel resume` releases them.
- The Lab, which can race a candidate revision's template against the
  failing one (`verify-fix`).
- The Ledger, notices and the approval flow.

## Where it can run

The Fixer needs a Git forge with pull requests. The test cluster runs one:
Gitea (MIT), in a namespace of its own, with repositories Argo CD and Flux
sync from. The development pool is test apps with bugs planted on purpose,
whose right fixes are known, rather than a real organization's code: real
code has few bugs with known answers, and its pull requests would be
public. A fresh, private GitHub organization and a GitHub App come in for
the demo and the outside reviewers (phases 6 and 7); a forge interface keeps
Gitea and GitHub interchangeable. Nothing in this phase needs the
self-hosted runner.

## Slices

### 1. Restore a bad value through Git (tier T0)

For an incident Sentinel mitigated by rolling back a workload a GitOps owner
delivers, where the blamed change set fields to new values:

1. **Find the writer**: the owner, then its source repository, revision and
   path. Values the owner itself sets (an Argo CD Application's kustomize
   images, a Flux Kustomization's images or patches, a HelmRelease's values)
   are written wherever the owner object comes from, which may be another
   path of the same repository; the first version handles the owner's own
   path and says when the value comes from elsewhere.
2. **Find the file and line** that set each field: render the path with
   Kustomize's origin annotations (in-process, through the krusty library)
   to learn which file or patch set it; for plain manifests, the file that
   holds the object.
3. **Edit**: restore the last known good value in that file with a
   comment-preserving YAML edit, and nothing else.
4. **Check the render**: render again; only the blamed fields of the
   blamed object may differ from production's last good template.
5. **Propose**: a branch and a pull request with the incident, the blamed
   change, the rollback and its verification, and the Lab's verdict when
   there is one. The Fixer abstains, and says why, when any step is unsure.
6. **Converge**: once the fix is merged and the owner's source has the merge
   commit, ask the actor to resume the owner, then verify that the workload
   kept the good template and the incident stayed quiet.

The Fixer runs as its own identity, like the Lab: it may read owners and
their sources, and holds the forge credential, which the controller, which
parses untrusted logs all day, never sees. It never writes to the cluster;
the controller asks it for a fix through a custom resource, as it asks the
Lab for an Experiment, and resuming stays the actor's, under its fence.
**Proof**: an end-to-end test where a bad image committed to the Gitea
repository reaches a Flux Kustomization, Sentinel rolls back, the Fixer
opens a pull request that changes one line, the test merges it as a person
would, and Sentinel resumes the Kustomization and verifies; the same through
Argo CD; and a case where the value comes from a file the Fixer cannot
place, where it abstains.

**Built** (decisions 67 to 70), for images: the Fix resource, the Fixer
as its own fenced identity, the controller asking once per verified
rollback of a Deployment, and the steps after the pull request, Merged,
Ready (the owner's source has the merge) and Applied (the owner is resumed
and has applied it), with Sentinel checking two minutes later that the fix
held. Step 6 changed: resuming stays a person's, whom Sentinel tells how
(decision 69). Proven end to end through Flux against a Gitea in the test
cluster; the abstentions are unit-tested.

### 2. Partial reverts

When the blamed change is a commit, revert only its hunks that set the
blamed fields, keeping its other changes; escalate when later commits touch
the same lines. **Proof**: scenarios whose culprit commit also changes
something harmless, which must survive the fix.

### 3. Values of their own: limits, timeouts, probes

For memory limits, timeouts and probe thresholds, a restored value can be
as wrong as the new one. Propose a value from quantiles of normal runs
instead (tier T1), and let the Lab race it before the pull request opens.
**Proof**: the memory-limit scenarios, fixed by a learned limit the Lab
passes.

### 4. Code incidents

Source resolution from the artifact (signed provenance, build information
embedded in the binary, OCI labels, SHA-like tags; abstain without a signed
source or two that agree), localization over the culprit change and crash
stacks, templates for the most common faults, builds with the build-parity
check, and the Lab as the oracle. Go first, then Python and
JavaScript/TypeScript.

### 5. Delivery tiers

Auto-merge for tiers that have earned it, per repository and namespace, by
the same kind of track record as phase 2's autonomy: a canary, and an
automatic Git revert when it fails.

## Gate

- Verified-fix and false-fix rates on the development pool.
- A pilot round of outside patch review.

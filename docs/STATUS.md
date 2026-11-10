# Status

Last updated: Tuesday 10 November 2026, 06:15 Cairo time (UTC+2).

## Where things stand

**Phase 0 (Foundations) is done and tagged `v2.0.0-alpha.0`.** Phase 1
(Observe) has all six slices built at least in their core; its gate still
needs the 72-hour soak. **Phase 2 (Act) is done, its gate passed:** where a
namespace's SentinelPolicy allows it, Sentinel stops the bleeding itself. In the
scored pool it rolled back or restored every fault, each verified, a
median of 69 seconds after the fault appeared, with no wrong action
(below). Through Argo CD and Flux it first pauses the owner that would
undo the rollback: in the GitOps session, a median of 66 seconds from fault
to verified recovery. Without a policy it still only records what it would
do. **Phase 4 (Fix) has four of its five slices built:** after such a
rollback, Sentinel proposes the change that makes it hold in Git, as a pull
request, and checks that it held once it is merged and the owner resumed.
It finds the value in a manifest, an images entry or a kustomization's
patch, fixes only the value the fault is blamed on when one commit changed
several, proposes a memory or CPU limit learned from healthy use rather than
the old one, and merges its own fix where a policy grants it and its record has
earned it. For a crash in code, in almost any language, it tries one
fixer's change after another, each built by the source's own pipeline and
raced in the Lab before the next: the crashing function put back, a fixed
template, the culprit commit reverted, and last an optional language
model's change; proven on the development cluster with a Go and a Python
service, and with a small local model (below). It works with GitHub,
Gitea, GitLab and Bitbucket. Where the cluster does not enforce
NetworkPolicy, every Lab pod now seals itself, so the Lab runs on the
development cluster too. **Phase 5 (Extend and harden) has started:**
four more fault classes, a Merkle tree over the Ledger, standby replicas
for the actor, the Lab and the Fixer, a console in the UI where a person
signs in as themselves to approve, veto, resume and set policies, and
optional traffic capture, whose recorded reads the Lab replays to its
copies.

To look at it on a cluster where it runs:

- `kubectl -n sentinel port-forward deploy/sentinel 8090`, then open
  <http://localhost:8090>: incidents, why Sentinel blames what it blames,
  what it did or would do, every action it took, and what it can do on
  this cluster.
- `kubectl -n sentinel exec deploy/sentinel -- /sentinel incidents`, and
  `... /sentinel report shadow --since 24h` for what it would have done.
- Sign in to the console with your own Kubernetes token (the **Sign in**
  button, for example a token from `kubectl create token`): approve or veto
  what Sentinel asks for, resume what it paused, and set a namespace's
  policy, all as yourself.
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
| 4. Owner adapters | Done for Argo CD Applications, Flux Kustomizations and Flux HelmReleases (decision 63). Left to people: an Application whose ApplicationSet rewrites its sync policy, a Kustomization whose parent sets its `spec.suspend`, and a HelmRelease that rolls back failed upgrades itself | The GitOps acting session below. In the E2E owners workflow: `TestArgoCDRollbackPausesTheApplication`, `TestArgoCDApplicationSets` (both kinds of ApplicationSet, and `sentinel resume` afterwards, decision 66), `TestFluxRollbackSuspendsTheKustomization`, `TestFluxRollbackSuspendsTheHelmRelease`, `TestFluxLeavesAKustomizationItsParentResumesToPeople` |
| 5. Track records and autonomy | Done: levels a policy makes earned (decision 60), records that age and pool (decision 62), and a person's revert counted as harm once the revision they put back runs without trouble (decisions 61 and 65) | Unit tests; in CI, `TestAnApprovedRollbackRunsAndNamesItsApprover` holds an earned `autonomous` rollback for a person until it has a record |

## Phase 3 progress

| Slice | State | Proof |
|---|---|---|
| 0. An enforcing development cluster | Replaced: where NetworkPolicy is not enforced, every Lab pod seals itself with Sentinel's own firewall before anything else in it runs (decision 90), so the Lab runs on the development VM's Flannel cluster | On the development cluster, Flannel: `TestTheLabSealsItselfOnAnyNetworkPlugin` (every forbidden destination refused, the controls answered), the prove Experiment "sealed by each pod's own firewall: 27 forbidden destinations refused", `TestTheLabVerifiesTheRollbackOfACrashingRevision` (15 of 15 against 15 of 15, the actor rolled back on the Lab's verdict), `TestALabReproductionRunsTheWorldBehindAProvenSeal`, and seven new fence cases refusing ways around the firewall. `TestTheFirewallRefusesAllButItsDestinations` installs it in a namespace of its own in the unit tests |
| 1. Seal and prove | Done, except the per-Lab DNS, teardown re-check and reaper, which move to slice 2 with the workloads that need them. The Lab runs as its own fenced identity (decision 48) and fails closed where enforcement is not proven | `TestTheLabSealHoldsOnlyWherePolicyIsEnforced`: on Flannel the matrix caught 16 leaks, as it must; in CI's enforcing clusters none may leak. `TestALabExperimentRunsOnlyBehindAProvenSeal`: on Flannel the Lab stays off and says why; in enforcing clusters a `prove` Experiment passes, and fails once an extra policy opens the namespace. Seven Lab cases in `TestFenceBlocksForbiddenWrites`. In CI, Calico and Cilium refused every forbidden destination; kindnet let Lab pods reach cloud metadata, and everything in the cluster as a pod started, so the Lab refuses it there (decisions 49 and 56) |
| 2. Synthesize the stateless world | The world built and proven: synthesis, the Lab DNS (decision 51), production's images under the fence's bounds (decision 50), the seal proven before the world starts and again with it running; Online Boutique's frontend and the ten services it calls ran in a Lab on the development cluster, every copy ready within 95 seconds and answering by its production name, once copies were fitted to Pod Security `restricted` (decision 59) | `TestTheLabBuildsAWorkloadsStatelessWorld`, as the Lab's identity: a front and the backend it calls answer by their production names, the API server's name resolves to nothing, and a canary Secret never reaches the Lab. `TestALabReproductionRunsTheWorldBehindAProvenSeal` in CI's enforcing clusters |
| 3. Reproduce and judge | Races and verdicts built (decisions 52 and 53): reproduce and verify-fix Experiments, the previous revision as the first candidate, a deletion control. The gate's repetitions passed on the self-hosted runner under Calico | `TestTheLabTellsAFixFromTheFailure` on the development cluster: against a front that crashes as it starts, its previous revision passed after 15 blocks (15 of 15 against 0 of 15), the deletion control failed. Through the Lab in CI, on Calico and Cilium: "the previous revision front-9964bb8bd fixes it: it worked in 15 of 15 runs where the failing revision failed in 15 of 15", and a healthy front's failure "did not reproduce"; kindnet and Flannel were refused. Unit simulations: 3.4% false PASS at the null's edge for α = 5%. The `Lab gate` workflow's first round, on the development cluster: the fix passed in 15 blocks, a revision that still crashes and a deletion-style candidate failed in 3, and a flaky revision raced against itself ran out of its 60 blocks, NOT SURE. The `Lab gate` workflow on the self-hosted runner, Calico, 10 races of each kind at α = 5%: a fix passed 10 of 10 (median 15 blocks); a revision that still crashes and a deletion that does nothing failed 10 of 10 (3 blocks); a flaky revision against itself failed 7 and was NOT SURE 3 (51 blocks). No false PASS in 30 races that must not pass. Beside it, Online Boutique's frontend and the ten services it calls were copied into a sealed world, every copy ready and every name the world asked for provided |
| Verified rollbacks | The controller asks the Lab about each proposed rollback (decision 54); the verdict joins the incident and its notices; at `lab-verified` it carries the rollback out (decision 57) | On the development cluster a bad image on `web` opened an incident, the controller asked the Lab, and the Lab, refusing a cluster without enforcement, answered NOT SURE within 35 seconds; the verdict shows on the incident page. In CI on Calico and Cilium, a request at `lab-verified` waited while the Lab raced a crashing revision of the front against its predecessor (15 of 15 against 15 of 15), then the actor rolled the front back, approved by the Lab |
| 4. Datastores and replay | Part built: datastores run empty in the Lab with production's engine and version (decision 78), each secret key has its own placeholder, so a name looked up from one says which secret the world lacks, and a second candidate, the failing revision with its configuration reverted, is raced beside the previous revision (decision 77). With traffic capture on, each run replays up to 20 reads production answered to its copy, which must answer them all (decision 86). Fakes of outside services and the Lab's certificate authority are next | Unit tests, among them a race against a fake cluster where the deletion control stays ready but answers nothing and fails; the replay's script run in the probe image against a server answering 200, 404, 503 and a panic. The race and the Lab's end-to-end tests run on the self-hosted runner, which the soak holds until Thursday |

## Phase 4 progress

| Slice | State | Proof |
|---|---|---|
| 1. Restore a bad value through Git | Built for an image, a command, arguments and environment values set in the files a Flux Kustomization or an Argo CD Application deploys (decisions 67 to 70 and 72), and in a kustomization's patches (decision 73): the Fixer finds the file, images entry or patch that set it, opens a pull request, follows it until the owner has applied it, and Sentinel checks that it held. Resuming the owner stays a person's (decision 69). Proven through Flux and Argo CD, and scored (below) | `TestTheFixerProposesTheFixInGit` and `TestTheFixerProposesTheFixInGitThroughArgoCD`, in CI and on the development cluster: a bad image committed to the Gitea repository the owner deploys from, Sentinel's rollback, a pull request changing line 17 of `deployment.yaml` with its comment kept, merged, the owner resumed, and "fix held", about 3 minutes after the proposal. The Git scenario session below. Unit tests render overlays with images entries, plain manifests, owners' name prefixes, and the abstentions |
| 2. Partial reverts | Built (decision 74): of several values a revision changed, the one the fault class blames is fixed and the rest stay, said in the pull request | The pool's bad tag beside a greeting (below): the tag fixed, the greeting kept; unit tests for every class |
| 3. Values of their own | Built for memory and CPU limits (decisions 75 and 98): a limit learned from healthy use, never above the one that held, raced in the Lab before its pull request opens; memory also updates the heap settings that no longer fit it (decision 96). Left: disk, probes, timeouts and replicas | Unit tests of the race, the verdict's path to the pull request, and the heap settings; the pool's memory-limit scenario (below) |
| 4. Code incidents | Built for Go, Python, JavaScript and TypeScript, Java, Kotlin, Scala, C#, Ruby, PHP, Rust, C, C++ and Swift (decision 91): for a crash a new image brought, or an old bug nothing was rolled back from (decision 95), the Fixer tries fixers in order, each change built by the source's own pipeline and raced in the Lab before the next is tried: the crashing function put back, fifteen templates, the whole commit reverted, and a language model's change last (decisions 92 and 93); without a stack or commit labels too (decision 94) | The release sessions below, on the development cluster: shop's Go crash and pyshop's Python crash each fixed and passed by the Lab, 15 of 15 runs; with a 1.5B local model alone, its first change failed in the Lab and it was asked again. `TestTheCrashingFunctionIsPutBackInEveryLanguage` (twelve languages), `TestTemplatesFixTheCrashesEveryCodebaseHas`, `TestACrashInCodeGetsAFixInItsSource` (restore failed by the Lab, then a revert on the same pull request, then a model's change in a new one after a person closed it), `TestAnOldBugIsFixedFromTheCommitBeforeIt` |
| 5. Delivery tiers | Built (decision 76): `fix.merge` at `autonomous`, earned by the record of fixes that held; the Fixer checks the grant before it merges | Unit tests of the grant, the record and the merge on Gitea and GitHub |
| Forges | GitHub, Gitea, GitLab, Bitbucket Cloud and Bitbucket Data Center (`fixer.forge.kind`, decision 87) | Fake-server tests of each forge's API; `hack/forge-live.sh` against a real GitLab CE: a branch pushed as the Fixer pushes, a merge request under a subgroup opened, commented on, merged, and its merge commit found on main, with merge commits and fast-forward |

## Phase 5 progress

| Item | State | Proof |
|---|---|---|
| More incident classes | Node isolation, DNS failure, certificate expiry and refused connections (decision 79) | Unit tests; a deploy's errors that mention a name still roll back |
| Merkle audit log | Done (decision 80) | Matches RFC 6962's test vectors; proofs checked for every tree up to 33 events |
| High availability | Standby replicas for the actor, the Lab and the Fixer (decision 81) | On the development cluster with two of each: one Lease holder each, the fence allowed the Leases, a standby took over a second after the holder was deleted |
| Console | Sign in with your own token, or through the cluster's identity provider (decision 89); approve, veto, resume, set policies, as yourself (decision 82) | Unit tests of sign-in, the person's own approval, refusals from another page, and of single sign-on's state, nonce and replay checks; on the development cluster a real token signed in, the cluster refused what it may not see, and a bad token got 401; `hack/oidc-live.sh` signs in through a real Dex as a browser would |
| Fingerprints | fp2 for logged exceptions (decision 84) | One Redis outage logged with two messages is one exception |
| Traffic capture | Built (decisions 85 and 86): an optional eBPF agent (OpenTelemetry's OBI), a switch per namespace in its SentinelPolicy and the console, request details only, and replay in the Lab | On the development cluster: the agent ran on both nodes with eBPF's capabilities, not privileged; Online Boutique's frontend's requests (`GET /cart`, `/product/<id>`, `POST /cart/checkout`) and its gRPC calls to seven services arrived with methods, paths, statuses and timings; Redis statements, which carry keys, stayed out; the incident page lists them |
| Argo Rollouts abort | Built (decision 97): `rollouts.abort` sets `status.abort`; Flagger still deferred to its analysis | Unit tests in `internal/actor`, `internal/watcher`, `internal/brain` |
| Honeytoken Secrets | Built: Lab env and named-key volumes get shape-preserving stand-ins; looking one up through Lab DNS fails the race | Unit tests in `internal/lab` |
| Outbound call catalog | Built: `capture.Calls` lists distinct client calls for Lab fakes | Unit tests in `internal/capture` |
| Own faker, mined fix patterns | Fakes started (catalog); mined patterns not started | |

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

## GitOps acting session

Four faults delivered through their owners on the development cluster
(`make delivery-up`, then `hack/run-scenarios.sh
test/scenarios/delivery/*.yaml`): a bad image through an Argo CD Application
that syncs and heals itself, through a Flux HelmRelease, and through a Flux
Kustomization, and a flag podinfo does not know, through a patch in the
Kustomization, which makes it exit as it starts. Each fault is a change to
the owner, so a plain rollback would be undone at the next sync:

| Scenario | Detected | Sentinel did | Recovered (fault to verified) |
|---|---|---|---|
| Argo CD: bad image | 6s | paused the Application's automated sync, rolled back | 1m6s |
| Flux HelmRelease: bad image | 8s | suspended the HelmRelease, rolled back | 1m18s |
| Flux Kustomization: bad image | 5s | suspended the Kustomization, rolled back | 1m5s |
| Flux Kustomization: crash on start | 6s | suspended the Kustomization, rolled back | 1m6s |

4 of 4 acted on and verified, root cause, class, blamed change and decision
right each time, no wrong action, no false alarm. Two earlier runs did not
count. In the first, the runner script started the crash scenario while the
previous incident was still open, so the crash joined it; the script now
asks the Ledger for open incidents rather than counting log lines. The second
put back the first run's bad revisions within the day, which Sentinel then
took for people reverting its rollbacks (decision 65).

## Git scenario session

Three faults delivered through Git or its owner, to podinfo, which Flux
deploys from a Gitea repository (`E2E_GITEA=1 make e2e-up`, `make
delivery-up`, then `hack/run-scenarios.sh test/scenarios/git/*.yaml`). The
harness plays the person who owns the repository: it commits each fault,
and reverting it merges whatever fix Sentinel proposed and resumes the owner
as Sentinel says. Each scenario's ground truth says what a right fix
changes, or that proposing none is right:

| Scenario | Sentinel did | Fix |
|---|---|---|
| A commit sets a bad image tag | rolled back through Flux, verified after 1m6s | `deployment.yaml:17` set back, merged, held ✓ |
| A commit starts podinfo with a flag it does not know | rolled back, verified after 59s | the added `command` line removed, merged, held ✓ |
| A bad image set on the Kustomization itself | rolled back, verified after 1m7s | none, and none was right: Git does not hold it ✓ |

Detection, class, root cause and decision right every time, every fault
rolled back and verified, no wrong action; both fixes at the right line, and
both held; no fix that did not hold, no abstention where a fix was right.

### Planted-bug pool

Three test apps of our own in the same Gitea repository, each deployed by
its own Flux Kustomization (`test/apps/gitea-delivery`): echo, agnhost's
netexec on the port its environment names, which exits on a port that is not
a number; patched, whose port a kustomization's patch sets; images, whose
tag an images entry sets. Five scenarios break them, and all eight Git
scenarios were scored together on Monday night (`hack/score.sh`):

| Scenario | Sentinel did | Fix |
|---|---|---|
| A commit sets echo's port to `eighty` | crash-on-start, rolled back, verified after 1m7s | `echo/deployment.yaml:28` set back, merged, held ✓ |
| One commit sets a tag that does not exist and changes the greeting | bad image, rolled back, verified after 1m6s | `echo/deployment.yaml:22`, the tag only; the greeting stayed ✓ |
| A commit sets the port in patched's kustomization patch | rolled back, verified after 1m6s | `patched/kustomization.yaml:10`, in the patch ✓ |
| A commit sets a bad tag in images' images entry | rolled back, verified after 1m5s | `images/kustomization.yaml:7` ✓ |
| A commit cuts echo's memory to 6Mi | memory limit, rolled back, verified after 1m32s | `echo/deployment.yaml:34` set to 15Mi, learned from what echo used ✓ |
| The three podinfo scenarios above | each rolled back and verified | the two fixes held, the owner's image rightly left alone ✓ |

Of twelve runs: 12 detected, every class, root cause and culprit right, 8
acted on and all verified, a median of 1m6s from fault to recovery, no
wrong action; 7 fixes proposed, all at the right file and line, all held;
one abstention, right. The four runs that did not act were stopped by
Sentinel's own limits, which the session found in itself (below): a
scenario that set the same revision as another, which Sentinel will not
roll back a third time in a day, and refusals by the hourly budget, which
locked the workload for a day. With the scenario's tag made its own and
decision 83, both ran clean.

### Planted-bug app (`bugapp`)

podinfo shrugs off bad environment values, so a small service of our own
(`test/planted/bugapp`) checks `LISTEN_PORT`, `CACHE_TTL` and `MODE` as it
starts and exits on a bad one. Flux deploys it from its own Gitea repository,
`person/bugapp` (`hack/bugapp-up.sh`, after `make delivery-up`). Two new
scenarios commit a bad value, and the whole Git pool was scored with `make
score` on the development cluster (Kubernetes 1.37, kindnet):

| Scenario | Sentinel did | Fix |
|---|---|---|
| A commit sets `CACHE_TTL` to a value bugapp cannot parse | rolled back, verified after 1m6s | `deployment.yaml:23` set back, merged, held ✓ |
| A commit sets `MODE` to a value bugapp rejects | rolled back, verified after 1m6s | `deployment.yaml:25` set back, merged, held ✓ |

All five Git scenarios: detection 5 of 5, class, root cause and decision right,
5 of 5 acted on and verified (median 1m6s), no wrong action, 4 of 4 fixes at
the right line and held, the one abstention right. The Fixer needed no change
for environment values; these runs show decision 72 working on an app that
reads them. Four scenarios that must be fixed are still too few to say how
often it is right, and all are one-line changes; slice 2 is the next test.
The first runs found three things, all fixed: the Fixer restored images
only (it now restores commands, arguments and environment values,
decision 72); after a person resumed the Kustomization with `sentinel
resume`, the actor refused every later rollback through it (decision 71);
and the crash scenario first replaced podinfo's command instead of adding a
flag, so the container could not start at all, which Sentinel rightly
called a configuration error.

### Release of a code bug (`shop`)

A Go service of our own, `shop` (`test/apps/shop-src`), lives in the test
Gitea as `person/shop-src`; `hack/ci.sh` plays its pipeline, building an
image of each commit into a registry in the cluster, labelled with the
commit and tagged with its first seven characters, and building every
open pull request's head as it appears. Flux deploys it from
`person/delivery` (`hack/shop-up.sh`). The release scenario commits a
change that builds the catalog into a nil map, builds it, and moves the
deploy repository's tag to the build. On Monday night, Cairo time:

| When | What happened |
|---|---|
| 01:04:56 | The scenario committed the bug to `person/shop-src`, built it, and moved the tag in `person/delivery`; Flux deployed it, and shop panicked as it started ("assignment to entry in nil map") |
| 01:05:03 | Incident: crash on start (85%), the image change to blame (98%), crash logs attached |
| 01:05:04 | Rolled back through Flux, its Kustomization suspended |
| 01:06:03 | Verified: the signals cleared a minute after the rollback; the Fixer asked |
| 01:06:04 | Two pull requests: `shop/deployment.yaml:22` pinned back in `person/delivery`, and in `person/shop-src` "Put load back as it was before c2fe9e2bcce8", one line |
| 01:06:34 | The test pipeline had built the pull request's head; the Fixer found the image |
| 01:12:59 | The Lab asked to race it (after the fix below); NOT SURE, as kind's network plugin does not enforce NetworkPolicy, said on the pull request |

The person merged the pin and resumed Flux; the fix held. The run found
three things, all fixed: the controller asked the Lab about fixes to code
only where it also verifies rollbacks, so the image was never raced while
the Fixer said it would be; a fix built before a restart was never asked
about; and the bot's token could not comment, so the Fixer kept asking and
the verdict never reached the fix's status. The race itself runs where
NetworkPolicy is enforced, on the self-hosted runner after the soak.

Tuesday, with each Lab pod sealing itself (decision 90), the same release
ran again on the development cluster: the Fixer put `load` back, the
pipeline built the pull request's head, and the Lab raced the image: "the
image shop:b0d0f3a fixes it: it worked in 15 of 15 runs where the failing
revision failed in 15 of 15 runs", said on the pull request.

### Release of a Python bug (`pyshop`)

`pyshop` (`test/apps/pyshop-src`, `hack/shop-up.sh pyshop`) builds its
catalog at start; its release scenario makes the catalog `None`. On the
development cluster, Tuesday night, Cairo time: the crash was read with
where it ran (`/app/catalog.py:14`, decision 91); the Fixer put
`catalog.load` back in `person/pyshop-src`; the pipeline built it; and the
Lab passed it, 15 of 15 runs against 15 of 15.

The same release with the Fixer set to its language-model fixer alone, a
1.5-billion-parameter model (`qwen2.5-coder:1.5b`) served by Ollama in the
cluster on CPU (`test/apps/ollama.yaml`): the model's first change was a
guard that skipped the assignment and left the catalog `None`, so pyshop
still crashed, and the Lab failed it in 3 runs. The Fixer asked the model
again with that verdict (decisions 92 and 93), pushed its second change,
`items = {}`, to the same pull request, the pipeline built it, and the Lab
raced it. The run found that a change moved to the tried list counted
twice against the model's attempts, so it was not asked again; fixed.

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

- **The capture agent leaves zeros out.** Its JSON names a gRPC call's
  status code with no value when the code is 0, as protobuf's JSON does,
  so every successful call read without a status until a valueless
  attribute counted as zero.
- **Docker pushes an index.** The test registry held each image as an OCI
  index, so asking for a plain manifest found nothing; and kind cannot load
  an image pulled for several platforms, so the scripts save the nodes'
  platform alone.
- **A refusal by the hourly budget locked a workload for a day.** The Git
  pool's session ran seven rollbacks in one namespace within an hour; the
  budget refused the next two, and each refusal opened the 24-hour breaker
  on its workload, and opened it again when the controller restarted.
  The budget says how often Sentinel acts, nothing against the action
  (decision 83).
- **Two scenarios set the same revision.** The owner's bad image used the
  same tag as the bad image committed to Git, so Kustomize rendered the same
  template: Sentinel had rolled that revision back twice that day and would
  not a third time (decision 65), as designed. Each scenario now sets a
  value of its own.
- **A redeploy with the same image tag does not restart the controller**
  when its pod template is unchanged; the workers' templates changed and
  they restarted. Restart it by hand after loading a new `sentinel:dev`.

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
- **Names collide.** podinfo's Helm chart passes `--grpc-service-name=podinfo`,
  which the dependency map read as a call to another podinfo's Service, so in
  CI a broken release joined someone else's incident and nothing acted on it.
  A failed image pull, a missing ConfigMap or Secret and an unschedulable pod
  now always keep their own incident (decision 64).
- **Putting a revision back is not always disagreeing.** A pipeline that
  redeploys a broken change, or an owner resumed before Git is fixed, looks
  just like a person undoing Sentinel's rollback. What the revision does
  next tells them apart: if it fails again, the rollback stands (decision 65).
- **An ApplicationSet keeps a pause but not its note.** Told to ignore
  `/spec/syncPolicy`, it left Sentinel's pause alone and deleted the note
  `sentinel resume` read within two seconds; resuming now reads the
  ApplicationSet's template instead (decision 66).

Full reasoning in [decisions.md](decisions.md).

## Next

- **Lab realism:** frontier fakes that answer from `capture.Calls`, the
  Lab's certificate authority, and an opt-in recorder of request and
  response bodies for write replay, response comparison and filled
  databases. Honeytoken Secrets that fail a race on DNS lookup are built.
- **More rollback kinds:** DaemonSets, StatefulSets (with approval) and
  Argo Rollouts abort are built; Flagger stays deferred.
- **Hardening:** baselines that survive restarts and dependency links from
  error logs are built; left: the node agent. Standby for actor/Lab/Fixer
  already exists.
- **Settings:** every switch in the console as well as the YAML.
- **CI:** with hosted Actions minutes spent and a $0 budget, CI and E2E
  run on the Azure self-hosted runner; wake may fail and the VM is
  started with `hack/azure-runner/wake.sh` when needed (decision 99).

- **Phase 3:** memory and CPU limits race in the Lab before their pull
  requests; left: fakes of outside services from capture, and the Lab's
  certificate authority for copies that serve TLS.
- **After the soak ends on Thursday:** its report, the network-plugin
  matrix and the Lab's end-to-end tests on the runner, and whether daily
  cycles call for seasonal baselines.

## Blocked or flaky on the development VM

- The Argo CD guestbook image (`gcr.io/google-samples/gb-frontend:v5`) timed
  out pulling on a cold cluster, so `make delivery-up` waited on it. The Git
  scenarios do not need it; `hack/bugapp-up.sh` can run without it. Preloading
  the image with `kind load` would avoid it.
- The disk was 95% full during bring-up; images cleared later. Online Boutique
  was left out for memory.

## Waiting on you

Nothing blocks the work. When you have time:

- **Click Save on the Environment card** if it is still there. It makes
  future cloud agents start with the toolchain ready.
- **Try it** with [the guide](guides/try-it.md), on a VM rather than your
  laptop, and say what reads badly in the UI; sign in to the console with
  a token for your own identity.

Settled with you on Monday night, and now built: fixes to code are built by
the source's own pipeline, which `hack/ci.sh` plays in our tests; traffic
capture is optional, off until a namespace turns it on; the console acts as
the person signed in.

Needed later:

| When | What |
|---|---|
| Phase 1 gate | The 72-hour soak itself, running on the self-hosted runner until Thursday (decision 58) |
| Phase 6 and 7 | A fresh, empty, private GitHub organization and a GitHub App, so outside reviewers and the demo see the Fixer's pull requests on GitHub |
| Phase 6 | Azure for Students credits (managed-cluster run) and outside human evaluators |

## How to check

- **History:** commits on `main`; milestones are tagged `v2.0.0-alpha.N`.
- **CI:** the Actions tab. `CI` runs on every push, `E2E` on code changes,
  `E2E Kubernetes versions` on code changes and weekly, all on GitHub's
  runners and without the Lab's tests. `E2E network plugins` runs the whole
  suite on each of four network plugins on the self-hosted Azure runner when
  the probe's, the Lab's or the actor's code changes, and weekly; `Soak` runs
  there on demand; `Capability report across distributions` runs when the
  probe's code changes and weekly.
- **Run it yourself:** `make e2e-up && make e2e` brings up a kind cluster with
  Sentinel and runs the end-to-end suite; `hack/run-scenarios.sh
  test/scenarios/smoke/*.yaml` runs the scenario pool against it, and
  `make score SINCE=<time>` scores the runs since then.

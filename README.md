# Sentinel

A Kubernetes controller that notices when a change breaks a workload, undoes
that change, and checks that the service actually recovered.

Most incidents start with a change someone made: a new image, a lower memory
limit, a bad environment variable. Sentinel watches workloads, detects
incidents from pod events and logs, ranks which recent change is to blame, and,
where a namespace's policy allows it, rolls that change back and verifies that
the incident's signals clear. Through Argo CD and Flux it first pauses the
owner so the rollback isn't immediately re-applied, then proposes the fix in
Git as a pull request. Without a policy it only records what it would have done.

## How it works

1. **Watch.** Pod states, events, Services and logs become per-workload
   signals: crash loops, OOM kills, image-pull errors, probe failures,
   not-ready pods, lost endpoints, error-rate jumps after a rollout.
2. **Detect.** A Gamma-Poisson count detector compares each signal against its
   learned baseline, with false alarms budgeted at one per week across all
   series.
3. **Blame.** Recent changes (rollouts, scaling, GitOps syncs) are ranked
   against the incident's timing and signals.
4. **Act.** Under a `SentinelPolicy`, Sentinel rolls back or restores the
   blamed change, either on its own or after a person approves. StatefulSets
   always wait for a person.
5. **Verify.** The action only counts if every signal of the incident ends
   within ten minutes; otherwise it is recorded as unverified and left to
   people.
6. **Fix.** The Fixer finds the bad value in the manifests the owner deploys
   from and opens a pull request. For crashes in code it tries candidate fixes
   one by one, each built by the repo's own pipeline and tested in the Lab, an
   isolated copy of the workload, before it is proposed.

Every decision and action is recorded with the reason for it, and shown in the
UI.

## Results

On 12 fault scenarios on a kind cluster running a small test app and Online
Boutique (9 faults, 3 where doing nothing is right):

| | Result |
|---|---|
| Faults acted on and verified | 9 of 9 |
| Wrong actions / false alarms | 0 / 0 |
| Median time from fault to verified recovery | 69 s (59–104 s) |
| GitOps faults through Argo CD and Flux | 4 of 4, median 66 s |

I wrote these scenarios and tuned the detectors on them, so this shows the
pipeline works end to end, not that it generalizes. Baselines against Argo
Rollouts and a simple always-roll-back rule are planned but not yet run. Setup,
per-scenario results, the baseline plan and known failures are in
[docs/evaluation.md](docs/evaluation.md).

## Status

Version 2, in active development. Detection, acting with verification, GitOps
support (Argo CD, Flux Kustomizations and HelmReleases) and fixes through Git
(GitHub, Gitea, GitLab, Bitbucket) work on the development cluster and in CI.
Not done yet: a long-run soak to measure the real false-alarm rate, and the
baselines. [docs/STATUS.md](docs/STATUS.md) has the detailed state and test
sessions; [docs/decisions.md](docs/decisions.md) records the design decisions.
Version 1 is on the `archive/v1` branch.

## Install on a cluster

One Helm release (CRDs + Sentinel + fence). No repo clone.

```sh
helm install sentinel oci://ghcr.io/karimzakzouk/charts/sentinel \
  --namespace sentinel --create-namespace
kubectl -n sentinel port-forward deploy/sentinel 8090
```

Open the UI, sign in, pick which namespaces to watch. That is the normal path.

### Or with a values file (GitOps / chart style)

```yaml
# values.yaml
profile: observe   # or full (actor + Lab)
global:
  watchNamespaces:
    - my-app
    - payments
```

```sh
helm install sentinel oci://ghcr.io/karimzakzouk/charts/sentinel \
  --namespace sentinel --create-namespace -f values.yaml
```

Day-2 (policies, kill switch, Slack/LLM secrets, changing watch namespaces) is
the UI. Privileged agents and forge tokens stay explicit `--set` /
`kubectl create secret` steps.

## Try the capability report

Against any cluster your kubeconfig can reach:

```sh
make build
bin/sentinel capabilities --namespaces my-app --agent-namespace sentinel --lab-namespace sentinel-lab-0 --active
```

`--active` proves NetworkPolicy enforcement with real traffic: it starts two
short-lived pods and a deny policy in the Lab namespace, then deletes them.

## Development

Requirements: Go, Docker, kind, kubectl, Helm and golangci-lint at the versions
pinned in `hack/install-tools.sh` (which installs them on Ubuntu).

| Command | What it does |
|---|---|
| `make test` | Unit tests with the race detector |
| `make lint` | golangci-lint, including the end-to-end tests |
| `make vuln` | govulncheck |
| `make generate` | Regenerate deepcopy code and CRDs |
| `make verify` | Fail if generated files or `go.mod` are stale |
| `make e2e-up` | kind cluster, image, the three charts, test workloads |
| `make e2e` | End-to-end suite against that cluster |
| `E2E_NETWORK=calico make e2e-up` | Same, with a network plugin that enforces NetworkPolicy |
| `make scenarios` | Validate the fault scenarios |

Inject a fault and record its ground truth:

```sh
bin/harness --context kind-sentinel-e2e inject --scenario test/scenarios/smoke/bad-image.yaml --revert
```

## License

Apache-2.0.

# Sentinel

A self-hosted autonomous SRE for Kubernetes: it watches workloads, notices real
incidents, stops the bleeding with reversible actions verified against
user-facing signals, and proposes fixes that a sealed Lab has reproduced and
judged with stated error bounds. Every decision is explained and recorded in a
tamper-evident log, and Sentinel fails closed: a missing prerequisite disables
a feature with the reason shown; it never weakens safety.

"Sentinel" is a working name. v1 lives on the `archive/v1` branch.

## Status

v2 is being rebuilt from scratch, phase by phase. See
[docs/STATUS.md](docs/STATUS.md) for where things stand,
[docs/decisions.md](docs/decisions.md) for the decisions made while building,
and [docs/plan/](docs/plan/) for the full design and build plan.

## Install on a cluster

One Helm release (CRDs + Sentinel + fence):

```sh
./hack/sentinel-install.sh --namespaces my-app
# --profile full  also installs the actor and Lab
kubectl -n sentinel port-forward deploy/sentinel 8090
```

Day-2 is the UI (policies, kill switch, Slack/LLM secrets). Privileged agents
and forge tokens stay explicit Helm/`kubectl` steps.

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

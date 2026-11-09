# Self-hosted CI runner on Azure

`create.sh` sets up one Azure VM that runs a GitHub Actions runner for this
repository, so that jobs needing more than a GitHub-hosted runner gives
(multi-node kind clusters with Calico, Cilium, Flannel or kindnet, long soaks)
can run on it. It deallocates itself when idle and workflows start it on
demand.

| | |
|---|---|
| VM | `sentinel-azure-1` in resource group `sentinel-ci`, eastus unless `LOCATION` says otherwise (see Create) |
| Size | The first of `SIZES` that can be created: Standard_D4as_v4 (4 vCPUs, 16 GiB), then Standard_D4s_v4 |
| Image | Ubuntu 24.04 LTS, `Canonical:ubuntu-24_04-lts:server:latest` |
| Disk | 64 GiB Standard SSD (`StandardSSD_LRS`); Docker's image cache lives on it |
| Network | VNet `sentinel-ci-vnet`, subnet `runners` without default outbound access, Standard static public IP for outbound traffic, NSG `sentinel-azure-1-nsg` denying all inbound traffic |
| Runner | `sentinel-azure-1`, labels `self-hosted`, `Linux`, `X64`, `azure`, `sentinel-lab`; systemd service as user `runner` |

Nothing can reach the VM from outside: there is no inbound rule, and every
command goes through `az vm run-command`, which works through the Azure VM
agent. The SSH key Azure demands is thrown away at creation.

## Files

| File | Where it goes | What it does |
|---|---|---|
| `create.sh` | your machine | Creates everything, sets the VM up, registers the runner, sets repository secrets and variables. Safe to rerun. |
| `register.sh` | your machine | Registers the runner again with a fresh token. |
| `wake.sh` | a workflow or your machine | Starts the VM, waiting out a deallocation in progress. |
| `vm-run.sh` | your machine | Runs a script from `vm/` as root on the VM. |
| `lib.sh` | | Settings and the run-command helper. |
| `vm/setup.sh` | | Docker, Azure CLI, kernel settings, the `runner` user, the runner, hooks and timers. |
| `vm/configure-runner.sh` | | Registers the runner and installs its service. |
| `vm/install-credentials.sh` | `/etc/runner-idle/azure-sp.env` | Stores or removes the service principal used to deallocate (see Identity). |
| `vm/runner-idle-check.sh`, `.service`, `.timer` | `/usr/local/sbin/runner-idle-check` | Deallocates the VM when idle, every 5 minutes. |
| `vm/job-started.sh`, `vm/job-completed.sh` | `/opt/actions-runner/hooks/` | Runner job hooks: clean up leftovers, mark activity. |
| `vm/record-image-use.sh` | `/usr/local/lib/runner-idle/record-image-use` | Stamps images that containers use, for the prune. |
| `vm/runner-docker-prune.sh`, `.service`, `.timer` | `/usr/local/sbin/runner-docker-prune` | Weekly prune of images and build cache unused for 7 days. |
| `vm/verify.sh` | | Docker as `runner`, a two-node kind cluster up and down. |
| `vm/test-idle-check.sh` | | Dry runs of the idle check, idle and busy. |

## Create

```sh
az login ...                     # an identity that can manage sentinel-ci
export GH_TOKEN=...              # can register runners and set Actions secrets and variables
export AZURE_CLIENT_ID=... AZURE_TENANT_ID=... AZURE_SUBSCRIPTION_ID=... AZURE_CLIENT_SECRET=...
hack/azure-runner/create.sh
```

It picks the first size in `SIZES` that the region offers the subscription,
that fits the family and regional vCPU quota, and that Azure accepts when
validating the whole deployment (`az vm create --validate`, which creates
nothing; capacity limits and region policies show only there). Then it
creates the network and the VM with a system-assigned managed identity, runs
`vm/setup.sh` on it, sets up the identity it deallocates itself with,
registers the runner, and sets the repository secrets `AZURE_CLIENT_ID`,
`AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` and `AZURE_CLIENT_SECRET` (where
missing) and the variables `RUNNER_RESOURCE_GROUP` and `RUNNER_VM_NAME`. RG,
LOCATION, SIZES, VM, REPO, RUNNER_NAME and RUNNER_LABELS override the
defaults in `lib.sh`; the resource group's own region does not matter.

### Regions and sizes for this subscription

Checked in November 2026 with deployment validations, which create nothing:

- The subscription (Azure for Students) may deploy only to francecentral,
  switzerlandnorth, italynorth, austriaeast and denmarkeast. Every US,
  Canadian, Latin American, UK and other European region fails with
  `RequestDisallowedByAzure` ("This policy maintains a set of best available
  regions where your subscription can deploy resources"). Asia-Pacific, the
  Middle East and Africa were not checked.
- Neither default size can be created there. francecentral, italynorth,
  austriaeast and denmarkeast do not offer D4as_v4 or D4s_v4 to the
  subscription (`NotAvailableForSubscription`); switzerlandnorth refuses both
  for capacity (`SkuNotAvailable`, in every zone). The newer families (Dv5,
  Dasv5, Dv6 and so on) have no vCPU quota in any of the five.
- Sizes with 4 vCPUs and at least 16 GiB that do validate: in austriaeast
  Standard_B4as_v2 ($0.173 an hour), Standard_B4s_v2 ($0.192), D12_v2 and
  DS12_v2 (28 GiB; no published price there, $0.417 in switzerlandnorth); in
  denmarkeast the same plus B4ms and A4m_v2, none with a published price.
  The B sizes are burstable: B4as_v2 and B4s_v2 sustain 40% of each vCPU and
  go faster only on banked credits. Something else in the subscription
  already uses 2 of austriaeast's 6 regional vCPUs.

So the runner does not exist yet. With one of those sizes, for example:

```sh
SIZES=Standard_B4as_v2 LOCATION=austriaeast hack/azure-runner/create.sh
```

To check the VM afterwards, while it runs:

```sh
hack/azure-runner/vm-run.sh verify.sh            # Docker and a two-node kind cluster
hack/azure-runner/vm-run.sh test-idle-check.sh   # idle check decisions, from the journal
```

## Wake

A workflow starts the VM from a GitHub-hosted job before the jobs that run on
it; those queue until the runner comes online, about a minute after the VM
starts:

```yaml
jobs:
  wake:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - name: Start the self-hosted runner
        env:
          AZURE_CLIENT_ID: ${{ secrets.AZURE_CLIENT_ID }}
          AZURE_CLIENT_SECRET: ${{ secrets.AZURE_CLIENT_SECRET }}
          AZURE_TENANT_ID: ${{ secrets.AZURE_TENANT_ID }}
          AZURE_SUBSCRIPTION_ID: ${{ secrets.AZURE_SUBSCRIPTION_ID }}
          RUNNER_RESOURCE_GROUP: ${{ vars.RUNNER_RESOURCE_GROUP }}
          RUNNER_VM_NAME: ${{ vars.RUNNER_VM_NAME }}
        run: |
          az login --service-principal -u "$AZURE_CLIENT_ID" --tenant "$AZURE_TENANT_ID" -p @- -o none <<<"$AZURE_CLIENT_SECRET"
          az account set -s "$AZURE_SUBSCRIPTION_ID"
          hack/azure-runner/wake.sh
  e2e:
    needs: wake
    runs-on: [self-hosted, azure, sentinel-lab]
```

Without the checkout, `az vm start -g "$RUNNER_RESOURCE_GROUP" -n "$RUNNER_VM_NAME"`
does the same, except that it fails while the VM is still deallocating and
does not tell a VM about to deallocate that a job is coming. `wake.sh` tags
the VM `runner-wake=<unix time>` first, which keeps it up for 15 minutes, and
waits out a deallocation in progress before starting it.

Things to know when moving jobs onto the runner:

- A job starts by deleting every kind, k3d and minikube cluster on the VM, and
  the VM deallocates 30 minutes after the last job. A soak must run inside one
  job; the 90-hour uptime limit below is its ceiling.
- Jobs run one at a time on the one runner, and share the Docker image cache,
  the Go caches in `/home/runner` and the tools in `/usr/local/bin`.
- Re-running only the failed jobs of a run does not re-run the wake job; if the
  VM went down meanwhile, the job waits in the queue (up to 24 hours) until
  something wakes it.
- Docker Hub limits anonymous pulls per IP address, and unlike GitHub-hosted
  runners this one is not exempt. Images pulled inside kind nodes count too.
  Log in (the repository has a `DOCKERHUB_TOKEN` secret) if pulls get throttled.
- Jobs get passwordless sudo and Docker, so they are root on the VM.

## Stop

```sh
az vm deallocate -g sentinel-ci -n sentinel-azure-1
```

Deallocate, never just stop: a VM that is stopped, from Azure or by shutting
down its OS, keeps its compute reserved and billed.

## Idle policy

`runner-idle-check.timer` runs `/usr/local/sbin/runner-idle-check` as root
every 5 minutes and logs each decision to the journal
(`journalctl -t runner-idle-check`):

- Busy while a `Runner.Worker` process (a job) runs; the check then refreshes
  `/var/lib/runner-idle/last-active`, which the job hooks also touch when a
  job starts and ends.
- Deallocates (`az vm deallocate --no-wait` on itself) once idle for 30
  minutes and up for at least 15.
- Deallocates after 90 hours of uptime whatever runs, as a safety net. A
  72-hour soak plus setup fits under it.
- Stays up while `/var/lib/runner-idle/hold-until` holds a later Unix time,
  for working on the VM by hand, and for 15 minutes after the `runner-wake`
  tag that `wake.sh` sets.

`/etc/default/runner-idle` overrides `IDLE_MINUTES`, `MIN_UPTIME_MINUTES`,
`MAX_UPTIME_HOURS` and `WAKE_GRACE_MINUTES`. `runner-idle-check --dry-run`
decides without deallocating, and `--check-auth` checks that it may
deallocate.

The job hooks also clear what earlier jobs left behind: when a job starts,
every kind, k3d and minikube cluster and every stopped container. Docker
images stay, as the cache; `runner-docker-prune.timer` removes, weekly (or at
the first boot after a missed week, once no job runs), images that no
container used and nothing pulled or tagged for 7 days, dangling images,
unused anonymous volumes and build cache unused for 7 days, and everything
unused if the disk is over 85% full.

## Identity

The VM deallocates itself with its system-assigned managed identity when that
has a role over the VM: `create.sh` creates the custom role
`CI runner self-deallocation (sentinel-ci)` (read, start and deallocate
VMs) and assigns it over the VM alone, falling back to the built-in Virtual
Machine Contributor role. Both take Owner or User Access Administrator on
the resource group.

When the role cannot be assigned, `create.sh` stores the service principal it
runs with in `/etc/runner-idle/azure-sp.env` (root only, mode 0600) instead,
and the idle check uses it. CI jobs are root on the VM and can read it, and
with it do anything the service principal can do in the resource group.
To switch to the managed identity, someone with Owner on `sentinel-ci` runs:

```sh
az role assignment create --role "Virtual Machine Contributor" --assignee-principal-type ServicePrincipal \
  --assignee-object-id "$(az vm show -g sentinel-ci -n sentinel-azure-1 --query identity.principalId -o tsv)" \
  --scope "$(az vm show -g sentinel-ci -n sentinel-azure-1 --query id -o tsv)"
hack/azure-runner/wake.sh
REMOVE=1 hack/azure-runner/vm-run.sh install-credentials.sh REMOVE
hack/azure-runner/vm-run.sh test-idle-check.sh   # "the managed identity may deallocate"
```

## Re-register

When the runner was removed from the repository or lost its credentials:

```sh
hack/azure-runner/wake.sh
GH_TOKEN=... hack/azure-runner/register.sh
```

It takes over the name `sentinel-azure-1`. The registration token, like the
service principal above, travels in the run-command request body, never on a
command line, and the copy run-command leaves on the VM is deleted.

## Cost

Pay-as-you-go prices in eastus (Azure retail price list, November 2026):

| | Price | Per month |
|---|---|---|
| Standard_D4as_v4, Linux | $0.192 per hour running | $7.68 at 40 busy hours (each wake adds up to 30 idle minutes) |
| 64 GiB Standard SSD (E6) | $4.80 per month | $4.80 always, plus $0.002 per 10,000 disk operations |
| Standard static public IP | $0.005 per hour | $3.65 always |

In austriaeast and denmarkeast the disk and the IP cost the same, and
Standard_B4as_v2 in austriaeast is $0.173 an hour ($6.92 for 40 hours).
Deallocated, the VM costs only the disk and the IP, about $8.50 a month.
Outbound data to the internet is free for the first 100 GB a month.

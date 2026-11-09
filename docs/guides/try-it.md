# Try it: watch Sentinel explain a fault, then fix it

This walks through what phases 1 to 3 built, on a kind cluster, in about
half an hour. Run it on a machine with at least 4 CPUs and 12 GB of memory
(a cloud VM or the CUFE server), not a laptop: the cluster, Online Boutique
and its load generator need the room.

## 1. Bring it up

You need Docker, Go, Helm, kind and Node; `hack/install-tools.sh` installs
the pinned versions of the rest.

```bash
make e2e-up        # kind cluster, Sentinel, the fence, the smoke app
make boutique-up   # Online Boutique and its load generator
```

`E2E_NETWORK=flannel make e2e-up` uses Flannel, which runs on more kernels.
Add `E2E_ARGOCD=1` or `E2E_FLUX=1` to install Argo CD or Flux too (step 7).

## 2. Open the UI

```bash
kubectl -n sentinel port-forward deploy/sentinel 8090
```

Open <http://localhost:8090>. The UI only listens inside the pod, so
port-forward, which Kubernetes RBAC guards, is the way in. Three pages:

- **Incidents**: what broke, why Sentinel thinks so (class, blamed change,
  each factor's weight), what it did or would do, and the evidence, with
  every step in a timeline.
- **Actions**: everything Sentinel did or would have done, and each
  action's track record.
- **Capabilities**: what Sentinel can see and do on this cluster, and why
  not where it cannot.

The header says **Observe mode** until a policy lets Sentinel act.

## 3. Break something, with Sentinel only watching

```bash
OBSERVE_SECONDS=120 hack/run-scenarios.sh test/scenarios/smoke/bad-image.yaml
```

The harness points the smoke app at an image that does not exist, waits,
then reverts. Within seconds the UI shows a **Bad image** incident blaming
the image change, and says Sentinel *would* roll it back. Nothing changed:
without a policy Sentinel only records. Other faults are in
`test/scenarios/` (a crash, a memory limit too low, a broken probe, Redis
scaled to zero, a misspelled address that only the logs show).

## 4. Let it act

A namespace admin opts a namespace in with a SentinelPolicy:

```bash
kubectl apply -f - <<'EOF'
apiVersion: sentinel.karimzakzouk.github.io/v1alpha1
kind: SentinelPolicy
metadata: {name: assist, namespace: e2e-smoke}
spec:
  autonomy:
    - {action: rollout.undo, level: autonomous}
    - {action: scale, level: autonomous}
EOF
```

Run the same scenario. This time Sentinel asks its actor to roll back, the
actor undoes the change (copying the earlier revision's template from the
Deployment's own ReplicaSet), and a minute later the incident is marked
**done and verified**. `kubectl get actionrequests -A` lists every request
and its outcome.

## 5. Approve or veto instead

Change the policy's level to `approve`, run the scenario again, and the
request waits:

```bash
go build -o bin/sentinel ./cmd/sentinel
kubectl get actionrequests -n e2e-smoke
bin/sentinel approve e2e-smoke/<request>    # or: bin/sentinel veto e2e-smoke/<request>
```

The approval names you: the fence refuses an approval in anyone else's
name, and Sentinel's own identities cannot approve. With a Slack incoming
webhook in a Secret (`notify.slack.secretName` in the chart), the request
also arrives in Slack with these two commands.

## 6. Stop it

The kill switch is the actor's own setting, which only the actor's
namespace holds:

```bash
kubectl -n sentinel-actor edit configmap sentinel-actor-config   # set enabled: false
# or, through the chart:
helm upgrade sentinel charts/sentinel -n sentinel --reuse-values --set actor.actions.enabled=false
```

Within a minute every request is refused with "the kill switch is on".
Setting it back to `true` lets requests through again.

## 7. Through Argo CD or Flux

With `E2E_ARGOCD=1`, `go test -tags e2e ./test/e2e/ -run TestArgoCD -v` deploys
Argo CD's guestbook with automated sync, delivers a bad image through it,
and shows Sentinel pause the Application's sync and roll back; Argo CD then
reports the Application out of sync instead of undoing the rollback. Fix
Git, then `bin/sentinel resume argocd argocd/<application>`. Flux works the
same way with `E2E_FLUX=1` and `-run TestFlux`.

## 8. Ask the Lab

The Lab copies the failing workload and what it calls into a sealed
namespace and races the revision before the bad one against it. It runs only
where it can prove the seal with live traffic: Calico and Cilium on a normal
kernel. It refuses Flannel, which ignores NetworkPolicy, and kindnet on cloud
VMs, which lets pods reach the cloud's metadata service. Bring the cluster up
with Calico, and let Sentinel ask the Lab about every rollback it proposes
(the end-to-end values turn that off, since the tests drive the Lab
themselves):

```bash
E2E_NETWORK=calico make e2e-up
helm upgrade sentinel charts/sentinel -n sentinel --reuse-values --set lab.verifyRollbacks=true
hack/run-scenarios.sh test/scenarios/smoke/bad-image.yaml
```

The incident's timeline shows Sentinel asking the Lab, and within about ten
minutes its verdict: **fixes it** ("it worked in 15 of 15 runs where the
failing revision failed in 15 of 15"), **does not fix it**, or **not sure**,
with the reason and how the Lab's copy differed from production. A verdict
passes only once the failure has reproduced in the copy and a deletion
control, a copy that starts but does nothing, has failed; it holds its error
rate at 5% however early the Lab stops. To ask by hand:

```bash
kubectl apply -f - <<'EOF'
apiVersion: sentinel.karimzakzouk.github.io/v1alpha1
kind: Experiment
metadata: {name: verify-web, namespace: sentinel-lab-0}
spec:
  incidentId: by-hand
  purpose: verify-fix
  labNamespace: sentinel-lab-0
  workload: {apiVersion: apps/v1, kind: Deployment, namespace: e2e-smoke, name: web}
  revision: web-<hash of the bad ReplicaSet>
EOF
kubectl -n sentinel-lab-0 get experiments -w
```

## 9. Score a session

```bash
date -u +%Y-%m-%dT%H:%M:%SZ > /tmp/start
hack/run-scenarios.sh test/scenarios/smoke/*.yaml
make score SINCE=$(cat /tmp/start)
```

The scorer compares Sentinel's incidents and actions with each scenario's
ground truth, which Sentinel never sees: detection, root cause, class,
blamed change, decision, the action it took, the time to verified recovery,
and false alarms and wrong actions in the quiet runs.

## Clean up

```bash
make e2e-down
```

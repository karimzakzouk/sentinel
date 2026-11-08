#!/usr/bin/env bash
# Brings up the end-to-end environment: a kind cluster, Sentinel's image, the
# three charts and the test workloads.
#
#   E2E_CLUSTER=sentinel-e2e E2E_NETWORK=kindnet IMAGE=sentinel:dev hack/e2e-up.sh
#
# Safe to rerun.
set -euo pipefail

cluster=${E2E_CLUSTER:-sentinel-e2e}
network=${E2E_NETWORK:-kindnet}
image=${IMAGE:-sentinel:dev}
ctx="kind-$cluster"
root=$(cd "$(dirname "$0")/.." && pwd)

"$root/hack/kind-up.sh" "$cluster" "$network"

docker build --quiet --build-arg VERSION="$(git -C "$root" describe --tags --match 'v2.*' --always --dirty)" -t "$image" "$root" >/dev/null
kind load docker-image "$image" --name "$cluster"

kubectl --context "$ctx" apply -f "$root/test/apps/namespaces.yaml"

helm --kube-context "$ctx" upgrade --install sentinel-crds "$root/charts/sentinel-crds" --wait
helm --kube-context "$ctx" upgrade --install sentinel "$root/charts/sentinel" \
  --namespace sentinel --create-namespace \
  -f "$root/test/e2e/values-sentinel.yaml" \
  --set image.repository="${image%:*}" --set image.tag="${image##*:}" --set image.pullPolicy=Never \
  --wait --timeout 5m
helm --kube-context "$ctx" upgrade --install sentinel-fence "$root/charts/sentinel-fence" \
  -f "$root/test/e2e/values-fence.yaml" --wait

kubectl --context "$ctx" apply -f "$root/test/apps/smoke.yaml"
kubectl --context "$ctx" -n e2e-smoke rollout status deployment/web --timeout 3m
echo "e2e environment ready on $ctx"

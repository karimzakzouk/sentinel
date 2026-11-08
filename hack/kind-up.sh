#!/usr/bin/env bash
# Creates a two-node kind cluster and waits until it is ready.
#
#   hack/kind-up.sh <cluster-name> [kindnet|calico]
#
# Safe to rerun: an existing cluster is reused.
set -euo pipefail

name=${1:?usage: hack/kind-up.sh <cluster-name> [kindnet|calico]}
network=${2:-kindnet}
here=$(cd "$(dirname "$0")" && pwd)

CALICO_VERSION=v3.33.0
CALICO_SHA256=2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5

case "$network" in
  kindnet | calico) ;;
  *) echo "unknown network plugin: $network" >&2; exit 2 ;;
esac

if ! kind get clusters 2>/dev/null | grep -qx "$name"; then
  kind create cluster --name "$name" --config "$here/kind/$network.yaml" --wait 0s
fi
ctx="kind-$name"

if [ "$network" = calico ]; then
  manifest=$(mktemp)
  trap 'rm -f "$manifest"' EXIT
  curl -fsSL --retry 3 -o "$manifest" "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
  echo "${CALICO_SHA256}  $manifest" | sha256sum -c --quiet -
  kubectl --context "$ctx" apply --server-side -f "$manifest" >/dev/null
  kubectl --context "$ctx" -n kube-system rollout status daemonset/calico-node --timeout 5m
  kubectl --context "$ctx" -n kube-system rollout status deployment/calico-kube-controllers --timeout 5m
fi

kubectl --context "$ctx" wait node --all --for condition=Ready --timeout 5m
echo "cluster $name ($network) is ready; context $ctx"

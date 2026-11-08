#!/usr/bin/env bash
# Creates a two-node kind cluster and waits until it is ready.
#
#   hack/kind-up.sh <cluster-name> [kindnet|calico|flannel]
#
#   kindnet  kind's default. It enforces NetworkPolicy only on kernels with
#            NFQUEUE support, and silently does not elsewhere.
#   calico   always enforces NetworkPolicy.
#   flannel  never enforces NetworkPolicy.
#
# Safe to rerun: an existing cluster is reused.
set -euo pipefail

name=${1:?usage: hack/kind-up.sh <cluster-name> [kindnet|calico|flannel]}
network=${2:-kindnet}
here=$(cd "$(dirname "$0")" && pwd)

CALICO_VERSION=v3.33.0
CALICO_SHA256=2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5
FLANNEL_VERSION=v0.28.10
FLANNEL_SHA256=393bdbce53eb50c8eb97dd513b218518b7d6e7985dc7b889be956e112a711084
CNI_PLUGINS_VERSION=v1.9.1
CNI_PLUGINS_SHA256=b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303

case "$network" in
  kindnet | calico | flannel) ;;
  *) echo "unknown network plugin: $network" >&2; exit 2 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() {
  curl -fsSL --retry 3 -o "$2" "$1"
  echo "$3  $2" | sha256sum -c --quiet -
}

if ! kind get clusters 2>/dev/null | grep -qx "$name"; then
  kind create cluster --name "$name" --config "$here/kind/$network.yaml" --wait 0s
fi
ctx="kind-$name"

case "$network" in
  calico)
    fetch "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml" "$tmp/calico.yaml" "$CALICO_SHA256"
    kubectl --context "$ctx" apply --server-side -f "$tmp/calico.yaml" >/dev/null
    kubectl --context "$ctx" -n kube-system rollout status daemonset/calico-node --timeout 5m
    kubectl --context "$ctx" -n kube-system rollout status deployment/calico-kube-controllers --timeout 5m
    ;;
  flannel)
    # kind's node image ships no bridge plugin, and Flannel delegates to it.
    fetch "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGINS_VERSION}.tgz" "$tmp/cni.tgz" "$CNI_PLUGINS_SHA256"
    tar -C "$tmp" -xzf "$tmp/cni.tgz"
    for node in $(kind get nodes --name "$name"); do
      docker cp "$tmp/bridge" "$node:/opt/cni/bin/bridge"
    done
    fetch "https://github.com/flannel-io/flannel/releases/download/${FLANNEL_VERSION}/kube-flannel.yml" "$tmp/flannel.yaml" "$FLANNEL_SHA256"
    # host-gw needs no VXLAN support in the kernel; kind's nodes share one L2 network.
    sed -i 's/"Type": "vxlan"/"Type": "host-gw"/; s/"EnableNFTables": false/"EnableNFTables": true/' "$tmp/flannel.yaml"
    kubectl --context "$ctx" apply -f "$tmp/flannel.yaml" >/dev/null
    kubectl --context "$ctx" -n kube-flannel rollout status daemonset/kube-flannel-ds --timeout 5m
    ;;
esac

kubectl --context "$ctx" wait node --all --for condition=Ready --timeout 5m
echo "cluster $name ($network) is ready; context $ctx"

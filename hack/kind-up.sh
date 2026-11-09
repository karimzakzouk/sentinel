#!/usr/bin/env bash
# Creates a two-node kind cluster and waits until it is ready.
#
#   hack/kind-up.sh <cluster-name> [kindnet|calico|flannel|cilium]
#
#   kindnet  kind's default. It enforces NetworkPolicy only on kernels with
#            NFQUEUE support, and silently does not elsewhere.
#   calico   always enforces NetworkPolicy, where the kernel has ipset.
#   flannel  never enforces NetworkPolicy.
#   cilium   enforces NetworkPolicy with eBPF, on kernels the others fail on.
#
# KIND_NODE_IMAGE, when set, picks the node image and so the Kubernetes
# version; kind's default is used otherwise.
#
# Safe to rerun: an existing cluster is reused.
set -euo pipefail

name=${1:?usage: hack/kind-up.sh <cluster-name> [kindnet|calico|flannel|cilium]}
network=${2:-kindnet}
here=$(cd "$(dirname "$0")" && pwd)

CALICO_VERSION=v3.33.0
CALICO_SHA256=2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5
FLANNEL_VERSION=v0.28.10
FLANNEL_SHA256=393bdbce53eb50c8eb97dd513b218518b7d6e7985dc7b889be956e112a711084
CNI_PLUGINS_VERSION=v1.9.1
CNI_PLUGINS_SHA256=b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303
CILIUM_VERSION=1.20.4
METRICS_SERVER_VERSION=v0.9.1
METRICS_SERVER_SHA256=3c7f57cdbee0f57fb0259a150e8f6d6cb4f29d72d1aa5dce8803151dd6d4c2a1

case "$network" in
  kindnet | calico | flannel | cilium) ;;
  *) echo "unknown network plugin: $network" >&2; exit 2 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() {
  # GitHub's release downloads answer 504 for minutes at a time now and then;
  # curl doubles its wait between tries, so eight tries span about four.
  curl -fsSL --retry 8 --retry-max-time 300 -o "$2" "$1"
  echo "$3  $2" | sha256sum -c --quiet -
}

if ! kind get clusters 2>/dev/null | grep -x "$name" >/dev/null; then
  image=()
  if [ -n "${KIND_NODE_IMAGE:-}" ]; then
    image=(--image "$KIND_NODE_IMAGE")
  fi
  kind create cluster --name "$name" --config "$here/kind/$network.yaml" --wait 0s "${image[@]}"
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
    # A pod reaching a Service backed by a pod on its own node crosses the
    # bridge, and the reply is un-NATed only if bridged traffic passes
    # netfilter. Docker no longer loads br_netfilter itself.
    sudo -n modprobe br_netfilter 2>/dev/null || true
    for node in $(kind get nodes --name "$name"); do
      docker cp "$tmp/bridge" "$node:/opt/cni/bin/bridge"
      docker exec "$node" sysctl -qw net.bridge.bridge-nf-call-iptables=1 net.bridge.bridge-nf-call-ip6tables=1 ||
        echo "warning: bridged traffic skips netfilter on $node; Services backed by pods on the same node will not answer" >&2
    done
    fetch "https://github.com/flannel-io/flannel/releases/download/${FLANNEL_VERSION}/kube-flannel.yml" "$tmp/flannel.yaml" "$FLANNEL_SHA256"
    # host-gw needs no VXLAN support in the kernel; kind's nodes share one L2 network.
    sed -i 's/"Type": "vxlan"/"Type": "host-gw"/; s/"EnableNFTables": false/"EnableNFTables": true/' "$tmp/flannel.yaml"
    kubectl --context "$ctx" apply -f "$tmp/flannel.yaml" >/dev/null
    kubectl --context "$ctx" -n kube-flannel rollout status daemonset/kube-flannel-ds --timeout 5m
    ;;
  cilium)
    helm --kube-context "$ctx" upgrade --install cilium cilium --repo https://helm.cilium.io/ --version "$CILIUM_VERSION" \
      --namespace kube-system --set ipam.mode=kubernetes --set operator.replicas=1 --wait --timeout 10m >/dev/null
    kubectl --context "$ctx" -n kube-system rollout status daemonset/cilium --timeout 5m
    ;;
esac

kubectl --context "$ctx" wait node --all --for condition=Ready --timeout 5m

# metrics-server serves the resource metrics Sentinel reads. kind's kubelets
# serve self-signed certificates.
fetch "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml" "$tmp/metrics-server.yaml" "$METRICS_SERVER_SHA256"
sed -i 's/- --metric-resolution=15s/- --metric-resolution=15s\n        - --kubelet-insecure-tls/' "$tmp/metrics-server.yaml"
kubectl --context "$ctx" apply -f "$tmp/metrics-server.yaml" >/dev/null
kubectl --context "$ctx" -n kube-system rollout status deployment/metrics-server --timeout 5m

echo "cluster $name ($network) is ready; context $ctx"

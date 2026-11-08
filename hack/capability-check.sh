#!/usr/bin/env bash
# Runs the capability report, with the active NetworkPolicy probe, against a
# cluster and checks the platform and NetworkPolicy answers it gives.
#
#   hack/capability-check.sh <kube-context> <expected-platform> <available|unavailable>
set -euo pipefail

ctx=${1:?usage: hack/capability-check.sh <kube-context> <platform> <available|unavailable>}
want_platform=${2:?expected platform}
want_netpol=${3:?expected NetworkPolicy status}
root=$(cd "$(dirname "$0")/.." && pwd)
lab=sentinel-capability-lab

kubectl --context "$ctx" create namespace "$lab" --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
kubectl --context "$ctx" label namespace "$lab" pod-security.kubernetes.io/enforce=restricted --overwrite >/dev/null

report=$("$root/bin/sentinel" capabilities --context "$ctx" --namespaces default \
  --agent-namespace kube-system --lab-namespace "$lab" --active --settle 45s -o json)

platform=$(jq -r .cluster.platform <<<"$report")
netpol=$(jq -r '.checks[] | select(.id == "network.policy-enforced") | .status' <<<"$report")
echo "platform: $platform (expected $want_platform)"
echo "NetworkPolicy: $netpol (expected $want_netpol): $(jq -r '.checks[] | select(.id == "network.policy-enforced") | .detail' <<<"$report")"
if [ "$platform" != "$want_platform" ] || [ "$netpol" != "$want_netpol" ]; then
  echo "--- cluster state for diagnosis"
  kubectl --context "$ctx" get nodes -o wide
  kubectl --context "$ctx" get pods -A -o wide
  kubectl --context "$ctx" get networkpolicies -A
  exit 1
fi

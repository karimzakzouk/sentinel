#!/usr/bin/env bash
# Deploys Online Boutique into the boutique namespace of the end-to-end
# cluster and waits until every service is available. Safe to rerun.
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)

kubectl --context "$ctx" apply -f "$root/test/apps/namespaces.yaml"
kubectl --context "$ctx" -n boutique apply -f "$root/test/apps/online-boutique/kubernetes-manifests.yaml"
kubectl --context "$ctx" -n boutique wait deployment --all --for condition=Available --timeout 10m
echo "Online Boutique is available in namespace boutique on $ctx"

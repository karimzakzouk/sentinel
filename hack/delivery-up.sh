#!/usr/bin/env bash
# Deploys the GitOps acting session's workloads (test/apps/delivery.yaml) into
# e2e-delivery and waits until each is available. Needs Argo CD and Flux, so
# bring the cluster up with E2E_ARGOCD=1 E2E_FLUX=1. Safe to rerun.
#
#   hack/delivery-up.sh && hack/run-scenarios.sh test/scenarios/delivery/*.yaml
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)

kubectl --context "$ctx" apply -f "$root/test/apps/namespaces.yaml"
kubectl --context "$ctx" apply -f "$root/test/apps/delivery.yaml"
deployments=(podinfo delivery-podinfo-helm kustomize-guestbook-ui)
# With the test Gitea (E2E_GITEA=1), podinfo from its repository too, for
# the scenarios that commit there.
if kubectl --context "$ctx" get namespace gitea >/dev/null 2>&1; then
  kubectl --context "$ctx" apply -f "$root/test/apps/git-delivery.yaml"
  deployments+=(git-podinfo git-echo git-patched git-images)
fi
for d in "${deployments[@]}"; do
  # Each appears once its owner has synced.
  for _ in $(seq 60); do
    kubectl --context "$ctx" -n e2e-delivery get deployment "$d" >/dev/null 2>&1 && break
    sleep 5
  done
  kubectl --context "$ctx" -n e2e-delivery rollout status "deployment/$d" --timeout 5m
done
echo "the delivery session's workloads are available in e2e-delivery on $ctx"

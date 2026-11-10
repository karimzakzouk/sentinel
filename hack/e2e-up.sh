#!/usr/bin/env bash
# Brings up the end-to-end environment: a kind cluster, Sentinel's image, one
# Helm release (CRDs + Sentinel + fence) and the test workloads.
#
#   E2E_CLUSTER=sentinel-e2e E2E_NETWORK=kindnet IMAGE=sentinel:dev hack/e2e-up.sh
#
# E2E_ARGOCD=1 also installs Argo CD (the core install, without its UI) and
# lets the actor pause its Applications; E2E_FLUX=1 installs Flux and lets
# the actor suspend its Kustomizations. E2E_GITEA=1 runs a Gitea in the
# cluster and the Fixer, which proposes fixes to its repository through
# either. Safe to rerun.
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

argocd=()
if [ -n "${E2E_ARGOCD:-}" ]; then
  version=v3.6.1
  kubectl --context "$ctx" create namespace argocd --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
  kubectl --context "$ctx" apply -n argocd --server-side --force-conflicts \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$version/manifests/core-install.yaml"
  # The core install has no API server, which is what creates the default project.
  kubectl --context "$ctx" apply -f "$root/test/apps/argocd-project.yaml"
  kubectl --context "$ctx" -n argocd rollout status statefulset/argocd-application-controller --timeout 5m
  kubectl --context "$ctx" -n argocd rollout status deployment/argocd-repo-server --timeout 5m
  argocd=(--set owners.argocd.enabled=true)
fi
flux=()
if [ -n "${E2E_FLUX:-}" ]; then
  kubectl --context "$ctx" apply -f https://github.com/fluxcd/flux2/releases/download/v2.10.0/install.yaml
  kubectl --context "$ctx" -n flux-system rollout status deployment/source-controller --timeout 5m
  kubectl --context "$ctx" -n flux-system rollout status deployment/kustomize-controller --timeout 5m
  flux=(--set owners.flux.enabled=true)
fi
fixer=()
if [ -n "${E2E_GITEA:-}" ]; then
  "$root/hack/gitea-up.sh"
  fixer=(--set fixer.enabled=true --set fixer.forge.web=http://gitea-http.gitea.svc:3000
    --set fixer.forge.api=http://gitea-http.gitea.svc:3000/api/v1 --set fixer.forge.secretName=sentinel-fixer-forge)
fi

helm --kube-context "$ctx" dependency update "$root/charts/sentinel"
helm --kube-context "$ctx" upgrade --install sentinel "$root/charts/sentinel" \
  --namespace sentinel --create-namespace \
  -f "$root/test/e2e/values-sentinel.yaml" \
  --set image.repository="${image%:*}" --set image.tag="${image##*:}" --set image.pullPolicy=Never \
  --set sentinel-fence.lab.sealImage="$image" \
  --set sentinel-fence.controller.namespace=sentinel \
  --set sentinel-fence.lab.namespace=sentinel \
  --set sentinel-fence.fixer.namespace=sentinel \
  ${E2E_ARGOCD:+--set sentinel-fence.argocdNamespace=argocd} \
  ${E2E_FLUX:+--set 'sentinel-fence.fluxNamespaces={flux-system}'} \
  "${argocd[@]}" "${flux[@]}" "${fixer[@]}" \
  --wait --timeout 5m

kubectl --context "$ctx" apply -f "$root/test/apps/smoke.yaml" -f "$root/test/apps/two-tier.yaml"
kubectl --context "$ctx" -n e2e-smoke rollout status deployment/web --timeout 3m
kubectl --context "$ctx" -n e2e-smoke rollout status deployment/front --timeout 3m
echo "e2e environment ready on $ctx"

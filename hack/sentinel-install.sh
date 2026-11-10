#!/usr/bin/env bash
# From-source helper for developers with this repo checked out.
# Companies do not use this — they install the published chart:
#   helm install sentinel oci://ghcr.io/karimzakzouk/charts/sentinel \
#     -n sentinel --create-namespace \
#     --set-json 'global.watchNamespaces=["my-app"]'
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
namespaces=""
profile=observe
context=""
release=sentinel
ns=sentinel
chart="$root/charts/sentinel"
extra=()

usage() {
  cat <<EOF
From-source install (this git checkout). Companies should use:

  helm install sentinel oci://ghcr.io/karimzakzouk/charts/sentinel \\
    -n sentinel --create-namespace \\
    --set-json 'global.watchNamespaces=["my-app"]'

Usage (repo checkout only):
  $(basename "$0") --namespaces NS[,NS...] [--profile observe|full] [helm flags…]

Profiles: observe (default) | full (actor + Lab)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --namespaces) namespaces=${2:-}; shift 2 ;;
    --namespaces=*) namespaces=${1#*=}; shift ;;
    --profile) profile=${2:-}; shift 2 ;;
    --profile=*) profile=${1#*=}; shift ;;
    --context) context=${2:-}; shift 2 ;;
    --context=*) context=${1#*=}; shift ;;
    --namespace) ns=${2:-}; shift 2 ;;
    --namespace=*) ns=${1#*=}; shift ;;
    --release) release=${2:-}; shift 2 ;;
    *) extra+=("$1"); shift ;;
  esac
done

if [[ -z "$namespaces" ]]; then
  echo "error: --namespaces is required (Sentinel never watches the whole cluster by guess)" >&2
  usage >&2
  exit 1
fi
case "$profile" in observe|full) ;; *)
  echo "error: --profile must be observe or full" >&2
  exit 1
esac

IFS=',' read -r -a ns_list <<<"$namespaces"
json_ns=$(printf '%s\n' "${ns_list[@]}" | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')

helm_ctx=()
[[ -n "$context" ]] && helm_ctx=(--kube-context "$context")

echo "Packaging chart dependencies…"
helm dependency update "$chart" >/dev/null

pool_size=2
lab_pool=$(python3 -c "import json; print(json.dumps([f'sentinel-lab-{i}' for i in range($pool_size)]))")

echo "Installing $release in namespace $ns (profile=$profile)…"
helm "${helm_ctx[@]}" upgrade --install "$release" "$chart" \
  --namespace "$ns" --create-namespace \
  --set profile="$profile" \
  --set-json "global.watchNamespaces=$json_ns" \
  --set-json "watch.namespaces=$json_ns" \
  --set-json "sentinel-fence.namespaces=$json_ns" \
  --set-json "sentinel-fence.lab.pool=$lab_pool" \
  --set "global.labPoolSize=$pool_size" \
  --set "sentinel-fence.lab.sealImage=ghcr.io/karimzakzouk/sentinel:0.1.0" \
  "${extra[@]}" \
  --wait --timeout 10m

cat <<EOF

Sentinel is up (profile=$profile). From-source only — prefer the OCI chart in production.

  kubectl -n $ns port-forward deploy/sentinel 8090
EOF

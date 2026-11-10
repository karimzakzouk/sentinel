#!/usr/bin/env bash
# One-command install: CRDs + Sentinel + fence.
#   ./hack/sentinel-install.sh --namespaces my-app[,other] [--profile observe|full] [--context CTX]
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
Install Sentinel with one Helm release (CRDs, controller, fence).

Usage:
  $(basename "$0") --namespaces NS[,NS...] [--profile observe|full] [helm flags…]

Examples:
  $(basename "$0") --namespaces shop
  $(basename "$0") --namespaces shop,payments --profile full
  $(basename "$0") --namespaces shop --set capture.enabled=true

Profiles:
  observe  (default) watch and record only
  full     also install actor + Lab (rollbacks and sealed verification)

Day-2 (policies, kill switch, MITM toggles, LLM/Slack secrets) is the UI:
  kubectl -n $ns port-forward deploy/sentinel 8090
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
  --set-json "watch.namespaces=$json_ns" \
  --set-json "global.watchNamespaces=$json_ns" \
  --set-json "sentinel-fence.namespaces=$json_ns" \
  --set "sentinel-fence.controller.namespace=$ns" \
  --set "sentinel-fence.lab.namespace=$ns" \
  --set "sentinel-fence.fixer.namespace=$ns" \
  --set-json "sentinel-fence.lab.pool=$lab_pool" \
  --set "sentinel-fence.lab.sealImage=ghcr.io/karimzakzouk/sentinel:0.1.0" \
  --set "global.labPoolSize=$pool_size" \
  "${extra[@]}" \
  --wait --timeout 10m

cat <<EOF

Sentinel is up (profile=$profile).

  kubectl -n $ns port-forward deploy/sentinel 8090
  open http://localhost:8090

Sign in with a Kubernetes token, then:
  • Cluster — what works here
  • Settings — kill switch, detection, Slack/LLM secrets, MITM
  • Policies — let Sentinel act in a namespace

Forge tokens (GitHub App / PAT) stay as kubectl secrets on purpose.
EOF

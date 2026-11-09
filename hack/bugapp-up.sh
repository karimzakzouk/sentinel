#!/usr/bin/env bash
# Builds the bugapp test service (test/planted/bugapp), loads its image into the
# end-to-end cluster, and has Flux deploy it from the test Gitea's repository
# person/bugapp. Needs the cluster up with E2E_GITEA=1 E2E_FLUX=1 and
# hack/delivery-up.sh run. Safe to rerun.
#
#   hack/bugapp-up.sh && hack/run-scenarios.sh test/scenarios/git/env-*.yaml
set -euo pipefail

cluster=${E2E_CLUSTER:-sentinel-e2e}
ctx="kind-$cluster"
root=$(cd "$(dirname "$0")/.." && pwd)
port=${GITEA_LOCAL_PORT:-13001}
k() { kubectl --context "$ctx" "$@"; }

build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
CGO_ENABLED=0 go build -C "$root" -o "$build/bugapp" ./test/planted/bugapp
cp "$roo./test/planted/bugapp/Dockerfile" "$build/"
docker build --quiet -t sentinel-bugapp:1 "$build" >/dev/null
kind load docker-image sentinel-bugapp:1 --name "$cluster"

kubectl --context "$ctx" -n gitea port-forward svc/gitea-http "$port:3000" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true; rm -rf "$build"' EXIT
for _ in $(seq 30); do
  curl -fsS "http://127.0.0.1:$port/api/healthz" >/dev/null 2>&1 && break
  sleep 1
done
person=$(k -n gitea get secret gitea-person -o jsonpath='{.data.token}' | base64 -d)
api() { curl -fsS -H "Authorization: token $person" -H 'Content-Type: application/json' "$@"; }

if ! api "http://127.0.0.1:$port/api/v1/repos/person/bugapp" >/dev/null 2>&1; then
  api -X POST "http://127.0.0.1:$port/api/v1/user/repos" \
    -d '{"name": "bugapp", "default_branch": "main", "auto_init": true, "readme": "Default"}' >/dev/null
  files=""
  for f in "$root"/test/planted/gitea-bugapp/*.yaml; do
    files="$files${files:+,}{\"operation\": \"create\", \"path\": \"$(basename "$f")\", \"content\": \"$(base64 -w0 <"$f")\"}"
  done
  api -X POST "http://127.0.0.1:$port/api/v1/repos/person/bugapp/contents" \
    -d "{\"branch\": \"main\", \"message\": \"Deploy bugapp\", \"files\": [$files]}" >/dev/null
  api -X PUT "http://127.0.0.1:$port/api/v1/repos/person/bugapp/collaborators/sentinel" -d '{"permission": "write"}' >/dev/null
fi

k apply -f "$root/test/planted/git-bugapp.yaml"
for _ in $(seq 60); do
  k -n e2e-delivery get deployment bugapp-bugapp >/dev/null 2>&1 && break
  sleep 5
done
k -n e2e-delivery rollout status deployment/bugapp-bugapp --timeout 5m
echo "bugapp is available in e2e-delivery on $ctx"

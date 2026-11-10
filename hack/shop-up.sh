#!/usr/bin/env bash
# Sets up a test service the Fixer fixes the code of, shop (Go) by default
# or pyshop (Python): its source (test/apps/APP-src) as the test Gitea's
# person/APP-src, with the bot sentinel a collaborator; an image of its main
# branch built by hack/ci.sh; and its manifests (test/apps/APP-deploy) in
# person/delivery's APP/, which Flux deploys into e2e-delivery as git-APP.
# Needs the cluster up with E2E_GITEA=1 E2E_FLUX=1 and hack/delivery-up.sh
# run. Safe to rerun.
#
#   hack/shop-up.sh [shop|pyshop] && hack/ci.sh watch person/shop-src &
#   hack/run-scenarios.sh test/scenarios/git/release-*.yaml
set -euo pipefail

app=${1:-shop}

cluster=${E2E_CLUSTER:-sentinel-e2e}
ctx="kind-$cluster"
root=$(cd "$(dirname "$0")/.." && pwd)
port=${GITEA_LOCAL_PORT:-13031}
k() { kubectl --context "$ctx" "$@"; }

"$root/hack/ci.sh" up

kubectl --context "$ctx" -n gitea port-forward svc/gitea-http "$port:3000" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do
  curl -fsS "http://127.0.0.1:$port/api/healthz" >/dev/null 2>&1 && break
  sleep 1
done
person=$(k -n gitea get secret gitea-person -o jsonpath='{.data.token}' | base64 -d)
api() { curl -fsS -H "Authorization: token $person" -H 'Content-Type: application/json' "$@"; }
base="http://127.0.0.1:$port/api/v1"

# commit_files commits every file of a directory a repository lacks, under a
# prefix, through sed when given.
commit_files() {
  local repo=$1 dir=$2 prefix=$3 sed=${4:-} have files="" f path
  have=$(api "$base/repos/$repo/git/trees/main?recursive=true" | jq -r '.tree[] | select(.type == "blob") | .path')
  while IFS= read -r f; do
    path=$prefix${f#"$dir/"}
    grep -qxF "$path" <<<"$have" && continue
    files="$files${files:+,}{\"operation\": \"create\", \"path\": \"$path\", \"content\": \"$(sed "${sed:-}" "$f" | base64 -w0)\"}"
  done < <(find "$dir" -type f | sort)
  if [ -n "$files" ]; then
    api -X POST "$base/repos/$repo/contents" -d "{\"branch\": \"main\", \"message\": \"Add ${prefix:-the source}\", \"files\": [$files]}" >/dev/null
  fi
}

if ! api "$base/repos/person/$app-src" >/dev/null 2>&1; then
  api -X POST "$base/user/repos" -d "{\"name\": \"$app-src\", \"default_branch\": \"main\", \"auto_init\": true, \"readme\": \"Default\"}" >/dev/null
  api -X PUT "$base/repos/person/$app-src/collaborators/sentinel" -d '{"permission": "write"}' >/dev/null
fi
commit_files "person/$app-src" "$root/test/apps/$app-src" ""

rev=$(api "$base/repos/person/$app-src/branches/main" | jq -r '.commit.id')
tag=$("$root/hack/ci.sh" build "person/$app-src" "$rev" | tail -n 1)
commit_files person/delivery "$root/test/apps/$app-deploy" "$app/" "s/$app:TAG/$app:$tag/"

k apply -f "$root/test/apps/git-$app.yaml" >/dev/null
for _ in $(seq 60); do
  k -n e2e-delivery get deployment "git-$app" >/dev/null 2>&1 && break
  sleep 5
done
k -n e2e-delivery rollout status "deployment/git-$app" --timeout 5m
echo "$app runs in e2e-delivery on $ctx, built from person/$app-src at ${rev:0:7}"

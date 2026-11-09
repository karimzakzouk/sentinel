#!/usr/bin/env bash
# Sets up shop, the test service the Fixer fixes the code of: its source
# (test/apps/shop-src) as the test Gitea's person/shop-src, with the bot
# sentinel a collaborator; an image of its main branch built by hack/ci.sh;
# and its manifests (test/apps/shop-deploy) in person/delivery's shop/,
# which Flux deploys into e2e-delivery as git-shop. Needs the cluster up
# with E2E_GITEA=1 E2E_FLUX=1 and hack/delivery-up.sh run. Safe to rerun.
#
#   hack/shop-up.sh && hack/ci.sh watch person/shop-src &
#   hack/run-scenarios.sh test/scenarios/git/release-*.yaml
set -euo pipefail

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

if ! api "$base/repos/person/shop-src" >/dev/null 2>&1; then
  api -X POST "$base/user/repos" -d '{"name": "shop-src", "default_branch": "main", "auto_init": true, "readme": "Default"}' >/dev/null
  api -X PUT "$base/repos/person/shop-src/collaborators/sentinel" -d '{"permission": "write"}' >/dev/null
fi
commit_files person/shop-src "$root/test/apps/shop-src" ""

rev=$(api "$base/repos/person/shop-src/branches/main" | jq -r '.commit.id')
tag=$("$root/hack/ci.sh" build person/shop-src "$rev" | tail -n 1)
commit_files person/delivery "$root/test/apps/shop-deploy" shop/ "s/shop:TAG/shop:$tag/"

k apply -f "$root/test/apps/git-shop.yaml" >/dev/null
for _ in $(seq 60); do
  k -n e2e-delivery get deployment git-shop >/dev/null 2>&1 && break
  sleep 5
done
k -n e2e-delivery rollout status deployment/git-shop --timeout 5m
echo "shop runs in e2e-delivery on $ctx, built from person/shop-src at ${rev:0:7}"

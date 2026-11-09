#!/usr/bin/env bash
# Runs a Gitea in the end-to-end cluster for the Fixer's tests. A person owns
# the repository person/delivery, seeded with test/apps/gitea-delivery
# (podinfo at its root, the planted-bug test apps in directories); the
# bot sentinel is a collaborator that may push branches and open pull
# requests. The bot's token goes to the Secret the Fixer mounts
# (sentinel/sentinel-fixer-forge), the person's to gitea/gitea-person, which
# tests read to change the repository and merge as a person would. Safe to
# rerun; a Gitea that restarted empty is set up again.
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)
port=${GITEA_LOCAL_PORT:-13000}
k() { kubectl --context "$ctx" "$@"; }
gitea() { k -n gitea exec deploy/gitea -- gitea "$@"; }
secret() { head -c 24 /dev/urandom | base64 | tr -d '/+='; }

k apply -f "$root/test/apps/gitea.yaml"
k -n gitea rollout status deployment/gitea --timeout 5m

# kubectl itself in the background, not the k function, so $! is kubectl's.
kubectl --context "$ctx" -n gitea port-forward svc/gitea-http "$port:3000" >/dev/null 2>&1 &
pf=$!
# The trap's status would be the script's: the port-forward may be gone.
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do
  curl -fsS "http://127.0.0.1:$port/api/healthz" >/dev/null 2>&1 && break
  sleep 1
done
person=$(k -n gitea get secret gitea-person -o jsonpath='{.data.token}' 2>/dev/null | base64 -d || true)
api() { curl -fsS -H "Authorization: token $person" -H 'Content-Type: application/json' "$@"; }

if [ -z "$person" ] || ! api "http://127.0.0.1:$port/api/v1/user" >/dev/null 2>&1; then
  gitea admin user create --username person --password "$(secret)" --email person@example.com --admin --must-change-password=false
  gitea admin user create --username sentinel --password "$(secret)" --email sentinel@example.com --must-change-password=false
  person=$(gitea admin user generate-access-token --username person --token-name e2e --scopes write:repository,write:user --raw)
  bot=$(gitea admin user generate-access-token --username sentinel --token-name fixer --scopes write:repository,write:issue --raw)
  k -n gitea create secret generic gitea-person --from-literal=token="$person" --dry-run=client -o yaml | k apply -f -
  k create namespace sentinel --dry-run=client -o yaml | k apply -f -
  k -n sentinel create secret generic sentinel-fixer-forge --from-literal=token="$bot" --dry-run=client -o yaml | k apply -f -
fi

repo="http://127.0.0.1:$port/api/v1/repos/person/delivery"
if ! api "$repo" >/dev/null 2>&1; then
  api -X POST "http://127.0.0.1:$port/api/v1/user/repos" \
    -d '{"name": "delivery", "default_branch": "main", "auto_init": true, "readme": "Default"}' >/dev/null
  api -X PUT "$repo/collaborators/sentinel" -d '{"permission": "write"}' >/dev/null
fi
# Every file of test/apps/gitea-delivery the repository lacks, so test apps
# added later reach a Gitea set up before them; files it has stay as they are.
have=$(api "$repo/git/trees/main?recursive=true" | jq -r '.tree[] | select(.type == "blob") | .path')
files=""
while IFS= read -r f; do
  path=${f#"$root/test/apps/gitea-delivery/"}
  grep -qxF "$path" <<<"$have" && continue
  files="$files${files:+,}{\"operation\": \"create\", \"path\": \"$path\", \"content\": \"$(base64 -w0 <"$f")\"}"
done < <(find "$root/test/apps/gitea-delivery" -type f -name '*.yaml' | sort)
if [ -n "$files" ]; then
  api -X POST "$repo/contents" -d "{\"branch\": \"main\", \"message\": \"Deploy the test apps\", \"files\": [$files]}" >/dev/null
fi
echo "Gitea is ready on $ctx: http://gitea-http.gitea.svc:3000/person/delivery.git"

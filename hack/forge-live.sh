#!/usr/bin/env bash
# Runs the Fixer's conversation with a real GitLab (internal/forge's live
# test): starts GitLab CE in Docker on 127.0.0.1:18929, gives its root user
# a token kept in a file, creates shop/backend/cart under a subgroup, and
# pushes, opens, comments on, merges and reads a merge request, once with
# merge commits and once fast-forward. GitLab takes a few minutes and about
# 4 GiB to start. Remove it with: docker rm -f gitlab-test
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
web=http://localhost:18929
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
token="$dir/token"
umask 077

if ! docker inspect gitlab-test >/dev/null 2>&1; then
  docker run -d --name gitlab-test --memory 5g -p 127.0.0.1:18929:18929 \
    -e GITLAB_OMNIBUS_CONFIG="external_url \"$web\"; puma[\"worker_processes\"] = 0; sidekiq[\"concurrency\"] = 2; prometheus_monitoring[\"enable\"] = false; gitlab_kas[\"enable\"] = false; registry[\"enable\"] = false" \
    gitlab/gitlab-ce:latest >/dev/null
fi
for _ in $(seq 120); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' gitlab-test)" = healthy ] && break
  sleep 5
done

printf 'glpat-%s' "$(head -c 30 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)" >"$token"
docker cp "$token" gitlab-test:/tmp/sentinel-token >/dev/null
docker exec gitlab-test sh -c 'chown git:git /tmp/sentinel-token && chmod 600 /tmp/sentinel-token'
docker exec gitlab-test gitlab-rails runner "t = User.find_by_username('root').personal_access_tokens.create!(name: 'sentinel-live', scopes: ['api', 'write_repository'], expires_at: 1.day.from_now); t.set_token(File.read('/tmp/sentinel-token').strip); t.save!" >/dev/null
docker exec gitlab-test rm -f /tmp/sentinel-token

api() { curl -fsS -H "PRIVATE-TOKEN: $(cat "$token")" -H 'Content-Type: application/json' "$@"; }
id() { python3 -c 'import json, sys; print(json.load(sys.stdin)["id"])'; }
if ! api "$web/api/v4/projects/shop%2Fbackend%2Fcart" >/dev/null 2>&1; then
  group=$(api -X POST "$web/api/v4/groups" -d '{"name": "shop", "path": "shop"}' | id)
  sub=$(api -X POST "$web/api/v4/groups" -d "{\"name\": \"backend\", \"path\": \"backend\", \"parent_id\": $group}" | id)
  api -X POST "$web/api/v4/projects" -d "{\"name\": \"cart\", \"namespace_id\": $sub, \"initialize_with_readme\": true, \"default_branch\": \"main\"}" >/dev/null
fi

for method in merge ff; do
  api -X PUT "$web/api/v4/projects/shop%2Fbackend%2Fcart" -d "{\"merge_method\": \"$method\"}" >/dev/null
  echo "== GitLab, merge method $method"
  FORGE_KIND=gitlab FORGE_WEB=$web FORGE_API=$web/api/v4 FORGE_REPO=$web/shop/backend/cart.git FORGE_USER=oauth2 FORGE_TOKEN_FILE=$token \
    go test -C "$root" -count=1 -tags live -run TestLiveForge -v ./internal/forge/ | grep -E 'opened|merged|PASS|FAIL|Error'
done

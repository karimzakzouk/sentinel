#!/usr/bin/env bash
# Stands in for a source repository's own pipeline in the end-to-end
# cluster: it builds an image of a commit of a repository in the test Gitea,
# labelled with the repository and the commit as pipelines label them,
# tagged with the commit's first seven characters, pushes it to the
# cluster's test registry (test/apps/ci.yaml) and loads it on the nodes.
#
#   hack/ci.sh up                     start the registry
#   hack/ci.sh build OWNER/NAME REV   build REV, print its tag
#   hack/ci.sh watch OWNER/NAME       build main and every open pull request's head as they appear
#
# The image is named after the repository, less a -src suffix:
# person/shop-src builds 10.96.200.200:5000/shop.
set -euo pipefail

cluster=${E2E_CLUSTER:-sentinel-e2e}
ctx="kind-$cluster"
root=$(cd "$(dirname "$0")/.." && pwd)
gitea_port=${CI_GITEA_PORT:-13030}
registry_port=${CI_REGISTRY_PORT:-15000}
registry=10.96.200.200:5000
k() { kubectl --context "$ctx" "$@"; }

pfs=()
work=$(mktemp -d)
trap 'kill "${pfs[@]}" 2>/dev/null || true; rm -rf "$work"' EXIT
forward() {
  kubectl --context "$ctx" -n "$1" port-forward "svc/$2" "$3:$4" >/dev/null 2>&1 &
  pfs+=($!)
  for _ in $(seq 30); do
    curl -fsS -o /dev/null "http://127.0.0.1:$3$5" 2>/dev/null && return
    sleep 1
  done
  echo "the port-forward to $1/$2 did not answer" >&2
  exit 1
}

# load puts an image on the nodes. kind cannot load an image pulled for
# several platforms as it is, so this saves the nodes' platform alone.
load() {
  docker save --platform "linux/$(docker version -f '{{.Server.Arch}}')" -o "$work/image.tar" "$1"
  kind load image-archive "$work/image.tar" --name "$cluster" >/dev/null
  rm -f "$work/image.tar"
}

up() {
  k apply -f "$root/test/apps/ci.yaml" >/dev/null
  docker image inspect registry:2 >/dev/null 2>&1 || docker pull -q registry:2 >/dev/null
  load registry:2
  k -n ci rollout status deployment/registry --timeout 3m >/dev/null
  echo "the test registry is up at $registry"
}

connect() {
  forward gitea gitea-http "$gitea_port" 3000 /api/healthz
  forward ci registry "$registry_port" 5000 /v2/
  person=$(k -n gitea get secret gitea-person -o jsonpath='{.data.token}' | base64 -d)
}

# built reports whether the registry has an image's tag, whatever its
# manifest's kind: Docker pushes an index.
built() {
  curl -fsS "http://127.0.0.1:$registry_port/v2/$1/tags/list" 2>/dev/null | jq -e --arg tag "$2" '.tags // [] | index($tag)' >/dev/null
}

build() {
  local repo=$1 rev=$2 name tag dir
  name=$(basename "$repo")
  name=${name%-src}
  tag=${rev:0:7}
  if built "$name" "$tag"; then
    echo "$tag"
    return
  fi
  dir="$work/$tag"
  git -c "http.extraHeader=Authorization: token $person" clone -q "http://127.0.0.1:$gitea_port/$repo.git" "$dir"
  git -C "$dir" -c advice.detachedHead=false checkout -q "$rev"
  rev=$(git -C "$dir" rev-parse HEAD)
  (cd "$dir" && CGO_ENABLED=0 go build -trimpath -o "$name" .)
  docker build -q -t "$registry/$name:$tag" -t "127.0.0.1:$registry_port/$name:$tag" \
    --label "org.opencontainers.image.source=http://gitea-http.gitea.svc:3000/$repo" \
    --label "org.opencontainers.image.revision=$rev" "$dir" >/dev/null
  docker push -q "127.0.0.1:$registry_port/$name:$tag" >/dev/null
  load "$registry/$name:$tag"
  rm -rf "$dir"
  echo "$tag"
}

watch() {
  local repo=$1 heads
  local api="http://127.0.0.1:$gitea_port/api/v1/repos/$repo"
  while true; do
    heads=$(curl -fsS -H "Authorization: token $person" "$api/branches/main" | jq -r '.commit.id'
      curl -fsS -H "Authorization: token $person" "$api/pulls?state=open" | jq -r '.[].head.sha')
    for rev in $heads; do
      if ! built "$(basename "${repo%-src}")" "${rev:0:7}"; then
        echo "$(date -u +%H:%M:%S) building $repo at ${rev:0:7}"
        build "$repo" "$rev" >/dev/null
      fi
    done
    sleep "${CI_POLL_SECONDS:-10}"
  done
}

case ${1:-} in
up) up ;;
build)
  connect
  build "$2" "$3"
  ;;
watch)
  connect
  watch "$2"
  ;;
*)
  echo "usage: $0 up | build OWNER/NAME REV | watch OWNER/NAME" >&2
  exit 2
  ;;
esac

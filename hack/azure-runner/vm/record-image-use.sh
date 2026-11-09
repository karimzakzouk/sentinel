#!/usr/bin/env bash
# Stamps each image that a container uses now, so that runner-docker-prune
# keeps images used within its window. Docker records no last-use time for
# images. Run by the job hooks and by runner-idle-check while a job runs.
set -euo pipefail
dir=/var/lib/runner-idle/image-use

mapfile -t containers < <(docker ps --all --quiet --no-trunc)
[ "${#containers[@]}" -gt 0 ] || exit 0
{ docker container inspect --format '{{.Image}}' "${containers[@]}" 2>/dev/null || true; } |
  sort -u | while read -r image; do
  touch "$dir/${image#sha256:}"
done

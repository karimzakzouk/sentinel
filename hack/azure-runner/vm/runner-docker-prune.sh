#!/usr/bin/env bash
# Weekly Docker cleanup that keeps the cache warm. It removes images that no
# container used (as stamped by record-image-use) and that were not pulled,
# built or tagged in the last KEEP_DAYS days, dangling images, unused anonymous
# volumes and build cache unused for KEEP_DAYS days. With the disk more than
# DISK_HIGH_PERCENT full afterwards, it removes every unused image and all
# build cache. It first waits for a running job to finish.
# Run by runner-docker-prune.timer.
set -euo pipefail
KEEP_DAYS=${KEEP_DAYS:-7}
DISK_HIGH_PERCENT=${DISK_HIGH_PERCENT:-85}
use=/var/lib/runner-idle/image-use

for _ in $(seq 72); do
  pgrep -x Runner.Worker >/dev/null || break
  echo "a job is running; checking again in 5 minutes"
  sleep 300
done
runuser -u runner -- /usr/local/lib/runner-idle/record-image-use || true

cutoff=$(($(date +%s) - KEEP_DAYS * 86400))
for id in $(docker image ls --quiet --no-trunc | sort -u); do
  used=$(stat -c %Y "$use/${id#sha256:}" 2>/dev/null || echo 0)
  tag_time=$(docker image inspect --format '{{.Metadata.LastTagTime}}' "$id" 2>/dev/null || true)
  tagged=0
  if [ -n "$tag_time" ]; then
    tagged=$(date -d "$tag_time" +%s 2>/dev/null || echo 0)
  fi
  if ((used < cutoff && tagged < cutoff)); then
    tags=$(docker image inspect --format '{{join .RepoTags " "}}' "$id" 2>/dev/null || true)
    echo "removing ${tags:-untagged} ($id): unused for $KEEP_DAYS days"
    docker image rm --force "$id" >/dev/null || true
  fi
done
find "$use" -type f -mtime +$((KEEP_DAYS * 4)) -delete

docker image prune --force >/dev/null
docker volume prune --force >/dev/null
docker builder prune --force --filter "until=$((KEEP_DAYS * 24))h" >/dev/null
full=$(df --output=pcent /var/lib/docker | tail -n 1 | tr -dc 0-9)
if ((full > DISK_HIGH_PERCENT)); then
  echo "disk ${full}% full: removing every unused image and all build cache"
  docker image prune --all --force >/dev/null
  docker builder prune --all --force >/dev/null
fi
docker system df

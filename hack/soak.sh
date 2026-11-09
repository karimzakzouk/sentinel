#!/usr/bin/env bash
# Starts or reads a soak on the end-to-end cluster: Sentinel with production
# detection settings and nothing injected, so every incident is a false alarm.
#
#   hack/soak.sh start    redeploy Sentinel with test/soak values; prints the start time
#   hack/soak.sh report   incidents opened since the start, and the hours watched
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)
state="$root/test/results/soak-start"

case "${1:-}" in
start)
  helm --kube-context "$ctx" upgrade sentinel "$root/charts/sentinel" --namespace sentinel --reset-then-reuse-values \
    -f "$root/test/soak/values-sentinel.yaml" \
    --set detection.window=5m --set detection.tick=15s --set detection.incidentGap=5m \
    --set detection.warmUp=30m --set logs.sampleInterval=1m --set capabilities.interval=10m --wait
  kubectl --context "$ctx" -n sentinel rollout restart deploy/sentinel
  kubectl --context "$ctx" -n sentinel rollout status deploy/sentinel --timeout 3m
  mkdir -p "$(dirname "$state")"
  date -u +%Y-%m-%dT%H:%M:%SZ | tee "$state"
  ;;
report)
  since=$(cat "$state")
  hours=$(( ($(date -u +%s) - $(date -u -d "$since" +%s)) / 3600 ))
  echo "soak since $since (${hours}h); incidents opened since:"
  kubectl --context "$ctx" -n sentinel exec deploy/sentinel -- /sentinel incidents \
    --since "$(( $(date -u +%s) - $(date -u -d "$since" +%s) ))s"
  ;;
*)
  echo "usage: $0 start|report" >&2
  exit 2
  ;;
esac

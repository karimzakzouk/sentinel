#!/usr/bin/env bash
# Starts or reads a soak on the end-to-end cluster: Sentinel with production
# detection settings and nothing injected, so every incident is a false alarm.
#
#   hack/soak.sh start     redeploy Sentinel with test/soak values; prints the start time
#   hack/soak.sh report    incidents opened since the start, and the hours watched
#   hack/soak.sh watch N   stream a report every SOAK_REPORT_EVERY_MIN minutes for N hours
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)
state="$root/test/results/soak-start"
progress="$root/test/results/soak-progress.txt"

since_seconds() {
  local since
  since=$(cat "$state")
  echo $(( $(date -u +%s) - $(date -u -d "$since" +%s) ))
}

report() {
  local since elapsed_s hours mins out count
  since=$(cat "$state")
  elapsed_s=$(since_seconds)
  hours=$((elapsed_s / 3600))
  mins=$(( (elapsed_s % 3600) / 60 ))
  echo "soak since $since (${hours}h ${mins}m elapsed); every incident below is a false alarm:"
  out=$(kubectl --context "$ctx" -n sentinel exec deploy/sentinel -- /sentinel incidents \
    --since "${elapsed_s}s" 2>&1) || true
  printf '%s\n' "$out"
  # Incident blocks are separated by blank lines in /sentinel incidents.
  count=$(printf '%s\n' "$out" | awk 'BEGIN{n=0;b=0} NF{if(!b){n++;b=1}} !NF{b=0} END{print n}')
  echo "false_alarms=${count:-0}"
}

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
  : >"$progress"
  ;;
report)
  report
  ;;
watch)
  hours=${2:-}
  case "$hours" in
    '' | *[!0-9]*) echo "usage: $0 watch <hours>" >&2; exit 2 ;;
  esac
  if [ "$hours" -lt 1 ] || [ "$hours" -gt 72 ]; then
    echo "hours must be between 1 and 72" >&2
    exit 2
  fi
  every_min=${SOAK_REPORT_EVERY_MIN:-10}
  end=$(( $(date -u +%s) + hours * 3600 ))
  mkdir -p "$(dirname "$progress")"
  {
    echo "== soak watch start $(date -u +%Y-%m-%dT%H:%M:%SZ) for ${hours}h; report every ${every_min}m =="
    report
  } | tee -a "$progress"
  while [ "$(date -u +%s)" -lt "$end" ]; do
    # Sleep in one-minute slices so a cancel is noticed quickly and logs flush.
    for _ in $(seq 1 "$every_min"); do
      now=$(date -u +%s)
      if [ "$now" -ge "$end" ]; then
        break 2
      fi
      sleep 60
    done
    elapsed_s=$(since_seconds)
    h=$((elapsed_s / 3600))
    m=$(( (elapsed_s % 3600) / 60 ))
    {
      echo
      echo "== soak progress $(date -u +%Y-%m-%dT%H:%M:%SZ) ${h}h ${m}m / ${hours}h =="
      report
      uptime || true
      docker stats --no-stream --format '{{.Name}} cpu {{.CPUPerc}} memory {{.MemUsage}}' 2>/dev/null \
        | grep -- '-control-plane\|-worker' || true
    } | tee -a "$progress"
  done
  {
    echo
    echo "== soak watch end $(date -u +%Y-%m-%dT%H:%M:%SZ) =="
    report
  } | tee -a "$progress"
  ;;
*)
  echo "usage: $0 start|report|watch <hours>" >&2
  exit 2
  ;;
esac

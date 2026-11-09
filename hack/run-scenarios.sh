#!/usr/bin/env bash
# Runs scenarios one after another against the end-to-end cluster and prints
# what Sentinel concluded for each. Every scenario is reverted, and each one
# starts only after Sentinel has resolved every open incident, so an earlier
# incident cannot absorb it.
#
#   hack/run-scenarios.sh test/scenarios/smoke/*.yaml
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)
observe=${OBSERVE_SECONDS:-60}

# settle waits until Sentinel has no open incident. It asks the Ledger:
# counting log lines misses incidents a restarted controller took back.
settle() {
  local open
  for _ in $(seq 1 60); do
    open=$(kubectl --context "$ctx" -n sentinel exec deploy/sentinel -- /sentinel incidents --since 24h |
      awk 'NR > 1 && $4 == "open"' | wc -l)
    [ "$open" -eq 0 ] && return
    sleep 5
  done
  echo "incidents still open after 5 minutes" >&2
}

# An interrupted run still reverts its fault: a fault left in place would
# become the "before" state the next run restores.
run=""
revert() {
  if [ -n "$run" ]; then
    "$root/bin/harness" --context "$ctx" revert --run "$root/$run"
    run=""
  fi
}
trap 'revert; exit 130' INT TERM

for scenario in "$@"; do
  settle
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  out=$("$root/bin/harness" --context "$ctx" inject --scenario "$scenario" --out "$root/test/results")
  run=$(grep -o 'test/results/[^ ]*\.json' <<<"$out" || true)
  sleep "$observe"
  revert
  echo "== $(basename "$scenario" .yaml)"
  kubectl --context "$ctx" -n sentinel logs deploy/sentinel --since-time="$start" |
    python3 -c '
import json, sys
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("msg") in ("incident opened", "incident updated"):
        keys = ("msg", "workload", "signal", "class", "classP", "culpritFields", "culpritP", "decision")
        print("  ", {k: e[k] for k in keys if k in e})'
done
settle

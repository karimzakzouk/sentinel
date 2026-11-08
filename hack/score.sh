#!/usr/bin/env bash
# Scores Sentinel's incidents on the end-to-end cluster against the ground
# truth of the scenario runs: exports the Ledger from the controller pod and
# runs harness score.
#
#   hack/score.sh [since]    since: RFC 3339 time of the first run to score
set -euo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
root=$(cd "$(dirname "$0")/.." && pwd)
since=${1:-}

ledger=$(mktemp)
trap 'rm -f "$ledger"' EXIT
kubectl --context "$ctx" -n sentinel exec deploy/sentinel -- \
  /sentinel ledger export --path /var/lib/sentinel/ledger.db >"$ledger"
args=(score --runs "$root/test/results" --ledger "$ledger")
if [ -n "$since" ]; then
  args+=(--since "$since")
fi
"$root/bin/harness" "${args[@]}"

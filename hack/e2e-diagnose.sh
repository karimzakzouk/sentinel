#!/usr/bin/env bash
# Prints the state of the end-to-end cluster after a failure.
set -uo pipefail

ctx="kind-${E2E_CLUSTER:-sentinel-e2e}"
section() { printf '\n===== %s =====\n' "$*"; }

section nodes
kubectl --context "$ctx" get nodes -o wide
section "pods in every namespace"
kubectl --context "$ctx" get pods -A -o wide
section "recent events"
kubectl --context "$ctx" get events -A --sort-by=.lastTimestamp | tail -60
section "sentinel controller"
kubectl --context "$ctx" -n sentinel describe deployment sentinel
kubectl --context "$ctx" -n sentinel logs deployment/sentinel --tail=200
section "admission policies"
kubectl --context "$ctx" get validatingadmissionpolicies -o yaml
section "smoke workloads"
kubectl --context "$ctx" -n e2e-smoke describe deployment,statefulset

#!/usr/bin/env bash
# Starts the runner VM and waits until Azure reports it running, waiting out
# a deallocation in progress. It first tags the VM with the time of the
# request, which keeps a VM that is about to deallocate itself up for a while
# (see vm/runner-idle-check.sh). Needs an az session that may manage the VM,
# such as the service principal in the repository secrets; nothing else.
#
#   RUNNER_RESOURCE_GROUP=sentinel-ci RUNNER_VM_NAME=sentinel-azure-1 hack/azure-runner/wake.sh
set -euo pipefail
rg=${RUNNER_RESOURCE_GROUP:-sentinel-ci}
vm=${RUNNER_VM_NAME:-sentinel-azure-1}
export AZURE_CORE_COLLECT_TELEMETRY=false AZURE_CORE_ONLY_SHOW_ERRORS=true

id=$(az vm show -g "$rg" -n "$vm" --query id -o tsv)
az tag update --resource-id "$id" --operation merge --tags "runner-wake=$(date +%s)" -o none ||
  echo "warning: could not tag $vm with the wake request" >&2
for _ in $(seq 60); do
  state=$(az vm get-instance-view -g "$rg" -n "$vm" \
    --query "instanceView.statuses[?starts_with(code, 'PowerState/')] | [0].code" -o tsv)
  case "$state" in
    PowerState/running)
      echo "$vm is running"
      exit 0
      ;;
    PowerState/starting) ;;
    PowerState/deallocating | PowerState/stopping)
      echo "$vm is ${state#PowerState/}; starting it once that is done"
      ;;
    *)
      echo "starting $vm (${state#PowerState/})"
      az vm start -g "$rg" -n "$vm" --no-wait -o none || true
      ;;
  esac
  sleep 10
done
echo "$vm is not running after 10 minutes" >&2
exit 1

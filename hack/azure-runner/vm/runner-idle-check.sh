#!/usr/bin/env bash
# Deallocates this VM once its GitHub Actions runner has been idle long
# enough. A VM shut down from inside stays allocated and keeps billing
# compute; a deallocated one does not. runner-idle-check.timer runs this as
# root every 5 minutes, and each decision goes to the journal
# (journalctl -t runner-idle-check).
#
#   runner-idle-check [--dry-run | --check-auth]
#
# The VM is busy while a Runner.Worker process (a job) runs; the check then
# refreshes /var/lib/runner-idle/last-active, which the runner's job hooks
# also touch. It deallocates when idle for IDLE_MINUTES and up for at least
# MIN_UPTIME_MINUTES, and once up for MAX_UPTIME_HOURS whatever runs. It stays
# up while /var/lib/runner-idle/hold-until holds a later Unix time, and while
# the VM's runner-wake tag holds a Unix time less than WAKE_GRACE_MINUTES old:
# wake.sh sets that tag before starting the VM, so a wake racing a
# deallocation wins. /etc/default/runner-idle may override the settings.
#
# It signs in with the service principal in /etc/runner-idle/azure-sp.env
# when that file exists, and with the VM's managed identity otherwise.
set -euo pipefail

IDLE_MINUTES=${IDLE_MINUTES:-30}
MIN_UPTIME_MINUTES=${MIN_UPTIME_MINUTES:-15}
MAX_UPTIME_HOURS=${MAX_UPTIME_HOURS:-90}
WAKE_GRACE_MINUTES=${WAKE_GRACE_MINUTES:-15}
state=/var/lib/runner-idle
credentials=/etc/runner-idle/azure-sp.env
mode=${1:-}
export AZURE_CORE_COLLECT_TELEMETRY=false AZURE_CORE_ONLY_SHOW_ERRORS=true

log() {
  echo "$*"
  # Under runner-idle-check.service stdout is the journal already. Children of
  # other services (run-command scripts) inherit JOURNAL_STREAM, so compare it
  # with what stdout really is.
  [ "${JOURNAL_STREAM:-}" = "$(stat -L -c %d:%i "/proc/$$/fd/1" 2>/dev/null)" ] ||
    logger -t runner-idle-check -- "$*"
}

imds() {
  curl -fsS --max-time 5 -H Metadata:true \
    "http://169.254.169.254/metadata/instance/compute/$1?api-version=2021-02-01&format=text"
}

# Identifiers that Azure's error messages repeat, kept out of the journal.
hidden=()
scrub() {
  local s=$1 v
  for v in "${hidden[@]}"; do
    [ -z "$v" ] || s=${s//"$v"/<redacted>}
  done
  printf '%s' "$s"
}

signed_in=
sign_in_failed=
az_login() {
  [ -z "$signed_in" ] || return 0
  [ -z "$sign_in_failed" ] || return 1
  local err id tenant secret
  if [ -f "$credentials" ]; then
    id=$(sed -n 's/^AZURE_CLIENT_ID=//p' "$credentials")
    tenant=$(sed -n 's/^AZURE_TENANT_ID=//p' "$credentials")
    secret=$(sed -n 's/^AZURE_CLIENT_SECRET=//p' "$credentials")
    hidden+=("$id" "$tenant")
    # The values travel through file descriptors, never the command line.
    if ! err=$(az login --service-principal -u @<(printf %s "$id") --tenant @<(printf %s "$tenant") \
      -p @- -o none 2>&1 <<<"$secret"); then
      sign_in_failed=yes
      log "error: service principal sign-in failed: $(scrub "$err")"
      return 1
    fi
    signed_in="service principal"
  else
    # `az vm ... --ids` needs the VM's subscription among those the login
    # finds, so a login that finds none must fail here, not at deallocation.
    if ! err=$(az login --identity -o none 2>&1); then
      sign_in_failed=yes
      log "error: managed identity sign-in failed: $(scrub "$err")"
      return 1
    fi
    signed_in="managed identity"
  fi
}

if ! vm_id=$(imds resourceId) || ! vm=$(imds name) || ! subscription=$(imds subscriptionId); then
  log "error: the instance metadata service did not answer"
  exit 1
fi
hidden+=("$subscription")

deallocate() {
  if [ "$mode" = --dry-run ]; then
    log "dry run: would deallocate $vm: $1"
    return 0
  fi
  log "deallocating $vm: $1"
  az_login || return 1
  local err
  if ! err=$(az vm deallocate --ids "$vm_id" --no-wait -o none 2>&1); then
    log "error: deallocation failed: $(scrub "$err")"
    return 1
  fi
  log "deallocation requested with the $signed_in"
}

if [ "$mode" = --check-auth ]; then
  az_login
  url="https://management.azure.com$vm_id/providers/Microsoft.Authorization/permissions?api-version=2022-04-01"
  if ! actions=$(az rest --method get --url "$url" --query 'value[].actions[]' -o tsv 2>&1); then
    log "error: the $signed_in cannot read its permissions on $vm: $(scrub "$actions")"
    exit 1
  fi
  if grep -qixE '\*|Microsoft\.Compute/\*|Microsoft\.Compute/virtualMachines/(\*|deallocate/action)' <<<"$actions"; then
    log "the $signed_in may deallocate $vm"
  else
    log "error: the $signed_in may not deallocate $vm"
    exit 1
  fi
  exit 0
fi

now=$(date +%s)
up=$(cut -d. -f1 /proc/uptime)
last=$(stat -c %Y "$state/last-active" 2>/dev/null || echo 0)
idle=$((now - last))
up_text="up $((up / 60))m"
if ((last > 0)); then
  idle_text="idle $((idle / 60))m"
else
  idle_text="no activity recorded"
fi

if ((up >= MAX_UPTIME_HOURS * 3600)); then
  deallocate "$up_text, past the ${MAX_UPTIME_HOURS}h safety net, whatever runs"
  exit
fi

if workers=$(pgrep -x Runner.Worker); then
  refreshed="last-active refreshed"
  if [ "$mode" = --dry-run ]; then
    refreshed="dry run, last-active left as is"
  else
    runuser -u runner -- touch "$state/last-active"
    runuser -u runner -- /usr/local/lib/runner-idle/record-image-use >/dev/null 2>&1 || true
  fi
  log "busy: a job is running (Runner.Worker pid ${workers//$'\n'/ }), $up_text; $refreshed"
  exit 0
fi

hold=$(cat "$state/hold-until" 2>/dev/null || true)
if [[ $hold =~ ^[0-9]+$ ]] && ((hold > now)); then
  log "held by $state/hold-until until $(date -u -d "@$hold" +%FT%TZ), $idle_text, $up_text: staying up"
  exit 0
fi
if ((idle < IDLE_MINUTES * 60)); then
  log "$idle_text of ${IDLE_MINUTES}m, $up_text: staying up"
  exit 0
fi
if ((up < MIN_UPTIME_MINUTES * 60)); then
  log "$idle_text, but $up_text of the ${MIN_UPTIME_MINUTES}m minimum: staying up"
  exit 0
fi

wake=
if az_login; then
  wake=$(az vm show --ids "$vm_id" --query 'tags."runner-wake"' -o tsv 2>/dev/null || true)
fi
if [[ $wake =~ ^[0-9]+$ ]] && ((now - wake < WAKE_GRACE_MINUTES * 60)); then
  log "$idle_text, but a wake was requested $(((now - wake) / 60))m ago: staying up"
  exit 0
fi
if pgrep -x Runner.Worker >/dev/null; then
  log "a job started meanwhile: staying up"
  exit 0
fi
deallocate "$idle_text (limit ${IDLE_MINUTES}m), $up_text (minimum ${MIN_UPTIME_MINUTES}m)"

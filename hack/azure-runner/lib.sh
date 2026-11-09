# Settings and helpers shared by the hack/azure-runner scripts; source it.
# Each setting may be overridden from the environment.
RG=${RG:-sentinel-ci}
LOCATION=${LOCATION:-austriaeast}
# VM sizes to try, in order of preference. See README.md for why these.
SIZES=${SIZES:-Standard_B4as_v2}
VM=${VM:-sentinel-azure-1}
REPO=${REPO:-karimzakzouk/sentinel}
RUNNER_NAME=${RUNNER_NAME:-sentinel-azure-1}
RUNNER_LABELS=${RUNNER_LABELS:-azure,sentinel-lab}
azure_runner=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export AZURE_CORE_COLLECT_TELEMETRY=false AZURE_CORE_ONLY_SHOW_ERRORS=true

# vm_run SCRIPT [VARIABLE...] runs vm/SCRIPT as root on the VM through
# `az vm run-command invoke`, with the rest of vm/ beside it and the named
# variables of this shell exported to it. Their values travel in the request
# body, never on a command line, and the copy of the request on the VM is
# deleted. It prints the end of the script's output (run-command keeps 4 KB;
# the whole of it goes to /var/log/azure-runner/ on the VM) and fails when
# the script does.
vm_run() {
  local script=$1 name file payload out
  shift
  payload=$(mktemp)
  chmod 600 "$payload"
  # shellcheck disable=SC2016 # the single-quoted lines are code for the VM
  {
    echo 'export HOME=/root PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
    echo 'd=$(mktemp -d)'
    # run-command saves the request as .../download/<n>/script.sh.
    echo 'self=$(readlink -f "$0")'
    echo 'trap '\''rm -rf "$d"; case $self in /var/lib/waagent/run-command/download/*/script.sh) rm -f "$self" ;; esac'\'' EXIT'
    for file in "$azure_runner"/vm/*; do
      echo "base64 -d >\"\$d/${file##*/}\" <<'EOF'"
      base64 -w 76 "$file"
      echo EOF
    done
    for name in "$@"; do
      printf 'export %s=%q\n' "$name" "${!name}"
    done
    echo 'install -d -m 0700 /var/log/azure-runner'
    echo "bash \"\$d/$script\" >/var/log/azure-runner/${script%.sh}.log 2>&1; rc=\$?"
    echo "tail -c 3500 /var/log/azure-runner/${script%.sh}.log"
    echo 'echo "exit status $rc"'
  } >"$payload"
  out=$(az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript \
    --scripts @"$payload" --query 'value[0].message' -o tsv 2>&1) || true
  rm -f "$payload"
  printf '%s\n' "$out" | sed -e '/^Enable succeeded: *$/d' -e '/^\[stdout\]$/d' -e '/^\[stderr\] *$/d'
  grep -qx 'exit status 0' <<<"$out"
}

# register_runner registers the runner on the VM with a fresh registration
# token from the GitHub API (gh, authenticated through GH_TOKEN).
# shellcheck disable=SC2034 # vm_run reads the variables by name
register_runner() {
  local RUNNER_TOKEN RUNNER_URL=https://github.com/$REPO
  RUNNER_TOKEN=$(gh api -X POST "repos/$REPO/actions/runners/registration-token" --jq .token)
  vm_run configure-runner.sh RUNNER_TOKEN RUNNER_URL RUNNER_NAME RUNNER_LABELS
}

#!/usr/bin/env bash
# Runs one of the scripts in vm/ as root on the runner VM through
# run-command, which needs no inbound access, exporting the named variables
# of this shell to it. The VM must be running.
#
#   hack/azure-runner/vm-run.sh verify.sh
#   hack/azure-runner/vm-run.sh test-idle-check.sh
#   REMOVE=1 hack/azure-runner/vm-run.sh install-credentials.sh REMOVE
set -euo pipefail
. "$(dirname "$0")/lib.sh"
vm_run "${1:?usage: vm-run.sh <script in hack/azure-runner/vm> [VARIABLE...]}" "${@:2}"

#!/usr/bin/env bash
# Registers the runner on the VM again with a fresh registration token,
# taking over its name on GitHub; for when it was removed from the repository
# or its credentials were lost. The VM must be running (wake.sh).
#
#   az login ...          # may run commands on the VM
#   export GH_TOKEN=...   # may register runners on $REPO
#   hack/azure-runner/register.sh
set -euo pipefail
. "$(dirname "$0")/lib.sh"
register_runner

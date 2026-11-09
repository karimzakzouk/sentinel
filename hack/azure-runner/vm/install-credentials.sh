#!/usr/bin/env bash
# Stores the service principal that runner-idle-check deallocates the VM with,
# for when the VM's managed identity has no role on the VM. The file is
# root-only, but CI jobs have passwordless sudo and can read it, so prefer the
# managed identity (see README). Runs as root, normally through create.sh.
#
#   AZURE_CLIENT_ID, AZURE_TENANT_ID, AZURE_CLIENT_SECRET   the service principal
#   REMOVE=1   delete the file instead, so the managed identity is used
set -euo pipefail
dir=/etc/runner-idle
file=$dir/azure-sp.env

if [ "${REMOVE:-}" = 1 ]; then
  rm -f "$file"
  echo "no service principal stored; runner-idle-check uses the managed identity"
  exit 0
fi
: "${AZURE_CLIENT_ID:?}" "${AZURE_TENANT_ID:?}" "${AZURE_CLIENT_SECRET:?}"
install -d -o root -g root -m 0700 "$dir"
umask 077
printf 'AZURE_CLIENT_ID=%s\nAZURE_TENANT_ID=%s\nAZURE_CLIENT_SECRET=%s\n' \
  "$AZURE_CLIENT_ID" "$AZURE_TENANT_ID" "$AZURE_CLIENT_SECRET" >"$file.new"
mv "$file.new" "$file"
echo "service principal stored in $file (root only, mode $(stat -c %a "$file"))"

#!/usr/bin/env bash
# Creates Sentinel's self-hosted GitHub Actions runner in Azure: one VM with
# no inbound access, set up by vm/setup.sh, registered with the repository
# and deallocating itself when idle. Then stores what workflows need to wake
# it as repository secrets and variables. Safe to rerun: existing resources
# are kept, the VM is set up again and the runner registered again.
#
#   az login ...          # may manage resource group $RG
#   export GH_TOKEN=...   # may register runners and set Actions secrets on $REPO
#   export AZURE_CLIENT_ID=... AZURE_TENANT_ID=... AZURE_SUBSCRIPTION_ID=... AZURE_CLIENT_SECRET=...
#   hack/azure-runner/create.sh
#
# The AZURE_* service principal becomes the repository secrets of the same
# names where those are missing. To deallocate itself the VM uses its
# managed identity, which needs a role on the VM, and creating one takes
# Owner or User Access Administrator on $RG; without it the service principal
# is stored on the VM instead (see README).
set -euo pipefail
. "$(dirname "$0")/lib.sh"

step() { echo "=== $*"; }
exists() { "$@" --query id -o tsv >/dev/null 2>&1; }
for name in AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID AZURE_CLIENT_SECRET GH_TOKEN; do
  [ -n "${!name:-}" ] || { echo "$name is not set" >&2; exit 2; }
done
az group show -n "$RG" -o none

if ! exists az vm show -g "$RG" -n "$VM"; then
  step "size"
  # Nothing can reach SSH, but Azure requires a key: make one and throw it away.
  key=$(mktemp -d)
  trap 'rm -rf "$key"' EXIT
  ssh-keygen -q -t rsa -b 3072 -N '' -C "$VM throwaway" -f "$key/id"
  vm_args=(-g "$RG" -n "$VM" -l "$LOCATION" --priority Regular --image Canonical:ubuntu-24_04-lts:server:latest
    --os-disk-name "$VM-osdisk" --os-disk-size-gb 64 --storage-sku StandardSSD_LRS --os-disk-delete-option Delete
    --admin-username azureuser --ssh-key-values "$key/id.pub" --assign-identity '[system]'
    --tags purpose=github-actions-runner repository="$REPO")
  usages=$(az vm list-usage -l "$LOCATION" -o json)
  usage() { jq -r --arg name "$1" '.[] | select(.name.value == $name) | "\(.currentValue) \(.limit)"' <<<"$usages"; }
  read -r cores_used cores_limit < <(usage cores)
  size=
  for candidate in $SIZES; do
    read -r family vcpus restrictions < <(az vm list-skus -l "$LOCATION" --size "$candidate" --resource-type virtualMachines --all -o json |
      jq -r --arg size "$candidate" '.[] | select(.name == $size) |
        "\(.family) \(.capabilities[] | select(.name == "vCPUs") | .value) \([.restrictions[] | select(.type == "Location")] | length)"')
    if [ -z "$family" ]; then
      echo "$candidate: not offered in $LOCATION"
      continue
    fi
    read -r family_used family_limit < <(usage "$family")
    family_used=${family_used:-0} family_limit=${family_limit:-0}
    echo "$candidate: $vcpus vCPUs, $family $family_used/$family_limit, regional $cores_used/$cores_limit, location restrictions: $restrictions"
    [ "$restrictions" = 0 ] || continue
    ((family_limit - family_used >= vcpus && cores_limit - cores_used >= vcpus)) || continue
    # Capacity limits and region policies show only when Azure validates a
    # deployment; this validates one with its own network, creating nothing.
    if err=$(az vm create "${vm_args[@]}" --size "$candidate" --public-ip-sku Standard --nsg-rule NONE \
      --accelerated-networking true --validate -o none 2>&1); then
      size=$candidate
      break
    fi
    echo "$candidate: refused: $(grep -oE '\([A-Z][A-Za-z]+\)' <<<"$err" | grep -vx '(InvalidTemplateDeployment)' |
      sort -u | tr -d '()' | paste -sd ' ')"
  done
  [ -n "$size" ] || { echo "none of $SIZES can be created in $LOCATION" >&2; exit 1; }
  echo "using $size"
fi

step "network"
exists az network nsg show -g "$RG" -n "$VM-nsg" || az network nsg create -g "$RG" -n "$VM-nsg" -l "$LOCATION" -o none
az network nsg rule create -g "$RG" --nsg-name "$VM-nsg" -n DenyAllInbound --priority 100 \
  --direction Inbound --access Deny --protocol '*' --source-address-prefixes '*' --source-port-ranges '*' \
  --destination-address-prefixes '*' --destination-port-ranges '*' \
  --description "Nothing reaches the runner; it is managed through run-command." -o none
exists az network vnet show -g "$RG" -n "$RG-vnet" ||
  az network vnet create -g "$RG" -n "$RG-vnet" -l "$LOCATION" --address-prefixes 10.10.0.0/24 \
    --subnet-name runners --subnet-prefixes 10.10.0.0/27 -o none
az network vnet subnet update -g "$RG" --vnet-name "$RG-vnet" -n runners --nsg "$VM-nsg" --default-outbound false -o none
# The subnet has no default outbound access; the VM reaches the internet
# through its own public IP, which the NSG closes to inbound traffic.
exists az network public-ip show -g "$RG" -n "$VM-ip" ||
  az network public-ip create -g "$RG" -n "$VM-ip" -l "$LOCATION" --sku Standard --allocation-method Static \
    --version IPv4 -o none
exists az network nic show -g "$RG" -n "$VM-nic" ||
  az network nic create -g "$RG" -n "$VM-nic" -l "$LOCATION" --vnet-name "$RG-vnet" --subnet runners \
    --public-ip-address "$VM-ip" --network-security-group "$VM-nsg" --accelerated-networking true -o none

step "vm"
if ! exists az vm show -g "$RG" -n "$VM"; then
  az vm create "${vm_args[@]}" --size "$size" --nics "$VM-nic" -o none
  az vm boot-diagnostics enable -g "$RG" -n "$VM" -o none
fi

step "setup"
release=$(gh api repos/actions/runner/releases/latest)
# shellcheck disable=SC2034 # vm_run reads them by name
{
  RUNNER_VERSION=$(jq -r '.tag_name | ltrimstr("v")' <<<"$release")
  RUNNER_SHA256=$(jq -r .body <<<"$release" | sed -n 's/.*BEGIN SHA linux-x64 -->\([0-9a-f]\{64\}\)<!--.*/\1/p')
}
vm_run setup.sh RUNNER_VERSION RUNNER_SHA256

step "identity for self-deallocation"
vm_id=$(az vm show -g "$RG" -n "$VM" --query id -o tsv)
principal=$(az vm show -g "$RG" -n "$VM" --query identity.principalId -o tsv)
role="CI runner self-deallocation ($RG)"
if [ -z "$(az role definition list --custom-role-only true --name "$role" --query '[0].id' -o tsv 2>/dev/null)" ]; then
  definition=$(jq -n --arg name "$role" --arg scope "$(az group show -n "$RG" --query id -o tsv)" '{
    Name: $name,
    Description: "Read, start and deallocate a VM. Given to a CI runner VM over itself.",
    Actions: ["Microsoft.Compute/virtualMachines/read", "Microsoft.Compute/virtualMachines/start/action",
      "Microsoft.Compute/virtualMachines/deallocate/action"],
    NotActions: [],
    AssignableScopes: [$scope]}')
  if ! err=$(az role definition create --role-definition "$definition" -o none 2>&1); then
    echo "cannot create the custom role: ${err%%$'\n'*}"
    role="Virtual Machine Contributor"
  fi
fi
assigned=
for _ in 1 2 3 4 5 6; do
  if err=$(az role assignment create --assignee-object-id "$principal" --assignee-principal-type ServicePrincipal \
    --role "$role" --scope "$vm_id" -o none 2>&1); then
    assigned=yes
    break
  fi
  # A new role or identity takes a while to replicate; a refusal does not.
  case "$err" in
    *RoleAssignmentExists*)
      assigned=yes
      break
      ;;
    *AuthorizationFailed* | *"does not have authorization"*) break ;;
  esac
  sleep 10
done
if [ -n "$assigned" ]; then
  echo "the VM's managed identity has \"$role\" over the VM"
  REMOVE=1 vm_run install-credentials.sh REMOVE
else
  echo "cannot give the VM's managed identity a role: ${err%%$'\n'*}"
  echo "falling back to the service principal, stored root-only on the VM"
  vm_run install-credentials.sh AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_CLIENT_SECRET
fi
vm_run test-idle-check.sh

step "runner"
register_runner

step "repository secrets and variables"
existing=$(gh secret list -R "$REPO" --json name --jq '.[].name')
for name in AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID AZURE_CLIENT_SECRET; do
  if grep -qx "$name" <<<"$existing"; then
    echo "secret $name exists"
  else
    printf %s "${!name}" | gh secret set "$name" -R "$REPO"
  fi
done
gh variable set RUNNER_RESOURCE_GROUP -R "$REPO" --body "$RG"
gh variable set RUNNER_VM_NAME -R "$REPO" --body "$VM"

step "done"
az vm show -d -g "$RG" -n "$VM" --query '{vm: name, size: hardwareProfile.vmSize, power: powerState, publicIp: publicIps}' -o table
gh api "repos/$REPO/actions/runners" --jq ".runners[] | select(.name == \"$RUNNER_NAME\") |
  \"runner \(.name) (id \(.id)): \(.status), labels \([.labels[].name] | join(\",\"))\""
echo "The VM deallocates itself after 30 idle minutes; wake it with hack/azure-runner/wake.sh."

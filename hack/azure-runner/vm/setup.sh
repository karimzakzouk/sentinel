#!/usr/bin/env bash
# Prepares an Ubuntu 24.04 Azure VM as the host of Sentinel's self-hosted
# GitHub Actions runner: Docker, the Azure CLI, kernel settings for multi-node
# kind clusters, the runner user, the runner itself with its job hooks, the
# idle check that deallocates the VM and the weekly Docker prune. Registering
# the runner is configure-runner.sh's job.
#
# Runs as root with the other files of vm/ beside it, normally through
# create.sh. Safe to rerun: Docker restarts only when its settings change.
#
# RUNNER_VERSION and RUNNER_SHA256 pin the runner release; without them the
# latest release is installed. An installed runner is kept, since it updates
# itself.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
runner_dir=/opt/actions-runner
state=/var/lib/runner-idle
export DEBIAN_FRONTEND=noninteractive
apt_get() {
  apt-get -qq -o DPkg::Lock::Timeout=900 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@" >/dev/null
}
# put FILE MODE writes stdin to FILE and succeeds only when FILE changed.
put() {
  local tmp
  tmp=$(mktemp)
  cat >"$tmp"
  if [ -f "$1" ] && cmp -s "$tmp" "$1"; then
    rm -f "$tmp"
    return 1
  fi
  install -D -m "$2" "$tmp" "$1"
  rm -f "$tmp"
}

cloud-init status --wait >/dev/null 2>&1 || true

echo "--- packages"
install -m 0755 -d /etc/apt/keyrings
curl -fsSL --retry 3 https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
curl -fsSL --retry 3 https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor --yes -o /etc/apt/keyrings/microsoft.gpg
chmod a+r /etc/apt/keyrings/docker.gpg /etc/apt/keyrings/microsoft.gpg
codename=$(. /etc/os-release && echo "$VERSION_CODENAME")
echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $codename stable" \
  >/etc/apt/sources.list.d/docker.list
echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/microsoft.gpg] https://packages.microsoft.com/repos/azure-cli/ $codename main" \
  >/etc/apt/sources.list.d/azure-cli.list
# Unattended security upgrades must not restart Docker or the runner under a job.
put /etc/needrestart/conf.d/90-ci-runner.conf 0644 <<<"\$nrconf{restart} = 'l';" || true
apt_get update
apt_get -y --with-new-pkgs upgrade
apt_get -y install ca-certificates curl git make jq unzip zstd gnupg build-essential \
  docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin azure-cli

echo "--- docker"
if put /etc/docker/daemon.json 0644 <<'EOF'; then
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "3" }
}
EOF
  systemctl restart docker
fi
systemctl enable --now containerd docker >/dev/null 2>&1

echo "--- kernel settings"
put /etc/modules-load.d/ci-runner.conf 0644 <<'EOF' || true
# Bridged traffic between kind nodes' pods must pass netfilter for Services
# and NetworkPolicy; Docker's overlay2 storage needs overlay.
br_netfilter
overlay
EOF
put /etc/sysctl.d/90-ci-runner.conf 0644 <<'EOF' || true
# Every kind node runs its own kubelet, controllers and systemd, and they all
# share the host's inotify limits.
fs.inotify.max_user_instances = 1024
fs.inotify.max_user_watches = 1048576
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
EOF
modprobe br_netfilter
modprobe overlay
sysctl -q -p /etc/sysctl.d/90-ci-runner.conf

echo "--- runner user"
id runner >/dev/null 2>&1 || useradd --create-home --shell /bin/bash runner
usermod -aG docker runner
put /etc/sudoers.d/90-runner 0440 <<'EOF' || true
# CI scripts install tools and load kernel modules with sudo.
runner ALL=(ALL) NOPASSWD:ALL
EOF
visudo -cqf /etc/sudoers.d/90-runner
install -d -o runner -g runner -m 0755 "$state" "$state/image-use"
# Setup counts as activity, so the idle check leaves time to register and verify.
runuser -u runner -- touch "$state/last-active"

echo "--- runner"
if [ ! -x "$runner_dir/bin/Runner.Listener" ]; then
  if [ -z "${RUNNER_VERSION:-}" ]; then
    release=$(curl -fsSL --retry 3 https://api.github.com/repos/actions/runner/releases/latest)
    RUNNER_VERSION=$(jq -r .tag_name <<<"$release")
    RUNNER_VERSION=${RUNNER_VERSION#v}
    RUNNER_SHA256=$(jq -r .body <<<"$release" | sed -n 's/.*BEGIN SHA linux-x64 -->\([0-9a-f]\{64\}\)<!--.*/\1/p')
  fi
  file=actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz
  curl -fsSL --retry 3 -o "/tmp/$file" "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/$file"
  echo "${RUNNER_SHA256:?no checksum for runner $RUNNER_VERSION}  /tmp/$file" | sha256sum -c --quiet -
  install -d -o runner -g runner -m 0755 "$runner_dir"
  tar -C "$runner_dir" -xzf "/tmp/$file"
  rm -f "/tmp/$file"
fi
# It tries newer package names first and reports each one missing.
if ! out=$("$runner_dir/bin/installdependencies.sh" 2>&1); then
  echo "$out" >&2
  exit 1
fi
install -d -m 0755 "$runner_dir/hooks"
install -m 0755 "$here/job-started.sh" "$here/job-completed.sh" "$runner_dir/hooks/"
# The runner loads .env into its own environment, and so into every job.
set_env() {
  touch "$runner_dir/.env"
  sed -i "/^$1=/d" "$runner_dir/.env"
  echo "$1=$2" >>"$runner_dir/.env"
}
set_env LANG C.UTF-8
set_env ACTIONS_RUNNER_HOOK_JOB_STARTED "$runner_dir/hooks/job-started.sh"
set_env ACTIONS_RUNNER_HOOK_JOB_COMPLETED "$runner_dir/hooks/job-completed.sh"
chown -R runner:runner "$runner_dir"
chown -R root:root "$runner_dir/hooks"

echo "--- idle check and Docker prune"
install -m 0755 "$here/runner-idle-check.sh" /usr/local/sbin/runner-idle-check
install -m 0755 "$here/runner-docker-prune.sh" /usr/local/sbin/runner-docker-prune
install -D -m 0755 "$here/record-image-use.sh" /usr/local/lib/runner-idle/record-image-use
install -m 0644 "$here/runner-idle-check.service" "$here/runner-idle-check.timer" \
  "$here/runner-docker-prune.service" "$here/runner-docker-prune.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now runner-idle-check.timer runner-docker-prune.timer >/dev/null 2>&1

echo "--- installed"
docker --version
echo "azure-cli $(az version --query '"azure-cli"' -o tsv 2>/dev/null)"
git --version
echo "runner $("$runner_dir/bin/Runner.Listener" --version)"
uname -r
sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches \
  net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables

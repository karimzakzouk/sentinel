#!/usr/bin/env bash
# Starts the Docker daemon on machines without systemd, such as Cloud Agent VMs.
# Safe to rerun.
set -euo pipefail

if ! sudo docker info >/dev/null 2>&1; then
  sudo rm -f /var/run/docker.pid
  sudo sh -c 'nohup dockerd >/var/log/dockerd.log 2>&1 &'
  for _ in $(seq 1 60); do
    sudo docker info >/dev/null 2>&1 && break
    sleep 1
  done
fi

# Some VM images carry iptables-legacy rules whose FORWARD policy drops traffic
# from Docker bridges that the nftables rules accept. Both rule sets apply, so
# kind nodes lose internet access and cannot pull images.
if command -v iptables-legacy >/dev/null && sudo iptables-legacy -S FORWARD 2>/dev/null | grep -q '^-P FORWARD DROP'; then
  for dir in -i -o; do
    sudo iptables-legacy -C FORWARD "$dir" br-+ -j ACCEPT 2>/dev/null ||
      sudo iptables-legacy -I FORWARD "$dir" br-+ -j ACCEPT
  done
fi

# Agent shells are not guaranteed to pick up docker group membership.
sudo chmod 666 /var/run/docker.sock
docker info --format 'docker {{.ServerVersion}} ready ({{.Driver}}, cgroup v{{.CgroupVersion}})'

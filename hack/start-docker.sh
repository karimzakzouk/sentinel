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

# Agent shells are not guaranteed to pick up docker group membership.
sudo chmod 666 /var/run/docker.sock
docker info --format 'docker {{.ServerVersion}} ready ({{.Driver}}, cgroup v{{.CgroupVersion}})'

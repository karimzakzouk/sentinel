#!/usr/bin/env bash
# Registers the runner in /opt/actions-runner with a repository, taking over
# any runner of the same name, and runs it as a systemd service under the
# runner user. Runs as root after setup.sh, normally through register.sh;
# safe to rerun.
#
#   RUNNER_TOKEN   registration token, from
#                  POST /repos/{owner}/{repo}/actions/runners/registration-token
#   RUNNER_URL     https://github.com/{owner}/{repo}
#   RUNNER_NAME    runner name
#   RUNNER_LABELS  labels besides self-hosted, Linux and X64, comma-separated
set -euo pipefail
: "${RUNNER_TOKEN:?}" "${RUNNER_URL:?}" "${RUNNER_NAME:?}" "${RUNNER_LABELS:?}"

cd /opt/actions-runner
path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin

if [ -f .service ]; then
  ./svc.sh stop >/dev/null 2>&1 || true
  ./svc.sh uninstall >/dev/null 2>&1 || true
fi
# Forget an earlier registration; --replace takes the name over on GitHub.
rm -f .runner .credentials .credentials_rsaparams

# The runner reads ACTIONS_RUNNER_INPUT_<ARG> for any argument, which keeps
# the token off the command line.
export ACTIONS_RUNNER_INPUT_TOKEN=$RUNNER_TOKEN
unset RUNNER_TOKEN
runuser -u runner -- env PATH="$path" HOME=/home/runner ./config.sh --unattended --replace \
  --url "$RUNNER_URL" --name "$RUNNER_NAME" --labels "$RUNNER_LABELS" --work _work
unset ACTIONS_RUNNER_INPUT_TOKEN

# config.sh records its own PATH as the jobs' PATH.
echo "$path" >.path
chown runner:runner .path

# svc.sh refuses to install over a unit file that a failed run left behind.
./svc.sh uninstall >/dev/null 2>&1 || true
./svc.sh install runner >/dev/null
unit=$(cat .service)
install -D -m 0644 /dev/stdin "/etc/systemd/system/$unit.d/10-ci.conf" <<'EOF'
[Unit]
# Jobs use Docker from their first step.
After=docker.service network-online.target
Wants=docker.service network-online.target

[Service]
# As on GitHub-hosted runners.
LimitNOFILE=65536
EOF
systemctl daemon-reload
./svc.sh start >/dev/null
sleep 5
echo "$unit: $(systemctl is-active "$unit")"

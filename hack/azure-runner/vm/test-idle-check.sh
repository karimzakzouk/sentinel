#!/usr/bin/env bash
# Exercises runner-idle-check without deallocating anything: it must be
# allowed to deallocate the VM, a dry run with its limits at zero must decide
# to deallocate, and the same dry run must stay up while a process named
# Runner.Worker runs. A last dry run with the real settings shows what the
# timer would decide now. Prints the journal lines it wrote, also on failure.
# Runs as root through vm-run.sh.
set -euo pipefail
hold=/var/lib/runner-idle/hold-until
since=$(date '+%Y-%m-%d %H:%M:%S')
held=$(cat "$hold" 2>/dev/null || true)
fake=
finish() {
  [ -z "$fake" ] || kill "$fake" 2>/dev/null || true
  rm -f /tmp/Runner.Worker
  [ -z "$held" ] || echo "$held" >"$hold"
  journalctl -t runner-idle-check --since "$since" -o short-iso --no-pager
}
trap finish EXIT
sleep 1

runner-idle-check --check-auth >/dev/null
rm -f "$hold"
IDLE_MINUTES=0 MIN_UPTIME_MINUTES=0 WAKE_GRACE_MINUTES=0 runner-idle-check --dry-run >/dev/null

cp /bin/sleep /tmp/Runner.Worker
/tmp/Runner.Worker 120 &
fake=$!
IDLE_MINUTES=0 MIN_UPTIME_MINUTES=0 WAKE_GRACE_MINUTES=0 runner-idle-check --dry-run >/dev/null
kill "$fake"
wait "$fake" 2>/dev/null || true
fake=
rm -f /tmp/Runner.Worker

[ -z "$held" ] || echo "$held" >"$hold"
runner-idle-check --dry-run >/dev/null

#!/usr/bin/env bash
# Runner hook, ACTIONS_RUNNER_HOOK_JOB_COMPLETED in /opt/actions-runner/.env:
# marks the runner active for runner-idle-check. It must never fail the job.
touch /var/lib/runner-idle/last-active || true
/usr/local/lib/runner-idle/record-image-use || true
exit 0

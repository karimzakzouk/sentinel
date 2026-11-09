#!/usr/bin/env bash
# Runner hook, ACTIONS_RUNNER_HOOK_JOB_STARTED in /opt/actions-runner/.env:
# marks the runner active for runner-idle-check and clears the clusters and
# containers earlier jobs left behind. Docker images stay: they are the cache.
# It must never fail the job.
touch /var/lib/runner-idle/last-active || true
/usr/local/lib/runner-idle/record-image-use || true
if command -v kind >/dev/null; then
  kind delete clusters --all || true
fi
if command -v k3d >/dev/null; then
  k3d cluster delete --all || true
fi
if command -v minikube >/dev/null; then
  minikube delete --all || true
fi
docker container prune --force >/dev/null || true
exit 0

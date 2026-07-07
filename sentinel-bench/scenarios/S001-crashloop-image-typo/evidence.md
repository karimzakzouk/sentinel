# S001 — CrashLoopBackOff due to image tag typo

## What broke

The `sample-app` Deployment in the `prod` namespace enters CrashLoopBackOff
within ~30s of being applied. `kubectl describe pod` shows:

```
Warning  Failed          kubelet  Error: ImagePullBackOff
  Failed to pull image "ghcr.io/sentinel-bench/sample-python-app:v1.2.3":
  rpc error: code = Unknown desc = Error response from registry:
  manifest unknown
```

## Why it broke

The image tag in `deployment/k8s.yaml` is `v1.2.3`. The actual published tag
in the container registry is `v1.23` — a single-character typo (extra `.`).
The registry returns `manifest unknown`, kubelet marks the pod
`ImagePullBackOff`, which transitions to `CrashLoopBackOff` after the
configured back-off.

## The fix

One-line patch: change `v1.2.3` -> `v1.23` in `deployment/k8s.yaml`.

## Why this is a "trivial" scenario

- The error message is unambiguous (`manifest unknown`).
- The fix is a single character.
- No code logic is involved — pure config.
- Any agent that can read `kubectl describe` output and grep the repo for
  the image string should solve this in <2 minutes.

We include trivial scenarios because they establish a floor: if an agent
can't solve S001, it can't solve anything. The DBR/VPR/TAR/FMR deltas
between trivial and moderate scenarios reveal whether the agent is
pattern-matching on common error strings vs. actually investigating.

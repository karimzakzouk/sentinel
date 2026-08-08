# S002 — Flask app crashes with KeyError on misspelled DATABASE_URL env var

## What broke

The `payments-api` Deployment in the `prod` namespace enters CrashLoopBackOff
within ~10 seconds of being scheduled. The container log shows a Python
traceback on import:

```
Traceback (most recent call last):
  File "/app/app.py", line 12, in <module>
    db_url = os.environ['DATABASE_URL']
             ~~~~~~~~~~^^^^^^^^^^^^^^^
  File "<frozen os>", line 685, in __getitem__
KeyError: 'DATABASE_URL'
```

The alert that triggers Sentinel is a `KubePodCrashLooping` page with
`restarts: 9` and `severity: critical`. The annotations mention a Python
KeyError traceback.

## Why it broke

The Flask app reads the database URL with `os.environ['DATABASE_URL']`
(single-L). The Deployment manifest in `deployment/k8s.yaml` defines the env
var as `DATABASE_URLL` (double-L) — a single-character typo.

The typo means the env var the app expects is never set, so
`os.environ['DATABASE_URL']` raises `KeyError` on import, the container
exits non-zero, kubelet restarts it, and after the configured back-off the
pod enters CrashLoopBackOff.

The bug lives in the source repo at `deployment/k8s.yaml`. The cluster
manifest applied during scenario setup mirrors the same broken state.

## The fix

One-line patch: rename `DATABASE_URLL` -> `DATABASE_URL` in
`deployment/k8s.yaml`.

```diff
-           - name: DATABASE_URLL
+           - name: DATABASE_URL
              value: "postgres://payments:****@db.prod.svc.cluster.local:5432/payments"
```

Renaming in the manifest is the right fix (not changing the app code),
because `DATABASE_URL` is the conventional name and what every other service
in the org expects.

## Why this is a "moderate" scenario

- The error message (`KeyError: 'DATABASE_URL'`) is unambiguous about which
  env var is missing, but does NOT tell you *why* it's missing.
- The agent has to cross-reference the traceback (`DATABASE_URL`) against
  the Deployment manifest (`DATABASE_URLL`) and notice the single-character
  difference.
- The fix is one line, but the diagnosis requires reading both the
  container log AND the source repo's manifest — not just greping for an
  image string.
- Tests the multi-language error parser (Python tracebacks with the
  PEP-657 caret marker `^^^^`) and the env-var hypothesis generator.

A trivial agent that only greps for "ImagePullBackOff" will fail this
scenario entirely; the failure mode is application-level, not kubelet-level.

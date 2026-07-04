# Example: Python CrashLoopBackOff → Auto-fix

This example shows Sentinel detecting a real production crash (TypeError in a Python Flask app), diagnosing it, writing a fix, verifying it in the sandbox, and opening a PR — all autonomously, in ~4 minutes.

## Prerequisites

- `kind` (Kubernetes in Docker) — `brew install kind` or https://kind.sigs.k8s.io/
- `helm` 3.x
- A GitHub Personal Access Token with `repo` scope
- An LLM API key (any OpenAI-compatible provider)

## Steps

### 1. Install Sentinel

```bash
helm repo add sentinel https://karimzakzouk.github.io/sentinel/
helm repo update

helm install sentinel sentinel/sentinel --version 5.10.1 \
  --namespace sentinel --create-namespace \
  --set sentinel.githubToken=YOUR_GITHUB_TOKEN \
  --set sentinel.llm.apiKey=YOUR_LLM_API_KEY \
  --set sentinel.llm.provider=gemini \
  --set 'sentinel.rbac.writeNamespaces={sim}'
```

Wait for Sentinel to be ready:

```bash
kubectl -n sentinel get pods -w
# sentinel-xxx   1/1 Running   0   1s
```

### 2. Deploy the broken app

```bash
kubectl apply -f broken-app.yaml -n sim
```

The app has a deliberate bug — it tries to concatenate a string with an integer:

```python
# app.py (buggy line)
return f"Price: " + price  # TypeError: can only concatenate str (not "int") to str
```

The pod will CrashLoopBackOff immediately.

### 3. Enroll the service

Annotate the Deployment so Sentinel knows where to find the source code:

```bash
kubectl -n sim annotate deployment payments-api \
  sentinel.io/repo=karimzakzouk/demo-app \
  sentinel.io/branch=main
```

### 4. Watch Sentinel fix it

```bash
# Watch the incident appear in Sentinel's API
kubectl -n sentinel port-forward svc/sentinel 8000:8000 &

# Open http://localhost:8000 — you'll see:
#   1. Incident appears (TypeError detected)
#   2. Investigation starts (clones repo, searches code)
#   3. Root cause identified (string + int concatenation)
#   4. Fix proposed (f-string conversion)
#   5. Sandbox verification (L1: pod Ready, L2: health OK, L3: error gone)
#   6. PR opened on your demo repo with `sandbox-verified` label
```

### 5. Merge the PR

Sentinel opens a PR like this:

```
Title: [Sentinel] Fix TypeError in app.py
Labels: sandbox-verified, sentinel-generated

## Summary
Detected TypeError in payments-api (sim namespace): can only concatenate
str (not "int") to str at app.py:42.

## Root Cause
The function `format_price(price)` returns `f"Price: " + price` where
`price` is an integer. Python cannot concatenate str + int.

## Fix
Convert to f-string: `f"Price: {price}"`

## Sandbox Verification
✅ Level 1: Canary pod Ready (sustained 30s)
✅ Level 2: Health endpoints returned 2xx
✅ Level 3: Original error gone from canary logs
```

Merge it — Sentinel's auto-merge gates already passed, so if you have auto-merge enabled, it'll merge itself.

## What just happened

1. **Detection** (0s): Sentinel's live log streamer saw the TypeError in the pod's logs the instant it crashed.
2. **Investigation** (15s): Sentinel cloned your repo, walked the file tree, ran regex searches for `format_price`, read `app.py`, and asked the LLM "what's the root cause?"
3. **Fix proposal** (30s): The LLM generated a SEARCH/REPLACE block converting `f"Price: " + price` to `f"Price: {price}"`.
4. **Sandbox verification** (2 min): Sentinel built a canary image with Kaniko, deployed it to an isolated namespace, and verified the fix across 3 levels.
5. **PR** (3 min): Sentinel opened a PR with the `sandbox-verified` label.

**Total time: ~4 minutes. Zero human intervention. Cost: ~$0.003 in LLM tokens.**

## Cleanup

```bash
helm uninstall sentinel -n sentinel
kind delete cluster
```

## Try more bugs

Edit `broken-app.yaml` and try these:

- `result = 100 / user_count` (ZeroDivisionError when user_count=0)
- `data = json.loads(response)` (json.JSONDecodeError on malformed response)
- `db = psycopg2.connect(os.environ["DATABASE_URL"])` (KeyError when env var missing)

Sentinel's error parser handles all of these — it's language-agnostic (Python, Go, Java, JavaScript, Rust, Ruby).

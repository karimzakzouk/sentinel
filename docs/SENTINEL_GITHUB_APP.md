# Sentinel GitHub App — Bot Identity

Sentinel can use a **GitHub App** instead of a personal access token (PAT) for all GitHub operations. This makes PRs, comments, reactions, and merges appear as **`Sentinel[bot]`** instead of your personal GitHub account.

This doc covers the three ways to set it up. Pick the one that matches your environment.

## TL;DR — pick your path

| Path | When to use | Effort | Public URL needed? |
|------|-------------|--------|---------------------|
| [A. `sentinelctl app setup`](#a-sentinelctl-app-setup-recommended) | Self-hosted Sentinel, local dev, GitHub Enterprise | 30 sec | No |
| [B. Public Sentinel App](#b-public-sentinel-app-one-click-install) | Cloud-hosted Sentinel, you want zero-config for users | 2 clicks (per user) | Yes |
| [C. Manual App via env vars](#c-manual-app-via-env-vars) | You want full control, no CLI | 5 min | No |
| [PAT (no App)](#no-app-pat-fallback) | Quick demo, testing | 0 min | No |

If you don't configure any App, Sentinel silently falls back to the PAT. **Nothing breaks.** You just see your own username on PRs instead of `Sentinel[bot]`.

---

## A. `sentinelctl app setup` (recommended)

Creates a GitHub App **in your own account** with all permissions pre-configured. One command, one click on GitHub, done.

### Prerequisites

- `sentinelctl` installed (built from this repo, or `brew install sentinel` once we ship a tap)
- Sentinel running + reachable from your laptop (port-forward is fine)
- The `SENTINEL_API_TOKEN` (from your Helm install)

### Steps

1. **Port-forward to Sentinel** (if not already public):
   ```bash
   kubectl -n sentinel port-forward svc/sentinel 8000:8000
   ```

2. **Run the setup command:**
   ```bash
   sentinelctl app setup \
     --server http://localhost:8000 \
     --token $SENTINEL_API_TOKEN
   ```

3. **Your browser opens** to `https://github.com/settings/apps/new?manifest=...` with a pre-filled form. Click **"Create GitHub App"**.

4. **GitHub redirects back** to your terminal. `sentinelctl` receives the App credentials (ID + PEM + webhook secret) and sends them to Sentinel.

5. **Your browser opens** to the install URL. Click **Install** and pick the repo(s) Sentinel should access.

6. **After install**, look at the URL in your browser:
   ```
   https://github.com/settings/installations/12345678
                                        ^^^^^^^^
                                        this is the install ID
   ```

7. **Set the install ID:**
   ```bash
   sentinelctl app set-install 12345678 \
     --server http://localhost:8000 \
     --token $SENTINEL_API_TOKEN
   ```

Done. PRs + comments will now appear as `Sentinel[bot]` (or whatever you named the App).

### Verify

```bash
sentinelctl app status --server http://localhost:8000 --token $SENTINEL_API_TOKEN
```

Output:
```
GitHub App: configured ✓
  App ID:        1234567
  Bot identity:  sentinel[bot]
  Install ID:    12345678
```

### GitHub Enterprise

Add `--github-host` to point at your GHES instance:

```bash
sentinelctl app setup --github-host github.example.com ...
```

The manifest flow works the same way on GHES.

### Remove

```bash
sentinelctl app remove --server http://localhost:8000 --token $SENTINEL_API_TOKEN
```

Sentinel falls back to the PAT.

---

## B. Public Sentinel App (one-click install)

If you're a maintainer shipping Sentinel as a service, you can create **one** public GitHub App that all your users install in 2 clicks. No CLI, no PEM files.

### Maintainer setup (you do this once)

1. Go to https://github.com/settings/apps/new (or your org's settings)
2. Fill in:
   - **Name:** `Sentinel`
   - **Homepage URL:** `https://github.com/yourname/sentinel`
   - **Webhook URL:** `https://your-sentinel-deployment.example.com/webhook/github`
   - **Webhook secret:** a random string (also set as `SENTINEL_GITHUB_WEBHOOK_SECRET` in your Helm values)
3. **Repository permissions:**
   - Contents: **Read and write**
   - Pull requests: **Read and write**
   - Issues: **Read and write**
   - Metadata: **Read-only**
   - Commit statuses: **Read-only**
4. **Subscribe to events:**
   - Issue comment
   - Pull request
   - Check run
   - Status
   - Installation
5. **Make it public:** check "Any user can install this GitHub App"
6. Click **Create GitHub App**
7. Generate a private key (.pem file)
8. Note the **App ID** (shown on the app settings page)
9. Install the App on your own repo/org to get the **installation ID**

### Configure your Sentinel deployment

```yaml
sentinel:
  githubToken: "..."  # PAT as fallback
  githubAppId: "1234567"
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
  githubAppInstallId: "12345678"
  githubWebhookSecret: "..."
```

### User install flow (what your users see)

1. User visits `https://github.com/apps/yourname-sentinel/installations/new`
2. Picks their repo → clicks Install
3. GitHub sends an `installation` webhook to your Sentinel
4. Sentinel auto-captures the install ID + starts using the App for that user's PRs

**Note:** your Sentinel must be publicly reachable for step 3 to work. If it's not, users have to paste the install ID manually via `sentinelctl app set-install`.

---

## C. Manual App via env vars

If you don't want to use the CLI (path A) and don't want to publish a public App (path B), you can create the App manually and configure Sentinel via env vars.

### Steps

1. Go to https://github.com/settings/apps/new
2. Configure permissions + events as listed in [path B](#maintainer-setup-you-do-this-once) above
3. Generate a private key (.pem file)
4. Install the App on your repo
5. Note the **App ID** + **install ID** (from the install URL)
6. Set these env vars (or Helm values) on your Sentinel deployment:

```bash
SENTINEL_GITHUB_APP_ID=1234567
SENTINEL_GITHUB_APP_PRIVATE_KEY=$(cat sentinel-private-key.pem)
SENTINEL_GITHUB_APP_INSTALL_ID=12345678
```

Or via Helm:

```yaml
sentinel:
  githubToken: "..."  # PAT as fallback
  githubAppId: "1234567"
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
  githubAppInstallId: "12345678"
```

Restart Sentinel. PRs + comments will appear as `Sentinel[bot]`.

---

## No App (PAT fallback)

If you don't configure any GitHub App, Sentinel uses the `SENTINEL_GITHUB_TOKEN` PAT for all operations. PRs + comments appear as the PAT owner's username.

This is the default. Nothing to configure. Works everywhere — local dev, cloud, Enterprise, air-gapped.

To switch to bot identity later, run `sentinelctl app setup` (path A) or set the env vars (path C). No code changes, no restart needed (for path A).

---

## How the fallback works

Sentinel checks for App credentials in this order:

1. **Runtime config** (set via `sentinelctl app setup` or the web UI Settings page) — highest priority
2. **Env vars** (`SENTINEL_GITHUB_APP_*`) — checked at startup
3. **PAT** (`SENTINEL_GITHUB_TOKEN`) — always works, no bot identity

When the App is configured but the App token call fails (e.g. App revoked, GitHub API down), Sentinel silently falls back to the PAT. **It never blocks on the App** — if the App is broken, your PAT keeps things running.

## What you'll see

| | PAT (no App) | GitHub App |
|---|---|---|
| PR author | `@yourname` | `@sentinel[bot]` |
| Comment author | `@yourname` | `@sentinel[bot]` |
| Emoji reactions | `@yourname` | `@sentinel[bot]` |
| "Verified" badge | No | Yes |
| Token expiry | 90 days (or never if you set it) | Never (auto-refresh, 1h tokens) |
| Rate limit | 5000/hr (shared with your account) | 5000/hr per installation |

## Troubleshooting

### PRs still show as my username after `sentinelctl app setup`

Check `sentinelctl app status`. If it says `Install ID: <empty>`, you forgot step 7 — run `sentinelctl app set-install <id>`.

### `sentinelctl app setup` says "timed out waiting for callback"

The localhost callback server didn't receive the GitHub redirect within 5 minutes. Causes:
- You closed the browser before clicking "Create GitHub App"
- Your browser is configured to block `localhost` redirects (rare)
- Network issue

Re-run `sentinelctl app setup`. The code is single-use, so a fresh run is required.

### `sentinelctl app setup` says "401 — pass --token"

Your Sentinel has `SENTINEL_API_TOKEN` set (admin auth). Pass `--token $SENTINEL_API_TOKEN` to authenticate.

### App token call fails after setup

Sentinel logs the error and falls back to the PAT. Check the logs:
```bash
kubectl -n sentinel logs deploy/sentinel | grep -i "github app"
```

Common causes:
- App was revoked (user uninstalled it)
- App's private key was rotated (re-run `sentinelctl app setup`)
- App's installation ID changed (re-run `sentinelctl app set-install`)

## Permissions reference

The manifest flow (path A) configures these permissions automatically:

| Permission | Level | Why |
|------------|-------|-----|
| Contents | Read + Write | Clone repo, push fix branches |
| Pull requests | Read + Write | Open PRs, post comments, react, merge |
| Issues | Read + Write | Post comments, react, label management (PRs are issues in the API) |
| Metadata | Read-only | Required by GitHub |
| Commit statuses | Read-only | Read CI status for auto-merge |
| Members | Read-only | Permission checks for chatops |
| Organization administration | Read-only | Permission checks in org repos |

Events subscribed:

| Event | Why |
|-------|-----|
| Issue comment | Chatops commands (`/sentinel <cmd>`) |
| Pull request | Predictive PR review |
| Check run | CI status for auto-merge |
| Status | Legacy CI status |
| Installation | Auto-capture install ID when user installs the App |

#!/usr/bin/env bash
# Signs a person in to the console through a real identity provider
# (internal/api's live OIDC test): starts Dex in Docker on 127.0.0.1:15556
# with the user alice@example.com, password "password", and a client whose
# secret stays in a file, then signs in as a browser would. Dex is removed
# afterwards.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dir=$(mktemp -d)
umask 077
trap 'docker rm -f sentinel-dex >/dev/null 2>&1 || true; rm -rf "$dir"' EXIT
head -c 24 /dev/urandom | base64 | tr -d '/+=' >"$dir/secret"
listen=127.0.0.1:18095
cat >"$dir/config.yaml" <<EOF
issuer: http://127.0.0.1:15556/dex
storage: {type: memory}
web: {http: 0.0.0.0:5556}
oauth2: {skipApprovalScreen: true}
staticClients:
  - id: sentinel
    name: Sentinel
    secret: $(cat "$dir/secret")
    redirectURIs: ["http://$listen/api/session/oidc/callback"]
enablePasswordDB: true
staticPasswords:
  # Dex's own example user; its password is "password".
  - email: alice@example.com
    hash: "\$2a\$10\$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W"
    username: alice
    userID: 08a8684b-db88-4b73-90a9-3cd1661f5466
EOF
chmod 644 "$dir/config.yaml"
docker run -d --name sentinel-dex -p 127.0.0.1:15556:5556 -v "$dir/config.yaml:/etc/dex/config.yaml:ro" \
  ghcr.io/dexidp/dex:v2.44.0 dex serve /etc/dex/config.yaml >/dev/null
for _ in $(seq 30); do
  curl -fsS -o /dev/null http://127.0.0.1:15556/dex/.well-known/openid-configuration 2>/dev/null && break
  sleep 1
done
OIDC_ISSUER=http://127.0.0.1:15556/dex OIDC_LISTEN=$listen OIDC_CLIENT_SECRET_FILE=$dir/secret \
  go test -C "$root" -count=1 -tags live -run TestLiveOIDC -v ./internal/api/ | grep -E 'signed in|PASS|FAIL|Error|led to|after the'

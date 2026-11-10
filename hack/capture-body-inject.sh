#!/usr/bin/env bash
# Posts a minimal OTLP/JSON trace with body attributes to Sentinel's capture
# receiver. Use to prove the store keeps bodies when EnabledBodies is on,
# without OBI on a cluster. Example:
#   CAPTURE_URL=http://127.0.0.1:4318/v1/traces CAPTURE_TOKEN=... ./hack/capture-body-inject.sh
set -euo pipefail
url="${CAPTURE_URL:-http://127.0.0.1:4318/v1/traces}"
token="${CAPTURE_TOKEN:?set CAPTURE_TOKEN to the sentinel-capture bearer token}"
body='{"resourceSpans":[{"resource":{"attributes":[
  {"key":"k8s.namespace.name","value":{"stringValue":"shop"}},
  {"key":"k8s.deployment.name","value":{"stringValue":"cart"}}]},
  "scopeSpans":[{"spans":[{"name":"POST /checkout","kind":2,
    "startTimeUnixNano":"1762700000000000000","endTimeUnixNano":"1762700000100000000",
    "attributes":[
      {"key":"http.request.method","value":{"stringValue":"POST"}},
      {"key":"url.path","value":{"stringValue":"/checkout"}},
      {"key":"http.response.status_code","value":{"intValue":"200"}},
      {"key":"http.request.body","value":{"stringValue":"{\"sku\":\"widget\"}"}},
      {"key":"http.response.body.content","value":{"stringValue":"{\"ok\":true}"}}
    ]}]}]}]}'
curl -fsS -X POST "$url" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${token}" \
  -d "$body"
echo "posted body-bearing trace to $url"

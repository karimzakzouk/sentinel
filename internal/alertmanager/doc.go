// Package alertmanager implements an Alertmanager webhook receiver.
//
// Alertmanager sends webhook notifications to a configured URL when an alert
// fires. Sentinel receives them at POST /webhook/alertmanager and converts
// each firing alert into a Signal that the detection clusterer deduplicates
// and groups.
//
// The Alertmanager webhook payload format (v4) is documented at
// https://prometheus.io/docs/alerting/latest/configuration/#webhook_config.
// From each firing alert Sentinel extracts:
//
//   - service:   labels.service (falling back to job/deployment/pod, then "unknown")
//   - namespace: labels.namespace (defaults to "default")
//   - reason:    alertname + annotations.summary
//   - repo:      labels.repo (optional — when absent the watcher looks it
//     up via K8s annotations)
//   - description: annotations.description + runbook_url + dashboard + labels
//
// Only "firing" alerts trigger investigation. "resolved" alerts are counted
// but do not trigger — the original firing alert already started an
// investigation, and resolution is handled by the outcome watcher.
//
// A shared-secret header (X-Sentinel-Webhook-Secret) gates the receiver so
// unauthenticated callers cannot inject alerts. The secret is compared in
// constant time to avoid timing-side-channel attacks.
//
// Duplicate firing alerts (same Fingerprint) are deduplicated within a
// cooldown window (default 5 minutes) so Alertmanager's repeat_interval
// doesn't re-trigger an investigation that is already in flight.
package alertmanager

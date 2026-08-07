// Package chaos implements Sentinel's self-chaos mode: scheduled chaos
// experiments against staging namespaces that Sentinel then investigates.
//
// Every serious SRE tool needs eval data. Sentinel-Bench is the public
// eval harness, but it needs scenarios to run. The best scenarios are
// real incidents — but real incidents are rare and you cannot wait for
// them. This package runs scheduled chaos experiments against an
// allow-list of staging namespaces:
//
//   - kill-pod: deletes a random pod (Deployment controller recreates it)
//   - mutate-configmap: adds a bad key to a ConfigMap a workload depends on
//   - restart-deployment: triggers a rollout restart
//
// After each injection, the runner invokes a caller-supplied trigger
// callback that starts Sentinel's own investigation pipeline against the
// synthetic incident. Sentinel investigates its own injection. The
// outcome (detected? root-caused? fixed? auto-merged?) is recorded as
// eval data for sentinel-bench and as a few-shot example for the
// feedback store.
//
// # SAFETY
//
// Chaos runs ONLY against namespaces in config.ChaosNamespaces.
// Production namespaces are never touched. DryRun mode logs what would
// happen without actually calling the K8s API. Every experiment returns
// a Restore func that the runner calls if Sentinel fails to fix the
// injection within the timeout (so staging is never left broken).
//
// The runner is nil-safe for the attestor: if SetAttestor is not called,
// no attestation is signed (the audit trail just has fewer entries).
package chaos

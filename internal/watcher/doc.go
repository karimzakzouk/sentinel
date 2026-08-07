// Package watcher implements the SentinelIncident CRD controller.
//
// Operators can trigger a Sentinel investigation manually by applying a
// SentinelIncident CR:
//
//	apiVersion: sentinel.io/v1alpha1
//	kind: SentinelIncident
//	metadata:
//	  name: payments-2026-06-29
//	  namespace: sentinel
//	spec:
//	  service: payments-api
//	  namespace: production
//	  reason: "manual trigger from on-call"
//
// The watcher uses a real Kubernetes Watch() stream for ~1s detection
// latency. On startup it processes any existing CRs that have no status
// (so a Sentinel restart re-triggers pending CRs). On each new or modified
// CR with no status, it patches status.phase to "investigating" (so the
// same CR isn't re-triggered) and invokes the Handler with a synthetic
// AnomalyCluster assembled from the CR spec + pod logs fetched from the
// crashing pods.
//
// The Handler is the callback into the pipeline — it builds the cluster,
// fetches pod logs via FetchCrashingPodLogs, attaches sentinel.io/*
// annotations discovered on the workload, and runs the investigation in a
// goroutine so the watch loop isn't blocked.
package watcher

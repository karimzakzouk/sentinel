// Package safeactions implements the L2 remediation controller that every
// write action flows through when a fix proposal's verdict is Allow.
//
// Three actions are supported — Restart, Rollback, Scale — and each runs as
// deterministic Go code against the K8s API (no free-form kubectl). Two
// gates apply to every action:
//
//   - Namespace allowlist (Config.AllowedNamespaces): actions in any other
//     namespace are rejected without contacting the cluster.
//   - Rate limit (Config.MaxActionsPerHour): a sliding 1-hour window. When
//     exceeded, the action is rejected with a "rate limit exceeded" detail.
//
// Scale additionally enforces a blast-radius cap (Config.MaxScaleDeltaPct):
// a percentage change larger than the cap is rejected with a detail
// instructing the caller to escalate to human approval.
//
// The controller takes a kubernetes.Interface (not a *kubernetes.Clientset)
// so tests can drive it with k8s.io/client-go/kubernetes/fake.
package safeactions

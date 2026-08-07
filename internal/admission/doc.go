// Package admission implements Sentinel as a Kubernetes validating admission
// webhook.
//
// Right now Sentinel reacts to crashes (internal/detection) and to alerts
// (internal/alertmanager). That's the bottom of the cliff. The top of the
// cliff is preventing the bad deploy from ever reaching the cluster.
//
// Kyverno and OPA Gatekeeper are the incumbents for admission control. They
// enforce *static* policies — "no privileged pods", "must set resource
// limits". They cannot enforce *learned* policies — "this image has the same
// pattern as the one that caused incident #47" or "this Deployment lacks the
// rate-limiting annotation that would have prevented the 504s we saw last
// week."
//
// Sentinel's admission mode combines:
//
//  1. Static policies (parity with Kyverno/Gatekeeper — table stakes)
//  2. CVE scanning (block images with unfixed critical vulns at admission time)
//  3. Semantic similarity to past incidents (the unique Sentinel value-add)
//
// This is the move that lets Sentinel honestly claim "prevents incidents"
// rather than just "fixes incidents." It converts Sentinel from a remediation
// tool into a governance tool — and it's the same control plane Kyverno
// occupies, except with a memory.
//
// # Safety
//
// The webhook NEVER mutates — it only validates. Mutation requires a
// MutatingWebhookConfiguration and a separate code path; we intentionally
// start with validate-only so a misbehaving Sentinel can block deploys but
// cannot silently modify them.
package admission

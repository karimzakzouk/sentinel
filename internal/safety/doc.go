// Package safety implements Sentinel's governance policy as five embedded Go
// rules — there is no OPA / Rego sidecar. Every fix proposed by the agent
// pipeline flows through Policy.Evaluate before any write action is taken.
//
// The verdict matrix:
//
//	rollback + schema migration present      → Deny (hard block)
//	non-production namespace                 → Allow (sandboxes are permissive)
//	production + unsafe fix type             → Deny
//	production + high-risk service           → RequireHumanApproval
//	production + replica change > MaxPct     → RequireHumanApproval
//	production + confidence ≥ autonomous     → Allow
//	production + confidence ≥ human review   → RequireHumanApproval
//	production + confidence < human review   → Deny
//
// HighRiskServices is empty by default — operators must opt services in via
// NewPolicyWithHighRisk or WithHighRiskServices. The Demo-era defaults
// (payment-api / billing-api / auth-service) are intentionally NOT carried
// forward.
package safety

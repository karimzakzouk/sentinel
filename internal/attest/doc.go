// Package attest implements the Ed25519-signed audit trail for every
// autonomous action Sentinel takes.
//
// Every autonomous action — investigation started, fix proposed, PR opened,
// PR merged, auto-reverted — emits a signed attestation containing:
//
//   - What was observed (incident signals, evidence).
//   - What the LLM proposed (model, prompt hash, input/output tokens, cost).
//   - What was verified (CI status, sandbox result, semantic diff).
//   - What was merged (PR number, commit SHA, merge commit).
//   - What happened after (post-merge regression check).
//
// Each attestation is signed with the Sentinel instance's Ed25519 private
// key. The signature is verifiable by anyone holding the public key —
// external auditors, customers' security teams, insurance underwriters.
// Attestations form a SHA-256 hash chain: each record's previous_hash
// field is the SHA-256 of the previous signed record, so any tampering
// with historical records breaks the chain.
//
// Attestations are written as one JSON object per line to a daily JSONL
// file under AttestLogDir (e.g. /data/attest/attest-2026-01-15.jsonl).
// Files are append-only and opened with 0600 permissions.
//
// Compliance: EU AI Act (enforcement August 2026), SOC 2, ISO 27001, and
// the IETF "Agent Audit Trail" draft (datatracker.ietf.org).
//
// Nil-safety: a nil *Signer is a no-op. Callers that have attestation
// disabled (config.AttestEnabled == false) simply hold a nil *Signer and
// every method returns nil without writing anything.
package attest

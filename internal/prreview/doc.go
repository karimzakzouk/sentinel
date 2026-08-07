// Package prreview implements Sentinel's predictive PR review — the
// "prevent before it happens" feature.
//
// When a developer opens a PR, GitHub sends a pull_request webhook to
// Sentinel. The Reviewer:
//
//  1. Verifies the webhook HMAC-SHA256 signature (refusing if no secret is
//     configured — unauthenticated webhooks must not be trusted because a
//     forged webhook could drive Sentinel into fetching attacker-controlled
//     URLs with the GitHub PAT attached).
//  2. Rebuilds the diff URL from the trusted apiURL + repo + PR number (the
//     diff_url field in the webhook body is an SSRF + token-exfil vector).
//  3. Fetches the PR diff via the GitHub API.
//  4. Chunks the diff into per-file hunks.
//  5. Embeds each chunk and queries the HNSW incident memory for similar
//     past incidents (especially ones with outcome=failure).
//  6. If similarity > threshold (default 0.75), posts a review comment
//     warning the developer.
//
// Review comments are posted as the GitHub App's verified bot identity when
// a TokenProvider is configured, falling back to the PAT otherwise.
//
// GitHub redelivers webhooks (3 retries over 24h). Each redelivery would
// post a duplicate review comment without dedup. The Reviewer tracks
// X-GitHub-Delivery IDs in-memory (1h TTL, LRU eviction at 1000 entries).
//
// HTTP entry point: Reviewer.HandleWebhook.
package prreview

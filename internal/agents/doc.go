// Package agents implements Sentinel's LLM agent pipeline: investigation,
// evidence gathering, analysis, fix proposal, security review, and post-fix
// artifact generation (regression test + reproducer).
//
// The pipeline is orchestrated by Pipeline (pipeline.go). Each agent is a
// struct with a Run method that mutates schemas.AgentState in place. Pure
// math agents (analyst) take no LLM; LLM-coupled agents (investigator,
// fix_proposer, evidence_gatherer, test_generator, reproducer) take a
// *llm.Router.
//
// Defense-in-depth against prompt injection (the LLM reads attacker-controlled
// pod logs):
//
//  1. Repo allowlist (config.AllowedGitHubRepos) — only annotated repos clone.
//  2. Path-confined ReadFile (internal/repo) — LLM-returned file paths
//     cannot escape the cloned repo.
//  3. Pattern-based security scanner (ScanFix in security.go) — 19 dangerous
//     patterns block the fix before the PR is opened.
//  4. Reviewer-agent LLM (ReviewerAgent in security.go) — a second LLM call
//     with a different system prompt audits the proposed fix.
//  5. Sandbox NetworkPolicy default-deny (internal/executors) — even a
//     compromised fix cannot exfiltrate secrets from the canary pod.
package agents

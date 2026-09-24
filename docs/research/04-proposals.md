# 04 — Proposals & open questions (draft, for later discussion)

Status: **not agreed, not scheduled**. These came out of post-run-#11
conversations. Nothing here blocks v1.7.4 bugfixing.

## P1 — Trace-first fast path in the investigator

Promote existing parsers from fallback to first attempt:

```
parse stack trace (extractCrashLocation / ExtractSearchQueriesUniversal)
  ├─ success → skip Round-1 LLM call; use parsed location + local search
  │            directly for Round 2 (or skip straight to fix if unambiguous)
  └─ empty   → current two-round LLM flow
```

- Estimated eval impact: shipping (Java) + search (Rust) get diagnosed free
  and immune to token limits; analytics (Ruby) likely.
- Cost: contained change in `internal/agents/investigator.go` + tests.
- Risk: crash-site ≠ bug-site (user-api lesson). Mitigation: fast path only
  *locates* and gathers files; the fix-writing LLM call still happens.

## P2 — LLM rate-limit resilience (this is really a v1.7.4 bugfix)

Run #11: shipping-api investigation failed permanently on Groq TPM limit
(13,271 requested vs 8,000). Fix candidates:

1. Retry with backoff on 429/quota errors (currently: hard fail).
2. Progressive context truncation on retry: halve search results, then pod
   logs (keep tail), then file tree — never drop the crash location.
3. TPM-aware budgeting: estimate tokens before send, shed context
   preemptively.

## P3 — Drain3-style template mining as detection sensor #2

New `internal/detection` sensor: per-service template vocabulary + novelty /
rate-spike flags feeding the correlator as a lower-confidence signal.
Needs: learning window config, false-positive strategy (maybe only escalates
when corroborated by another signal).

## P4 — Synthetic probing (already on the v1.8 radar)

Payments-api (no logs) and inventory-api (silent corruption) are undetectable
by any log/status approach. Active health checks with semantic assertions
("charge returns 200", "reserved ≤ stock after concurrent reserves") are the
only way to see them. Design sketch lives in the v1.8 roadmap discussion.

## P5 — Ground-truth/scoring hygiene (eval harness, not product)

- analytics-worker is unwinnable as scored until the NoMethodError fix merges
  (the planted deadlock cannot fire behind it). Reclassify or sequence the
  bugs.
- Consider scoring "correct fix of the *observed* error" separately from
  "matches planted bug".

## Open questions to talk through later

1. Should the fast path (P1) skip the LLM entirely when the trace is
   unambiguous, or always keep a reasoning pass?
2. Would symbolication discipline (source maps in images) be worth requiring
   of adopters, or is that too much to ask? Middle ground: parse `dist/`
   paths back to `src/` heuristically via the file tree.
3. Is Drain3 worth the operational complexity, or do patterns + synthetic
   probing (P4) cover the practical gap?
4. Harmful-fix guard: should the fix proposer be shown the deployment env var
   *names* (not values) to prevent the user-api `DATABASE_URL` class of
   wrong-but-confident fixes?

## Run #11 evidence recap (why these proposals exist)

| Service | What happened | Which proposal addresses it |
|---|---|---|
| shipping-api | Investigation killed by Groq TPM limit | P2 |
| user-api | Harmful fix (env var rename), trace pointed at compiled victim | P1, P5(open q4) |
| analytics-worker | Correct fix of shadowing bug, scored NO | P5 |
| checkout-api | Slow-sink never implemented; 409-noise misdiagnosis | harness |
| recommendation-engine | Injector image pull stuck (no pre-pull) | harness |
| payments/inventory | No signal exists to detect | P4 |
| search-api | Full pipeline success | — (proof it works) |

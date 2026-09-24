# 03 — What Sentinel sends to the LLM today, and why

Question asked: *"What exactly is being sent to the LLM? Is all of it needed?"*

## Current investigator flow (v1.7.3, `internal/agents/investigator.go`)

The LLM never sees the cluster, Kubernetes, or the repo contents wholesale.
It is a text-in / JSON-out consultant. Two rounds:

### Round 1 — "what should I search for?"

System prompt: senior-SRE persona + rules for emitting regex search queries.

User message contains exactly:

| Field | Content | Cap |
|---|---|---|
| Service + namespace | identity of the workload | ~10 tokens |
| Anomaly cluster summary | one-paragraph "what's wrong" | small |
| Detected language | inferred from the error (e.g. NullPointerException → Java) | ~20 tokens |
| Crash location | `extractCrashLocation(podLogs)` if parseable | tiny |
| Pod logs | raw crash logs | **4 KB** (`MaxPodLogsBytes`) |
| File tree | *paths only*, no contents | 300 entries (r1) / 200 (r2) |

Response: `{"search_queries": [...]}` — 3-8 regex patterns.

Key design point: **the searches run locally** as regex over the cloned repo
(`repo.Search`). The LLM picks keywords; Go code executes them. The repo
never goes to the LLM.

### Round 2 — "what's your diagnosis?"

Same context package + **search results** (file:line — matching code lines,
deduped, capped at `MaxStackFilesToRead`). Response: up to 5 hypotheses
(statement / evidence / confidence / source_files) + 1-5 `files_to_read`.

Those files then go **in full** to the Fix Proposer (separate system prompt:
"you write production code fixes"), then the Security Reviewer sees the diff.

### Downstream of the investigator (context flow)

```
logs + tree ──▶ R1 LLM ──▶ local regex search ──▶ R2 LLM ──▶ hypotheses
                                                              │
full files (LLM-picked) ──▶ Fix Proposer LLM ──▶ patch diff ──▶ Security Reviewer LLM
                                                              │
                                              sandbox (no LLM) ──▶ PR (no LLM)
```

## Why each piece is there (failure mode it prevents)

- **Pod logs** — the only actual *evidence*. Without them the model invents.
- **File tree** — anti-hallucination anchor; stops confident references to
  files that don't exist.
- **Language hint** — crutch for small models (20B class) that misidentify
  languages from errors alone.
- **Namespace/service** — mostly identity; weakest item, ~10 tokens.

## Why not more context

- **Cost/limits**: run #11's shipping-api investigation died at 13,271 tokens
  vs Groq's 8,000 TPM on gpt-oss-120b. One over-limit request = incident
  failed, no retry (the v1.7.4 fix candidate).
- **Attention dilution**: well-documented "lost in the middle" effect — more
  context makes models *worse* at using the relevant part.

## Why not less context

- Run #11 user-api: logs showed DB timeouts; the model saw `DATABASE_URL`
  somewhere and produced a confident, *harmful* fix (switched the env var name
  the deployment actually uses). Not "not enough context" but "not enough of
  the RIGHT evidence" — same lesson.

## The core trick

The LLM never gets the repo — **it gets a map and a phone.** Round 1 is "tell
me what to pull for you," so two small calls beat one giant one. This is the
standard agent pattern (retrieval via tool calls instead of stuffing context)
and it is what makes the design viable on small models and tight token
budgets.

Related reading: "context engineering" literature; the 2023-era failures of
"just send the whole repo" are why this pattern exists.

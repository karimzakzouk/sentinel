# Research Notes — Detection & Fault Localization

> Origin: post-run-#11 (v1.7.3) discussions, 2026-09-24. Question-driven research
> sparked by reviewing why 8/9 eval services were missed across runs and whether
> the LLM steps can be reduced or eliminated.

These notes capture what we found so we can come back to it when designing
v1.8+. Nothing here is merged into the product; this branch is a reference.

## Index

| File | Question it answers |
|---|---|
| [01-detection-without-llm.md](01-detection-without-llm.md) | Is there a universal "find the error in any language" approach? (Drain3, Sentry SDK, LLM log triage) |
| [02-fault-localization.md](02-fault-localization.md) | Can we pinpoint the exact file/line without an LLM? (symbolication, SBFL, core dumps, crash clustering) |
| [03-llm-context.md](03-llm-context.md) | What exactly does Sentinel send to the LLM today, and why? Is all of it needed? |
| [04-proposals.md](04-proposals.md) | Concrete improvement proposals + open questions for later discussion |
| [05-attack-list.md](05-attack-list.md) | Full adversarial review of the shipped workflow + user-api harmful-fix autopsy + fix bucketing |
| [06-generalization.md](06-generalization.md) | Research consensus (scaffolding>model, minimal loops, verification scaling) + generalization recipe + decisions log |

## Context for future readers

- Run #11 (chart 1.7.3): 6/9 incidents detected, 5 PRs, 1/9 correct fix.
- Detection layer (patterns + container status) performed as designed — every
  bug that actually *fired* was detected. Remaining misses live in the harness
  (bugs not firing) and the investigation/fix layers (LLM reasoning quality,
  token limits).
- The recurring theme of these notes: **cheap deterministic layers should do
  everything they can before the LLM is invoked.**

# 01 — Is there a universal error detector, no LLM?

Question asked: *"Is there no general one call that finds the error in whatever
language or any tool in general?"*

Short answer: no single call exists, but there are three families that get
close, each from a different angle. Sentinel's current design (hardcoded
patterns) is a legitimate member of the first family.

## 1. Make the app report its own errors — SDK error tracking (Sentry et al.)

One SDK integration per app (`captureError(e)` or auto-hooked crash handlers)
normalizes errors from 40+ languages into a single schema with stack traces,
grouping, and alerting.

- Pros: no log parsing at all; the error arrives pre-structured.
- Cons: requires **modifying every application** (9 code changes in the marketly
  eval), and cannot see failures the app doesn't report (payments-api silently
  502ing would still be invisible).
- Trade-off framing: "read the logs, never touch the apps" (Sentinel) vs
  "instrument the apps, never parse logs" (Sentry). The industry argues about
  this constantly; both are valid.

## 2. Learn what "normal" looks like — unsupervised log template mining (Drain3)

[Drain3](https://github.com/logpai/Drain3) (LogPAI, IBM-adjacent research
lineage; the underlying Drain algorithm has 1500+ citations) is the canonical
open-source implementation:

1. Streams logs and clusters lines into **templates** automatically
   (`user bob logged in` and `user alice logged in` → one template
   `user * logged in`). Language-agnostic by construction.
2. After a learning window, the set of templates per service = its "normal"
   vocabulary.
3. Anything **new** (never-seen template) or **suddenly more frequent**
   (template rate spike) is flagged.

- Pros: catches *unknown* error formats. Run #7's notification-worker failure
  (`gaierror` spam) would have been flagged without any pattern existing,
  purely as a new template appearing ~400 times.
- Cons: needs a learning period (new deploys look anomalous at first → false
  positives), and it flags *weird*, not necessarily *broken* — needs a
  severity/confidence layer on top.
- Related academic work: LogAnomalyDetector ("Project Scorpio", AICoE), Zebrium
  (commercial, ML-based root-cause from logs), LogLLM (2024), TPLogAD (2024).

## 3. LLM-based log triage (LogLLM, LogAn, LLM4Log survey)

Small language models reading logs and classifying severity/root-cause like a
human. Most general in theory; too slow/expensive as the front door at
millions of lines/hour. Current consensus: use LLMs as a **second opinion** on
lines a cheap layer already flagged — which is roughly what Sentinel's
investigator does after detection.

## What this means for Sentinel

The detection gap that hurt us in eval runs was always "error printed in a
format we didn't pattern-match". Drain3-style template mining is the natural
**second sensor** alongside `internal/errorparse`:

- patterns = "known bad" (deterministic, zero learning)
- template mining = "unknown but new" (catches novel formats)
- container status = "crash regardless of logs" (already shipped)

Sources: logpai/Drain3 GitHub; Drain paper (He et al., "An Online Log Parsing
Approach with Fixed Depth Tree"); LogLLM (Nov 2024); LogAn (2026);
LLM4Log survey repo.

# 02 — Pinpointing the faulty file/line without an LLM

Question asked: *"Can I get the file or the exact place that caused the issue
without the LLM?"*

Short answer: yes — this is a whole research field called **fault
localization**, running since the 1990s. Four families, each with a catch.

## 1. Stack-trace parsing + symbolication (industry standard)

Every mature runtime prints the crash location in a regular shape:

| Language | Shape |
|---|---|
| Python | `File "app/handlers.py", line 99, in get_user` |
| Java | `at com.marketly.CarrierClient.quote(CarrierClient.java:42)` |
| Node | `at async getUserByEmail (/app/dist/db.js:35:20)` |
| Ruby | `app/lock/distributed_lock.rb:28:in 'acquire'` |
| Go | `main.go:42 +0x1f` inside the goroutine dump |
| Rust | `thread 'main' panicked at src/handlers.rs:99:20` |

A regex parser extracts file:function:line at zero cost. **Sentinel already
has the pieces** (`extractCrashLocation`, `ExtractSearchQueriesUniversal` in
`internal/agents`) but only uses them as *fallbacks* — the LLM is called
first.

- Sentry/Datadog do this at scale in production: "error tracking" = parse
  trace → group by fingerprint → point at the line. No LLM involved.
- **Symbolication** maps compiled frames back to source: source maps for
  JS/TS (uploaded at deploy), DWARF debug symbols for Go/Rust/native.
- Catch: requires debug artifacts shipped at deploy time (most prod images
  strip them for size). Without source maps, `dist/db.js:35` cannot become
  `src/db.ts:41`. Run #11's user-api PR was wrong partly for this reason —
  the trace pointed at compiled output.

## 2. Spectrum-Based Fault Localization (SBFL) — statistical

The classic technique (Tarantula, Ochiai formulas): instrument code coverage so
you know which lines ran in **passing** vs **failing** executions, then rank
lines by suspiciousness (executed in 90% of failed runs, 5% of passed runs →
prime suspect). Pure arithmetic.

- Catch: needs coverage instrumentation in every service — unrealistic for
  third-party/un-instrumented workloads.
- **Directly relevant 2025 paper**: Rafi et al., *"Spectrum-based fault
  localization without fault-triggering tests"* — uses **stack traces from
  crash reports as proxies for failing tests**. This is essentially the
  "parse the trace first, use it to rank suspicious code, no LLM" design we
  sketched during the discussion. Validates the direction.

## 3. Core dumps + record/replay debuggers — forensic

- **Core dumps**: OS dumps the crashing process's memory; `gdb core` gives
  exact line, every variable value, full call stack. Gold standard for native
  crashes (the recommendation-engine segfault scenario). Needs core dumps
  enabled + disk, only applies to native crashes.
- **rr (record & replay)**: records execution; after a crash you can step
  *backwards* — "who wrote this bad value?" Deepest debugging tool that
  exists. Dev-machine tool mostly; impractical to run fleet-wide, but the
  concept (execution evidence beats inference) is the inspiration for
  synthetic probing.

## 4. Crash clustering / dedup (ReBucket line)

Not localization but triage: ReBucket (Microsoft Research, 215+ citations)
groups duplicate crash reports by call-stack similarity so engineers fix a
bucket, not a ticket. Newer: deep-metric-learning bucketing (2022),
transformer-based dedup "dedupT" (2025). Sentinel's
`(namespace, service, errorType)` fingerprint + cooldown is a simpler cousin;
stack-frame-weighted similarity would be the upgrade if we ever see fingerprint
collisions.

## The honest limitation (why the LLM survives)

All four families locate the **symptom line**, not the fix. They also assume a
trace exists:

- checkout-api: structured `[error] ... 409 Conflict` lines — no file/line at all.
- payments-api: no logging whatsoever.
- user-api: trace pointed at the *victim* (pg pool), not the *cause* (heap).

So the realistic architecture is a fast path / slow path:

```
trace parseable + artifacts?
  YES → deterministic locate (free, instant) → LLM only writes the fix
  NO  → full LLM investigation (current two-round flow)
```

Eval-derived estimate for the marketly services: fast path would nail
shipping (Java stack), search (Rust panic location), probably analytics
(Ruby `.rb:line`), partially user-api (compiled-output problem), and do
nothing for checkout/payments/inventory.

# 05 — Attack list on the shipped workflow (post-run-11 review)

A full adversarial review of the v1.7.3 pipeline ("will it fail in real life?").
Grades: 🔴 proven in our own evals, 🟠 verifiable in code and waiting for real
life, 🟡 fundamental limit of the approach.

## Proven failures (we watched them happen)

1. 🔴 **"Resolved" is pipeline-completion, not reality.** Incidents are marked
   resolved when the pipeline finishes and the sandbox passes
   (`MarkResolved` at pipeline end, before the fix is even deployed). The
   beta/Thompson feedback loop is rewarded on sandbox success — wrong-but-
   sandbox-passing fixes (checkout, notification) teach the system those fix
   patterns "work". Poisoned feedback compounds.
   **DECISION (2026-09-24): keeping sandbox-resolved semantics FOR NOW** —
   deliberate scope choice; post-deploy verification is deferred, not
   rejected. Revisit after run #12 evidence.
2. 🔴 **Confident-wrong fixes; fast path amplifies.** The ≥0.9 confidence
   fast path skips evidence gathering precisely when the model is most sure.
   LLM confidence is self-reported (user-api fix confidence 61%, checkout
   59% — both wrong).
3. 🔴 **One rate-limit error kills an incident.** Run #11 shipping-api:
   13,271-token request vs Groq 8,000 TPM, no retry, incident permanently
   failed.
4. 🔴 **False positives are default weather.** checkout-api's "incident" was
   409s from inventory working as designed. No severity model, no baselines.

## Security holes

5. 🟠 **Indirect prompt injection via pod logs.** Logs are attacker-
   controllable text fed to an agent holding a GitHub write PAT and cluster
   read. The security reviewer is also an LLM reading the same context; most
   configs stop at L3.
6. 🟠 **Secrets/PII go to the LLM provider unredacted.** No scrubbing code
   exists. Fails compliance review (SOC2/GDPR/HIPAA) quickly.

## Scale & operations

7. 🟠 **List-poll + one follow-stream per pod**, not a shared watch API.
   Linear API-server load; fine at 50 pods, painful at thousands.
8. 🟠 **Singleton deployment, no leader election, SQLite.** Detection stops
   on restart; in-flight investigations lost (only tracked PRs reconcile).
9. 🟠 **Cross-service bugs are invisible.** One service's logs + one repo per
   investigation. Real outages are "orders-service published a malformed
   event; checkout crashed" — Sentinel faithfully fixes the victim.
10. 🟠 **Clone-vs-deployed drift.** `:latest` tags / stale
    `sentinel.io/git-commit` → fixes written against code that isn't running.

## Trust & social reality

11. 🟡 **Sandbox ≠ production** (L3 is compile+boot; no real dependencies).
12. 🟡 **PR spam kills adoption** before correctness does (11 incidents /
    9 services / 45 min extrapolates badly).
13. 🟡 **Annotation-or-nothing**: missing `sentinel.io/repo` silently
    downgrades a service to logs-only diagnosis forever, with no report.

## Fix priority (lethality per effort)

1. Close the feedback loop (DEFERRED by decision — see #1)
2. LLM retry + redaction (mechanical) → **v1.7.4 scope**
3. Severity/baseline gate on detection
4. Multi-service correlation (big; before v1.8 commitments)

## Post-discussion bucketing of proposed Phase-1 fixes

Filter: "would this fix matter if the eval didn't exist?"

- **Bucket A — general**: LLM retry + progressive truncation. Rate limits
  are universal (OpenAI TPM, Azure quotas, vLLM capacity). Production bug
  in the failure path; the eval found it cheaply.
- **Bucket B — eval-shaped as first proposed, upgraded to general**:
  env-var grounding. Grep-the-repo version assumed in-repo manifests (true
  in the eval, false under GitOps/Terraform). **Upgraded to "ask the
  cluster"**: inject the live pod's env var NAMES (never values), resource
  limits, and configmap/secret refs into the Fix Proposer context as a
  standing "Runtime Context" block. Works for every repo layout because it
  reads ground truth from the pod spec. → **v1.7.4 scope**
- **Bucket C — calibration only (harness, not product)**: image pre-pull,
  checkout slow-sink, heap cap, analytics scoring. Fixes the measurement
  instrument. Explicitly NOT counted as product work.

## User-api harmful-fix autopsy (case study)

Timeline: 64MB heap cap + 160 rps login hammer → event-loop starvation →
pg connection timeouts (2s `connectionTimeoutMillis`) → uncaught
`Connection terminated` crash. The FATAL heap line never fired.

The LLM's hypothesis (verbatim from incidents.json): "The PostgreSQL
connection pool is created without a valid connection string (e.g. missing
or malformed **DATABASE_URL**)". DATABASE_URL appears nowhere in the logs
or the code — the model supplied the canonical 12-factor name from its
training prior, saw the nonstandard `USER_DATABASE_URL` in db.ts, and
concluded the code reads the wrong var. The fix renames the var the
deployment actually sets (deployment.yaml:34 `USER_DATABASE_URL`) →
would fall back to localhost. Harmful.

Evidence that argued against the theory (and was ignored): a missing env
var yields instant "connection refused" via the `||` localhost fallback;
the app instead died of 2-second timeouts — proof the var was set and the
DB reachable, pod just too starved to finish the handshake.

Why nothing caught it: (a) the one decisive fact — the pod's actual env —
never reached the LLM; (b) `k8s/deployment.yaml` was in the file tree but
never requested ("files most likely to contain the bug" → manifests don't
pattern-match); (c) sandbox L3 passes trivially (env rename doesn't break
boot; /health doesn't query the DB); (d) confidence 61% > loop-back
threshold 30%.

Root cause of the root cause: **the model doesn't distinguish observed
facts from its own inferences.** "Code reads X" (observed) and "env
provides Y" (inferred) felt identical. The general fix is not more
reasoning rounds — it's deterministic grounding: when a hypothesis
references the environment, fetch the environment. That is v1.7.4's
runtime-context injection.

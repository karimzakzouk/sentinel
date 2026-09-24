# 06 — Research consensus & the generalization recipe

Question asked: *"What do research and the public do, and what should we do
so it's generalized and can work anywhere?"* (searched 2026-09-24)

## What the research world established (2024–2026)

1. **Scaffolding beats model choice.** SWE-bench-era analyses: scaffolding
   changes alone move 22+ points with the same model; model changes ~1.
   The wrapper (tool orchestration, error recovery, context management) is
   the product.
2. **Minimal loops beat elaborate ones.** Mini-SWE-agent: ~100 lines,
   ~74% SWE-bench Verified. Heavy frameworks without loop detection /
   context filtering / basic verification accumulate failed steps until
   budget exhaustion. Lesson: loop with exit conditions and evidence
   discipline, or don't loop.
3. **Verification is the scaling law.** Test-time-compute literature
   (DeepSeek-R1, o1, "Trust but Verify" survey): more thinking helps only
   with a verifier in the loop; verification-signal compute dominates
   verification-free compute asymptotically. The sandbox is Sentinel's
   most valuable component.
4. **Structured self-verification, not hoped-for.** Hallucination-in-agents
   surveys: self-questioning, grounding, abstention work when structured
   and model-agnostic (explicit assumption checklists, retrieval before
   assertion).
5. **The AI-SRE industry is deliberately more timid.** 2026 products
   triage, correlate, and remediate known runbooks; almost none
   autonomously merge code fixes. Sentinel is ahead on autonomy — which
   also means the market signal is: detection→diagnosis→suggestion is a
   product today; autonomous merge is a research project with a budget.

## The generalization recipe (mapped to Sentinel)

- **A. Make verification the spine.** Sandbox depth scales with risk:
  variable rename → L3 fine; connection string / config semantics →
  mandatory L4/L5 with reproducer fail-before/pass-after against real env
  var names.
- **B. Structure the assumption checklist.** Deterministic grounding
  before any env/config-touching fix: pod env names, limits, refs into the
  Fix Proposer context. (Shipped as v1.7.4 runtime-context injection.)
- **C. Close the post-deploy loop.** Resolved should eventually mean
  "errors stopped in production", detected by the same watcher that
  raised the incident. *(DEFERRED by explicit decision 2026-09-24:
  sandbox-resolved semantics kept for now.)*
- **D. Graduated autonomy.** Suggest-mode default; auto-merge unlocks
  per-service after N verified-correct fixes. Earned trust per workload.
- **E. Stay model-agnostic.** The router swaps models; keep it that way.

## Decisions log

| Date | Decision | Reason |
|---|---|---|
| 2026-09-24 | v1.7.4 = LLM retry/truncation + runtime-context injection + harness calibration | Proven blockers only; no new capabilities |
| 2026-09-24 | Keep sandbox-resolved semantics FOR NOW | User decision; post-deploy verification deferred until run #12 evidence |
| 2026-09-24 | Analytics NoMethodError fix counts as correct in scoring | Genuine fix of a genuine bug; planted deadlock unreachable behind it |
| 2026-09-24 | Agent loop stays opt-in | Economics + flailing without verification spine |

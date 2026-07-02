// Package agents implements the 4-agent reasoning pipeline.
// Pipeline:
//   Investigator → EvidenceGatherer → Analyst → FixProposer
// Any agent can raise a RewindEvent which wipes hypotheses and returns
// control to the Investigator (max 5 iterations).
package agents

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "encoding/json"
        "fmt"
        "log"
        "time"

        "github.com/karimzakzouk/sentinel/internal/k8s"
        "github.com/karimzakzouk/sentinel/internal/llm"
        "github.com/karimzakzouk/sentinel/internal/schemas"
        "github.com/karimzakzouk/sentinel/internal/tracing"
)

// Pipeline runs all 4 agents in sequence with rewind support.
// Returns the final AgentState.

type Pipeline struct {
        investigator     *Investigator
        evidenceGatherer *EvidenceGatherer
        analyst          *Analyst
        fixProposer      *FixProposer
        testGenerator    *TestGenerator
        reproducerGen    *ReproducerGenerator
        betaStore        BetaStore

        investigatorTimeoutSec       int
        evidenceGathererTimeoutSec   int
        analystTimeoutSec            int
        fixProposerTimeoutSec        int
        confidenceLoopBackThreshold  float64

        generateTests bool

        // Used as a fallback when the test generator fails or the repo has no test framework.

        generateReproducer bool

        maxIterations int
}

// NewPipeline creates a Pipeline with optional vector store for memory loop.

func NewPipeline(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, betaStore BetaStore, vectorStore VectorStore) *Pipeline {
        return &Pipeline{
                investigator:                NewInvestigator(router, k8sClient, gitHubToken, vectorStore),
                evidenceGatherer:            NewEvidenceGatherer(router, k8sClient),
                analyst:                     NewAnalyst(router),
                fixProposer:                 NewFixProposer(router, k8sClient, gitHubToken),
                testGenerator:               NewTestGenerator(router),
                reproducerGen:               NewReproducerGenerator(router),
                betaStore:                   betaStore,
                investigatorTimeoutSec:      120,
                evidenceGathererTimeoutSec:  90,
                analystTimeoutSec:           15,
                fixProposerTimeoutSec:       300,
                confidenceLoopBackThreshold: 0.3,
                generateTests:               true,
                generateReproducer:          true,
        }
}

// SetConfig sets configurable pipeline parameters.


func (p *Pipeline) SetConfig(invTimeout, evTimeout, anTimeout, fpTimeout int, confLoopBack float64) {
        if invTimeout > 0 {
                p.investigatorTimeoutSec = invTimeout
        }
        if evTimeout > 0 {
                p.evidenceGathererTimeoutSec = evTimeout
        }
        if anTimeout > 0 {
                p.analystTimeoutSec = anTimeout
        }
        if fpTimeout > 0 {
                p.fixProposerTimeoutSec = fpTimeout
        }
        if confLoopBack > 0 {
                p.confidenceLoopBackThreshold = confLoopBack
        }
}

// SetPrometheusURL wires the Prometheus backend into the evidence gatherer.
// a real Prometheus instance. No-op if URL is empty (backward compatible).

func (p *Pipeline) SetPrometheusURL(url string) {
        p.evidenceGatherer.SetPrometheusURL(url)
}

// SetAllowedRepos propagates the GitHub repo allowlist to the Investigator.
// referenced by the sentinel.io/repo pod annotation. See
// Config.IsRepoAllowed for the matching config-level helper.

func (p *Pipeline) SetAllowedRepos(repos []string) {
        p.investigator.SetAllowedRepos(repos)
}

// SetGitHost propagates the git host to the Investigator + FixProposer.
//. Enables GitHub Enterprise / GitLab / Bitbucket.

func (p *Pipeline) SetGitHost(host string) {
        p.investigator.SetGitHost(host)
        p.fixProposer.SetGitHost(host)
}

// SetDefaultBranch propagates the default branch fallback to the
// Investigator + FixProposer. v3.11.0: Replaces hardcoded "main" in
// cloner.go + branch_resolver.go + fix_proposer.go.

func (p *Pipeline) SetDefaultBranch(branch string) {
        p.investigator.SetDefaultBranch(branch)
        p.investigator.cloner.WithDefaultBranch(branch)
        p.fixProposer.SetDefaultBranch(branch)
}

// SetLLMPriorWeight propagates the LLM prior weight to the Analyst.


func (p *Pipeline) SetLLMPriorWeight(w float64) {
        p.analyst.SetLLMPriorWeight(w)
}

// SetMaxIterations sets the max pipeline iterations. v3.11.0: Replaces
// the hardcoded 5 in schemas/types.go NewAgentState. The
// pipeline's Run loop uses state.MaxIterations, so we set it on the state
// before each Run via this setter (stored on the pipeline).
// NOTE: This is a soft setter — the state's MaxIterations is set in
// schemas.NewAgentState. To override, call SetMaxIterations BEFORE the
// pipeline runs and the pipeline will rewrite state.MaxIterations at
// the start of Run.

func (p *Pipeline) SetMaxIterations(n int) {
        if n > 0 {
                p.maxIterations = n
        }
}

// RetryFixProposal re-runs ONLY the fix proposer with the current state.
// sets state.SandboxFeedback with the failure details before calling this.
// The fix proposer sees the feedback in its user message and generates a
// new, corrected fix. Does NOT re-run the investigator/evidence/analyst —
// those are expensive and the root cause hasn't changed.

func (p *Pipeline) RetryFixProposal(ctx context.Context, state *schemas.AgentState) error {
        fpCtx, fpCancel := context.WithTimeout(ctx, time.Duration(p.fixProposerTimeoutSec)*time.Second)
        defer fpCancel()
        if err := p.fixProposer.Run(fpCtx, state); err != nil {
                return fmt.Errorf("fix_proposer_retry: %w", err)
        }
        return nil
}

// Run executes the full pipeline on the given state. Handles rewinds.
// already has high confidence (>= 0.7). Evidence gathering adds ~90s of LLM +
// K8s API latency; for obvious errors like KeyError with the key name in the
// traceback, it's unnecessary.

func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
        defer recoverPanic("pipeline")

        // value.NewAgentState.

        if p.maxIterations > 0 {
                state.MaxIterations = p.maxIterations
        }

        for state.Iterations < state.MaxIterations {
                state.Iterations++

                // 1. Investigator
                invCtx, invSpan := tracing.Start(ctx, "investigator.run")
                invCtx, invCancel := context.WithTimeout(invCtx, time.Duration(p.investigatorTimeoutSec)*time.Second)
                if err := p.investigator.Run(invCtx, state); err != nil {
                        invCancel()
                        invSpan.End()
                        return fmt.Errorf("investigator: %w", err)
                }
                invCancel()
                invSpan.SetAttribute("hypotheses", fmt.Sprintf("%d", len(state.Hypotheses)))
                invSpan.End()
                if state.Status == schemas.StatusFailed || len(state.Hypotheses) == 0 {
                        return nil
                }

                // high-confidence. This saves ~90s (LLM + K8s API calls) for obvious
                // errors like KeyError: 'DATABASE_URLL' where the traceback has all
                // the info we need.

                topConfidence := float64(0)
                for _, h := range state.Hypotheses {
                        if h.Confidence > topConfidence {
                                topConfidence = h.Confidence
                        }
                }
                if topConfidence >= 0.7 {
                        logging.Info("top hypothesis confidence %.2f >= 0.7 — skipping evidence gathering (fast path)", "topconfidence", topConfidence)
                } else {
                        // 2. Evidence Gatherer
                        evCtx, evSpan := tracing.Start(ctx, "evidence_gatherer.run")
                        evCtx, evCancel := context.WithTimeout(evCtx, time.Duration(p.evidenceGathererTimeoutSec)*time.Second)
                        if err := p.evidenceGatherer.Run(evCtx, state); err != nil {
                                evCancel()
                                evSpan.End()
                                return fmt.Errorf("evidence_gatherer: %w", err)
                        }
                        evCancel()
                        evSpan.End()
                        if state.Status == schemas.StatusFailed {
                                return nil
                        }
                        if isRewindBy(state, "evidence_gatherer") {
                                continue
                        }
                }

                // 3. Analyst (pure math — fast)

                anCtx, anCancel := context.WithTimeout(ctx, time.Duration(p.analystTimeoutSec)*time.Second)
                if err := p.analyst.Run(anCtx, state, p.betaStore); err != nil {
                        anCancel()
                        return fmt.Errorf("analyst: %w", err)
                }
                anCancel()
                if state.Status == schemas.StatusFailed {
                        return nil
                }
                if state.RootCause == nil {
                        if isRewindBy(state, "analyst") {
                                continue
                        }
                        return nil
                }

                // Confidence routing

                if state.RootCause.Confidence < p.confidenceLoopBackThreshold && state.Iterations < 3 {
                        log.Printf("[pipeline] confidence %.2f < %.2f — looping back to investigator (iteration %d)",
                                state.RootCause.Confidence, p.confidenceLoopBackThreshold, state.Iterations)
                        state.Hypotheses = nil
                        state.Evidence = nil
                        state.RootCause = nil
                        continue
                }

                // 4. Fix Proposer

                fpCtx, fpCancel := context.WithTimeout(ctx, time.Duration(p.fixProposerTimeoutSec)*time.Second)
                if err := p.fixProposer.Run(fpCtx, state); err != nil {
                        fpCancel()
                        return fmt.Errorf("fix_proposer: %w", err)
                }
                fpCancel()

                // The test is generated by a SEPARATE LLM call from the fix proposer —
                // the LLM that wrote the fix does NOT write the test. This prevents
                // the LLM from gaming its own test.
                // The test runs in the sandbox as Level 4 (test gate).

                if p.generateTests && state.FixProposal != nil {
                        testCtx, testCancel := context.WithTimeout(ctx, 60*time.Second)
                        test, err := p.testGenerator.Generate(testCtx, state)
                        testCancel()
                        if err != nil {
                                logging.Error("test generation failed (non-blocking)", "err", err)
                        } else if test != nil {
                                // Store the test in the agent state for the sandbox to run.
                                // We store it as a special event so it persists + is visible in the trace.

                                testJSON, _ := json.Marshal(test)
                                state.AgentTrace = append(state.AgentTrace, map[string]any{
                                        "agent":         "test_generator",
                                        "action":        "test_generated",
                                        "test_file":     test.TestFile,
                                        "test_framework": test.TestFramework,
                                        "test_language": test.TestLanguage,
                                        "description":   test.Description,
                                })
                                // Store the test in a special state field for the sandbox.
                                // We reuse the RetrievedIncidents field (it's a []map[string]any)
                                // to avoid a schema change. The sandbox looks for a key "regression_test".

                                if state.RetrievedIncidents == nil {
                                        state.RetrievedIncidents = []map[string]any{}
                                }
                                state.RetrievedIncidents = append(state.RetrievedIncidents, map[string]any{
                                        "regression_test": string(testJSON),
                                })
                                logging.Info("regression test generated", "value", test.TestFile, test.TestFramework, test.TestLanguage)
                        }
                }

                if p.generateReproducer && state.FixProposal != nil {
                        reproCtx, reproCancel := context.WithTimeout(ctx, 60*time.Second)
                        reproducer, err := p.reproducerGen.Generate(reproCtx, state)
                        reproCancel()
                        if err != nil {
                                logging.Error("reproducer generation failed (non-blocking)", "err", err)
                        } else if reproducer != nil {
                                reproJSON, _ := json.Marshal(reproducer)
                                state.AgentTrace = append(state.AgentTrace, map[string]any{
                                        "agent":           "reproducer_generator",
                                        "action":          "reproducer_generated",
                                        "script_file":     reproducer.ScriptFile,
                                        "script_language": reproducer.ScriptLanguage,
                                        "description":     reproducer.Description,
                                })
                                if state.RetrievedIncidents == nil {
                                        state.RetrievedIncidents = []map[string]any{}
                                }
                                state.RetrievedIncidents = append(state.RetrievedIncidents, map[string]any{
                                        "reproducer": string(reproJSON),
                                })
                                logging.Info("reproducer script generated", "value", reproducer.ScriptFile, reproducer.ScriptLanguage)
                        }
                }

                // Runs AFTER the fix is generated but BEFORE the pipeline returns.
                // If the security check fails, the fix is marked as requiring human review
                // (status = governance_review) and the security findings are stored.

                if state.FixProposal != nil {
                        secCtx, secCancel := context.WithTimeout(ctx, 30*time.Second)
                        secResult := RunSecurityCheck(secCtx, p.investigator.router, state)
                        secCancel()

                        state.AgentTrace = append(state.AgentTrace, map[string]any{
                                "agent":       "security_scanner",
                                "action":      "security_scan",
                                "passed":      secResult.Passed,
                                "findings":    len(secResult.Findings),
                                "summary":     secResult.Summary,
                        })

                        if !secResult.Passed {
                                logging.Error("SECURITY CHECK FAILED", "summary", secResult.Summary)
                                // Store the security findings in the error field so the PR body
                                // (if opened) shows them, and the safety policy can block auto-merge.

                                secDetail := secResult.Summary
                                for _, f := range secResult.Findings {
                                        secDetail += fmt.Sprintf("\n  - [%s] %s: %s (%s)", f.Severity, f.Rule, f.Message, f.File)
                                }
                                // Mark as governance review — the safety policy will block auto-merge

                                state.Status = schemas.StatusGovernanceReview
                                s := fmt.Sprintf("security_check_failed: %s", secDetail)
                                state.Error = &s
                                // Still return nil (not an error) — the pipeline completed, but the
                                // fix needs human review. The handler in main.go will see the status
                                // and skip auto-merge.

                                return nil
                        }
                        logging.Info("security check PASSED", "summary", secResult.Summary)
                }

                return nil
        }
        return fmt.Errorf("max iterations (%d) reached", state.MaxIterations)
}

func isRewindBy(state *schemas.AgentState, agent string) bool {
        if len(state.RewindEvents) == 0 {
                return false
        }
        last := state.RewindEvents[len(state.RewindEvents)-1]
        return last.RaisedBy == agent
}

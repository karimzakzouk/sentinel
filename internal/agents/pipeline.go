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
        "sync"
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
        generateReproducer bool
        maxIterations int
        skipPostFix bool // when true, Run() skips test/reproducer/security — caller runs RunPostFixArtifacts separately
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
// Investigator + FixProposer.Replaces hardcoded "main" in
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

// SetMaxIterations sets the max pipeline iterations.Replaces
// the hardcoded 5 in schemas/types.go NewAgentState. The
// pipeline's Run loop uses state.MaxIterations, so we set it on the state
// before each Run via this setter (stored on the pipeline).
// schemas.NewAgentState. To override, call SetMaxIterations BEFORE the
// pipeline runs and the pipeline will rewrite state.MaxIterations at
// the start of Run.

func (p *Pipeline) SetMaxIterations(n int) {
        if n > 0 {
                p.maxIterations = n
        }
}

func (p *Pipeline) SetSkipPostFix(skip bool) {
        p.skipPostFix = skip
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

func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
        defer recoverPanic("pipeline")

        if p.maxIterations > 0 {
                state.MaxIterations = p.maxIterations
        }

        for state.Iterations < state.MaxIterations {
                state.Iterations++

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

                topConfidence := float64(0)
                for _, h := range state.Hypotheses {
                        if h.Confidence > topConfidence {
                                topConfidence = h.Confidence
                        }
                }
                if topConfidence >= ConfidenceFastPath {
                        logging.Info("top hypothesis confidence %.2f >= ConfidenceFastPath — skipping evidence gathering (fast path)", "topconfidence", topConfidence)
                } else {
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

                if state.RootCause.Confidence < p.confidenceLoopBackThreshold && state.Iterations < 3 {
                        log.Printf("[pipeline] confidence %.2f < %.2f — looping back to investigator (iteration %d)",
                                state.RootCause.Confidence, p.confidenceLoopBackThreshold, state.Iterations)
                        state.Hypotheses = nil
                        state.Evidence = nil
                        state.RootCause = nil
                        continue
                }

                fpCtx, fpCancel := context.WithTimeout(ctx, time.Duration(p.fixProposerTimeoutSec)*time.Second)
                if err := p.fixProposer.Run(fpCtx, state); err != nil {
                        fpCancel()
                        return fmt.Errorf("fix_proposer: %w", err)
                }
                fpCancel()

                if p.skipPostFix {
                        return nil
                }
                if err := p.RunPostFixArtifacts(ctx, state); err != nil {
                        return err
                }

                return nil
        }
        return fmt.Errorf("max iterations (%d) reached", state.MaxIterations)
}

func (p *Pipeline) RunPostFixArtifacts(ctx context.Context, state *schemas.AgentState) error {
        if state.FixProposal == nil {
                return nil
        }

        var (
                testResult  *schemas.GeneratedTest
                testErr     error
                reproResult *schemas.GeneratedReproducer
                reproErr    error
                secResult   *SecurityResult
        )
        var wg sync.WaitGroup
        if p.generateTests {
                wg.Add(1)
                go func() {
                        defer wg.Done()
                        testCtx, testCancel := context.WithTimeout(ctx, 60*time.Second)
                        defer testCancel()
                        testResult, testErr = p.testGenerator.Generate(testCtx, state)
                }()
        }
        if p.generateReproducer {
                wg.Add(1)
                go func() {
                        defer wg.Done()
                        reproCtx, reproCancel := context.WithTimeout(ctx, 60*time.Second)
                        defer reproCancel()
                        reproResult, reproErr = p.reproducerGen.Generate(reproCtx, state)
                }()
        }
        wg.Add(1)
        go func() {
                defer wg.Done()
                secCtx, secCancel := context.WithTimeout(ctx, 30*time.Second)
                defer secCancel()
                secResult = RunSecurityCheck(secCtx, p.investigator.router, state)
        }()
        wg.Wait()

        if testResult != nil && testErr == nil {
                testJSON, _ := json.Marshal(testResult)
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":          "test_generator",
                        "action":         "test_generated",
                        "test_file":      testResult.TestFile,
                        "test_framework": testResult.TestFramework,
                        "test_language":  testResult.TestLanguage,
                        "description":    testResult.Description,
                })
                if state.RetrievedIncidents == nil {
                        state.RetrievedIncidents = []map[string]any{}
                }
                state.RetrievedIncidents = append(state.RetrievedIncidents, map[string]any{
                        "regression_test": string(testJSON),
                })
                logging.Info("regression test generated", "value", testResult.TestFile, testResult.TestFramework, testResult.TestLanguage)
        } else if testErr != nil {
                logging.Error("test generation failed (non-blocking)", "err", testErr)
        }

        if reproResult != nil && reproErr == nil {
                reproJSON, _ := json.Marshal(reproResult)
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":           "reproducer_generator",
                        "action":          "reproducer_generated",
                        "script_file":     reproResult.ScriptFile,
                        "script_language": reproResult.ScriptLanguage,
                        "description":     reproResult.Description,
                })
                if state.RetrievedIncidents == nil {
                        state.RetrievedIncidents = []map[string]any{}
                }
                state.RetrievedIncidents = append(state.RetrievedIncidents, map[string]any{
                        "reproducer": string(reproJSON),
                })
                logging.Info("reproducer script generated", "value", reproResult.ScriptFile, reproResult.ScriptLanguage)
        } else if reproErr != nil {
                logging.Error("reproducer generation failed (non-blocking)", "err", reproErr)
        }

        if secResult != nil {
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":    "security_scanner",
                        "action":   "security_scan",
                        "passed":   secResult.Passed,
                        "findings": len(secResult.Findings),
                        "summary":  secResult.Summary,
                })
                if !secResult.Passed {
                        logging.Error("SECURITY CHECK FAILED", "summary", secResult.Summary)
                        secDetail := secResult.Summary
                        for _, f := range secResult.Findings {
                                secDetail += fmt.Sprintf("\n  - [%s] %s: %s (%s)", f.Severity, f.Rule, f.Message, f.File)
                        }
                        state.Status = schemas.StatusGovernanceReview
                        s := fmt.Sprintf("security_check_failed: %s", secDetail)
                        state.Error = &s
                        return nil
                }
                logging.Info("security check PASSED", "summary", secResult.Summary)
        }

        return nil
}

func isRewindBy(state *schemas.AgentState, agent string) bool {
        if len(state.RewindEvents) == 0 {
                return false
        }
        last := state.RewindEvents[len(state.RewindEvents)-1]
        return last.RaisedBy == agent
}

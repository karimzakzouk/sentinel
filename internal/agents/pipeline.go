package agents

// pipeline.go: orchestrates the agent loop.
//
// Pipeline:
//
//      Investigator → EvidenceGatherer → Analyst → FixProposer → Security
//                                                  (post-fix artifacts in parallel:
//                                             TestGenerator + ReproducerGenerator)
//
// Any agent can raise a RewindEvent which wipes hypotheses and returns control
// to the Investigator (max state.MaxIterations iterations). The pipeline owns
// state.Iterations — it is the ONLY place that increments it. The
// investigator's runWithRepo and runWithoutRepo paths both rely on this.
//
// Confidence fast path: if the top hypothesis confidence >=
// ConfidenceFastPath (0.7), evidence gathering is skipped (the analyst will
// pick this hypothesis directly).
//
// Confidence loop-back: if the survivor confidence <
// confidenceLoopBackThreshold (0.3) and we're on iteration < 3, the pipeline
// wipes hypotheses/evidence/root cause and loops back to the investigator.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// Pipeline runs all agents in sequence with rewind support.
type Pipeline struct {
	investigator     *Investigator
	evidenceGatherer *EvidenceGatherer
	analyst          *Analyst
	fixProposer      *FixProposer
	testGenerator    *TestGenerator
	reproducerGen    *ReproducerGenerator
	betaStore        BetaStore

	// agentLoopRunner, if set, replaces the legacy investigator's Run method
	// for the hypothesis/root-cause phase. The legacy investigator is still
	// used for: (1) repo allowlist propagation via SetAllowedRepos, (2)
	// SetGitHost / SetDefaultBranch propagation to the cloner, and (3)
	// router access in RunSecurityCheck. Only the Run call is delegated.
	//
	// Set via SetAgentLoopRunner; nil means "use legacy investigator"
	// (default — preserves backward compatibility).
	agentLoopRunner InvestigatorRunner

	investigatorTimeoutSec      int
	evidenceGathererTimeoutSec  int
	analystTimeoutSec           int
	fixProposerTimeoutSec       int
	confidenceLoopBackThreshold float64

	generateTests      bool
	generateReproducer bool
	maxIterations      int
	skipPostFix        bool // when true, Run() skips test/reproducer/security — caller runs RunPostFixArtifacts separately
}

// InvestigatorRunner is the contract for the hypothesis/root-cause phase of
// the pipeline. The legacy *Investigator satisfies it; the new
// internal/agent.PipelineAdapter also satisfies it so the pipeline can swap
// to the agent loop without touching the rest of the pipeline.
type InvestigatorRunner interface {
	Run(ctx context.Context, state *schemas.AgentState) error
}

// NewPipeline creates a Pipeline with optional vector store for the
// investigator's memory loop. betaStore may be nil (uses Beta(1,1) prior).
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

// SetConfig sets configurable pipeline timeouts (seconds) and the
// confidence loop-back threshold.
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

// SetLogQuerier propagates a log backend (Loki) to the EvidenceGatherer.
func (p *Pipeline) SetLogQuerier(q LogQuerier) {
	p.evidenceGatherer.SetLogQuerier(q)
}

// SetTraceQuerier propagates a tracing backend (Jaeger/Tempo) to the EvidenceGatherer.
func (p *Pipeline) SetTraceQuerier(q TraceQuerier) {
	p.evidenceGatherer.SetTraceQuerier(q)
}

// SetAllowedRepos propagates the GitHub repo allowlist to the Investigator.
func (p *Pipeline) SetAllowedRepos(repos []string) {
	p.investigator.SetAllowedRepos(repos)
}

// SetGitHost propagates the git host to the Investigator + FixProposer.
func (p *Pipeline) SetGitHost(host string) {
	p.investigator.SetGitHost(host)
	p.fixProposer.SetGitHost(host)
}

// SetDefaultBranch propagates the default branch fallback to the
// Investigator + FixProposer.
func (p *Pipeline) SetDefaultBranch(branch string) {
	p.investigator.SetDefaultBranch(branch)
	p.investigator.cloner.WithDefaultBranch(branch)
	p.fixProposer.SetDefaultBranch(branch)
}

// SetLLMPriorWeight propagates the LLM prior weight to the Analyst.
func (p *Pipeline) SetLLMPriorWeight(w float64) {
	p.analyst.SetLLMPriorWeight(w)
}

// SetMaxIterations overrides the default max iterations (5). Must be called
// before Run; the pipeline rewrites state.MaxIterations at the start of Run.
func (p *Pipeline) SetMaxIterations(n int) {
	if n > 0 {
		p.maxIterations = n
	}
}

// SetSkipPostFix toggles whether Run runs the post-fix artifacts (test,
// reproducer, security) itself or returns after the fix proposer. When true,
// the caller runs RunPostFixArtifacts separately.
func (p *Pipeline) SetSkipPostFix(skip bool) {
	p.skipPostFix = skip
}

// SetAgentLoopRunner swaps the hypothesis/root-cause phase to use the supplied
// runner (typically *agent.PipelineAdapter) instead of the legacy
// investigator. Pass nil to revert to the legacy investigator.
//
// The legacy *Investigator is still kept around for: repo allowlist/git host
// propagation (SetAllowedRepos/SetGitHost/SetDefaultBranch still configure
// the legacy investigator's cloner, which the agent.PipelineAdapter also
// shares if constructed with the same cloner) and router access in
// RunSecurityCheck (p.investigator.router).
func (p *Pipeline) SetAgentLoopRunner(r InvestigatorRunner) {
	p.agentLoopRunner = r
}

// AgentLoopEnabled returns true if the agent-loop runner is currently active.
// Used by sentinelctl status + the API to surface which investigator path is
// wired in.
func (p *Pipeline) AgentLoopEnabled() bool {
	return p.agentLoopRunner != nil
}

// RetryFixProposal re-runs ONLY the fix proposer with the current state. The
// caller sets state.SandboxFeedback with the failure details before calling
// this. The fix proposer sees the feedback in its user message and generates
// a new, corrected fix. Does NOT re-run the investigator/evidence/analyst —
// those are expensive and the root cause hasn't changed.
func (p *Pipeline) RetryFixProposal(ctx context.Context, state *schemas.AgentState) error {
	fpCtx, fpCancel := context.WithTimeout(ctx, time.Duration(p.fixProposerTimeoutSec)*time.Second)
	defer fpCancel()
	if err := p.fixProposer.Run(fpCtx, state); err != nil {
		return fmt.Errorf("fix_proposer_retry: %w", err)
	}
	return nil
}

// Run executes the pipeline. Returns nil on natural completion or fatal
// state transition (StatusFailed, no hypotheses, no root cause). Returns an
// error only if an agent returns an error or max iterations is reached.
func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
	defer recoverPanic("pipeline")

	if p.maxIterations > 0 {
		state.MaxIterations = p.maxIterations
	}

	for state.Iterations < state.MaxIterations {
		state.Iterations++

		invCtx, invCancel := context.WithTimeout(ctx, time.Duration(p.investigatorTimeoutSec)*time.Second)
		// Delegate to the agent-loop runner when configured; otherwise fall
		// back to the legacy 2-round investigator. Either path produces
		// state.Hypotheses (and optionally state.RootCause) for the analyst.
		runner := p.agentLoopRunner
		if runner == nil {
			runner = p.investigator
		}
		if err := runner.Run(invCtx, state); err != nil {
			invCancel()
			return fmt.Errorf("investigator: %w", err)
		}
		invCancel()
		if state.Status == schemas.StatusFailed || len(state.Hypotheses) == 0 {
			return nil
		}

		// Confidence fast path: skip evidence gathering if the top
		// hypothesis is already confident enough.
		topConfidence := float64(0)
		for _, h := range state.Hypotheses {
			if h.Confidence > topConfidence {
				topConfidence = h.Confidence
			}
		}
		if topConfidence >= ConfidenceFastPath {
			logging.Info("top hypothesis confidence above fast-path threshold — skipping evidence gathering",
				"top_confidence", topConfidence, "threshold", ConfidenceFastPath)
		} else {
			evCtx, evCancel := context.WithTimeout(ctx, time.Duration(p.evidenceGathererTimeoutSec)*time.Second)
			if err := p.evidenceGatherer.Run(evCtx, state); err != nil {
				evCancel()
				return fmt.Errorf("evidence_gatherer: %w", err)
			}
			evCancel()
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
			logging.Info("root-cause confidence below loop-back threshold — re-running investigator",
				"confidence", state.RootCause.Confidence, "threshold", p.confidenceLoopBackThreshold, "iteration", state.Iterations)
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

// RunPostFixArtifacts runs the post-fix generators (regression test +
// reproducer) and the security scanner in parallel. All three are
// non-blocking on failure — test/reproducer failures are logged, security
// failures block the fix (state transitions to StatusGovernanceReview).
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
			defer recoverPanic("test_generator")
			testCtx, testCancel := context.WithTimeout(ctx, 60*time.Second)
			defer testCancel()
			testResult, testErr = p.testGenerator.Generate(testCtx, state)
		}()
	}
	if p.generateReproducer {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverPanic("reproducer_generator")
			reproCtx, reproCancel := context.WithTimeout(ctx, 60*time.Second)
			defer reproCancel()
			reproResult, reproErr = p.reproducerGen.Generate(reproCtx, state)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverPanic("security_scanner")
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
		logging.Info("regression test generated", "test_file", testResult.TestFile, "framework", testResult.TestFramework, "language", testResult.TestLanguage)
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
		logging.Info("reproducer script generated", "script_file", reproResult.ScriptFile, "language", reproResult.ScriptLanguage)
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
			logging.Error("security check FAILED", "summary", secResult.Summary)
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

// isRewindBy returns true if the most recent RewindEvent was raised by agent.
func isRewindBy(state *schemas.AgentState, agent string) bool {
	if len(state.RewindEvents) == 0 {
		return false
	}
	last := state.RewindEvents[len(state.RewindEvents)-1]
	return last.RaisedBy == agent
}

// Package agents implements the 4-agent reasoning pipeline.
// Replaces the Python LangGraph multi-agent system with a plain Go state machine.
//
// Pipeline:
//   Investigator → EvidenceGatherer → Analyst → FixProposer
//
// Any agent can raise a RewindEvent which wipes hypotheses and returns
// control to the Investigator (max 5 iterations).
package agents

import (
        "context"
        "fmt"
        "log"
        "time"

        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/llm"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// Pipeline runs all 4 agents in sequence with rewind support.
// Returns the final AgentState.
type Pipeline struct {
        investigator     *Investigator
        evidenceGatherer *EvidenceGatherer
        analyst          *Analyst
        fixProposer      *FixProposer
        betaStore        BetaStore

        // v3.6.0: Configurable timeouts (seconds) — set via SetConfig
        investigatorTimeoutSec       int
        evidenceGathererTimeoutSec   int
        analystTimeoutSec            int
        fixProposerTimeoutSec        int
        confidenceLoopBackThreshold  float64
}

// NewPipeline creates a Pipeline with optional vector store for memory loop.
func NewPipeline(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, betaStore BetaStore, vectorStore VectorStore) *Pipeline {
        return &Pipeline{
                investigator:                NewInvestigator(router, k8sClient, gitHubToken, vectorStore),
                evidenceGatherer:            NewEvidenceGatherer(router, k8sClient),
                analyst:                     NewAnalyst(router),
                fixProposer:                 NewFixProposer(router, k8sClient, gitHubToken),
                betaStore:                   betaStore,
                investigatorTimeoutSec:      120,
                evidenceGathererTimeoutSec:  90,
                analystTimeoutSec:           15,
                fixProposerTimeoutSec:       120,
                confidenceLoopBackThreshold: 0.3,
        }
}

// SetConfig sets configurable pipeline parameters.
// v3.6.0: Allows tuning timeouts and thresholds via env vars.
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
// v3.4.0: Allows evidence queries of type "prometheus" to execute against
// a real Prometheus instance. No-op if URL is empty (backward compatible).
func (p *Pipeline) SetPrometheusURL(url string) {
        p.evidenceGatherer.SetPrometheusURL(url)
}

// Run executes the full pipeline on the given state. Handles rewinds.
// v3.6.0: Speed optimization — skip evidence gathering when the top hypothesis
// already has high confidence (>= 0.7). Evidence gathering adds ~90s of LLM +
// K8s API latency; for obvious errors like KeyError with the key name in the
// traceback, it's unnecessary.
func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
        defer recoverPanic("pipeline")

        for state.Iterations < state.MaxIterations {
                state.Iterations++

                // 1. Investigator
                invCtx, invCancel := context.WithTimeout(ctx, time.Duration(p.investigatorTimeoutSec)*time.Second)
                if err := p.investigator.Run(invCtx, state); err != nil {
                        invCancel()
                        return fmt.Errorf("investigator: %w", err)
                }
                invCancel()
                if state.Status == schemas.StatusFailed || len(state.Hypotheses) == 0 {
                        return nil
                }

                // v3.6.0: Skip evidence gathering if the top hypothesis is already
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
                        log.Printf("[pipeline] top hypothesis confidence %.2f >= 0.7 — skipping evidence gathering (fast path)", topConfidence)
                } else {
                        // 2. Evidence Gatherer
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

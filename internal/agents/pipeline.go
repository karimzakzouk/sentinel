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
}

// NewPipeline creates a Pipeline with optional vector store for memory loop.
func NewPipeline(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, betaStore BetaStore, vectorStore VectorStore) *Pipeline {
        return &Pipeline{
                investigator:     NewInvestigator(router, k8sClient, gitHubToken, vectorStore),
                evidenceGatherer: NewEvidenceGatherer(router, k8sClient),
                analyst:          NewAnalyst(router),
                fixProposer:      NewFixProposer(router, k8sClient, gitHubToken),
                betaStore:        betaStore,
        }
}

// SetPrometheusURL wires the Prometheus backend into the evidence gatherer.
// v3.4.0: Allows evidence queries of type "prometheus" to execute against
// a real Prometheus instance. No-op if URL is empty (backward compatible).
func (p *Pipeline) SetPrometheusURL(url string) {
        p.evidenceGatherer.SetPrometheusURL(url)
}

// Run executes the full pipeline on the given state. Handles rewinds.
// v3.2: confidence routing — if confidence < 0.5, loop back (max 3 cycles).
// v3.2: per-agent timeouts — each agent has a hard timeout.
func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
        defer recoverPanic("pipeline")

        for state.Iterations < state.MaxIterations {
                // 1. Investigator (120s timeout — two-round agentic loop = 2 LLM calls)
                invCtx, invCancel := context.WithTimeout(ctx, 120*time.Second)
                if err := p.investigator.Run(invCtx, state); err != nil {
                        invCancel()
                        return fmt.Errorf("investigator: %w", err)
                }
                invCancel()
                if state.Status == schemas.StatusFailed || len(state.Hypotheses) == 0 {
                        return nil
                }

                // 2. Evidence Gatherer (60s timeout)
                evCtx, evCancel := context.WithTimeout(ctx, 90*time.Second)
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

                // 3. Analyst (10s timeout — pure math)
                anCtx, anCancel := context.WithTimeout(ctx, 15*time.Second)
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

                // v3.2: Confidence routing
                // < 0.3 → no hypothesis is strong enough, loop back (up to 3 cycles)
                // 0.3–0.7 → root cause identified, proceed to fix
                // ≥ 0.7 → high confidence, proceed to fix
                if state.RootCause.Confidence < 0.3 && state.Iterations < 3 {
                        log.Printf("[pipeline] confidence %.2f < 0.3 — looping back to investigator (iteration %d)", state.RootCause.Confidence, state.Iterations)
                        state.Hypotheses = nil
                        state.Evidence = nil
                        state.RootCause = nil
                        continue
                }

                // 4. Fix Proposer (120s timeout — LLM free tiers can be slow)
                fpCtx, fpCancel := context.WithTimeout(ctx, 120*time.Second)
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

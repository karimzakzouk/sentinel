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

        "github.com/karimzakzoukz/sentinel/v3/internal/k8s"
        "github.com/karimzakzoukz/sentinel/v3/internal/llm"
        "github.com/karimzakzoukz/sentinel/v3/internal/schemas"
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

// NewPipeline creates a Pipeline.
func NewPipeline(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, betaStore BetaStore) *Pipeline {
        return &Pipeline{
                investigator:     NewInvestigator(router, k8sClient, gitHubToken),
                evidenceGatherer: NewEvidenceGatherer(router, k8sClient),
                analyst:          NewAnalyst(router),
                fixProposer:      NewFixProposer(router, k8sClient, gitHubToken),
                betaStore:        betaStore,
        }
}

// Run executes the full pipeline on the given state. Handles rewinds.
func (p *Pipeline) Run(ctx context.Context, state *schemas.AgentState) error {
        for state.Iterations < state.MaxIterations {
                // 1. Investigator
                if err := p.investigator.Run(ctx, state); err != nil {
                        return fmt.Errorf("investigator: %w", err)
                }
                if state.Status == schemas.StatusFailed || len(state.Hypotheses) == 0 {
                        return nil
                }

                // 2. Evidence Gatherer
                if err := p.evidenceGatherer.Run(ctx, state); err != nil {
                        return fmt.Errorf("evidence_gatherer: %w", err)
                }
                if state.Status == schemas.StatusFailed {
                        return nil
                }
                // Check for rewind
                if isRewindBy(state, "evidence_gatherer") {
                        continue
                }

                // 3. Analyst
                if err := p.analyst.Run(ctx, state, p.betaStore); err != nil {
                        return fmt.Errorf("analyst: %w", err)
                }
                if state.Status == schemas.StatusFailed {
                        return nil
                }
                if state.RootCause == nil {
                        // All hypotheses falsified — rewind
                        if isRewindBy(state, "analyst") {
                                continue
                        }
                        return nil
                }

                // 4. Fix Proposer
                if err := p.fixProposer.Run(ctx, state); err != nil {
                        return fmt.Errorf("fix_proposer: %w", err)
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

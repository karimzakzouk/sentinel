package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/codeintel"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repo"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// Investigator wraps the agent loop for use in the investigation pipeline.
// It replaces the 2-round LLM pipeline in internal/agents/investigator.go
// with an N-turn agent loop that uses code intelligence tools.
type Investigator struct {
	router   *llm.Router
	cfg      AgentConfig
	repoPath string
}

// NewInvestigator creates an agent-based investigator.
func NewInvestigator(router *llm.Router, repoPath string, cfg AgentConfig) *Investigator {
	return &Investigator{
		router:   router,
		cfg:      cfg,
		repoPath: repoPath,
	}
}

// Run executes the agent investigation loop and populates the AgentState
// with hypotheses, root cause, and source files.
func (inv *Investigator) Run(ctx context.Context, state *schemas.AgentState) error {
	podLogs := extractPodLogs(state)
	if podLogs == "" {
		agents.GenerateHypothesesUniversal("", state)
		return nil
	}

	logging.Info("agent: building code intelligence index", "repo", inv.repoPath)
	idx, err := codeintel.BuildIndex(inv.repoPath, 500)
	if err != nil {
		logging.Error("agent: codeintel build failed, falling back to error parser", "err", err)
		hyps := agents.GenerateHypothesesUniversal(podLogs, state)
		state.Hypotheses = hyps
		state.SourceFiles = extractSourceFiles(hyps)
		return nil
	}
	logging.Info("agent: code intelligence index built", "symbols", len(idx.Symbols), "files", len(idx.CallGraph.Files))

	a := New(inv.router, inv.cfg)
	a.RegisterCodeIntelTools(idx)
	a.RegisterRepoTools(inv.repoPath)

	systemPrompt := buildSystemPrompt(podLogs, state, idx)

	logging.Info("agent: starting investigation loop", "max_turns", inv.cfg.MaxTurns)
	result, err := a.Run(ctx, systemPrompt)
	if err != nil {
		logging.Error("agent: loop failed, falling back", "err", err)
		hyps := agents.GenerateHypothesesUniversal(podLogs, state)
		state.Hypotheses = hyps
		state.SourceFiles = extractSourceFiles(hyps)
		return nil
	}

	logging.Info("agent: investigation complete",
		"turns", result.TurnsUsed,
		"confidence", result.Confidence,
		"files", result.SourceFiles,
		"root_cause", result.RootCause)

	state.Hypotheses = []schemas.Hypothesis{{
		HID:         "H1",
		Statement:   result.RootCause,
		Confidence:  result.Confidence,
		SourceFiles: result.SourceFiles,
	}}

	if result.Confidence >= 0.5 {
		state.RootCause = &schemas.RootCause{
			Statement:   result.RootCause,
			Confidence:  result.Confidence,
			SourceFiles: result.SourceFiles,
		}
		state.Status = schemas.StatusRootCaused
	}

	state.SourceFiles = result.SourceFiles

	fetched := make(map[string]string)
	for _, path := range result.SourceFiles {
		content, err := repo.ReadFile(inv.repoPath, path)
		if err == nil {
			fetched[path] = content
		}
	}
	state.FetchedCode = fetched

	for _, turn := range result.Trace {
		state.AgentTrace = append(state.AgentTrace, map[string]any{
			"agent":      "agent_loop",
			"turn":       turn.TurnNumber,
			"tool":       turn.ToolCall,
			"result_len": len(turn.LLMResponse),
		})
	}

	return nil
}

func buildSystemPrompt(podLogs string, state *schemas.AgentState, idx *codeintel.Index) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Service: %s\n", state.Cluster.Service)
	fmt.Fprintf(&b, "Namespace: %s\n", state.Cluster.Namespace)
	fmt.Fprintf(&b, "\n## Crash Logs\n\n%s\n", truncate(podLogs, 4000))

	// Runtime ground truth (v1.7.4) — env var names the deployment
	// actually sets, so agent hypotheses about "missing config" are
	// checked against reality instead of training-data priors.
	if rcBlock := agents.RenderRuntimeContext(state.RuntimeContext); rcBlock != "" {
		fmt.Fprintf(&b, "\n%s\n", rcBlock)
	}

	if idx != nil && len(idx.Symbols) > 0 {
		symbolList := idx.FormatSymbolList()
		if len(symbolList) > 4000 {
			symbolList = symbolList[:4000] + "...(truncated)"
		}
		fmt.Fprintf(&b, "\n## Repo Symbols\n\n%s\n", symbolList)
	}

	parsed := agents.ParseError(podLogs)
	if parsed != nil {
		fmt.Fprintf(&b, "\n## Parsed Error\n")
		fmt.Fprintf(&b, "Type: %s\n", parsed.ErrorType)
		fmt.Fprintf(&b, "Detail: %s\n", parsed.ErrorDetail)
		fmt.Fprintf(&b, "File: %s\n", parsed.SourceFile)
		fmt.Fprintf(&b, "Line: %s\n", parsed.SourceLine)
		fmt.Fprintf(&b, "Language: %s\n", parsed.Language)
	}

	return b.String()
}

func extractPodLogs(state *schemas.AgentState) string {
	for _, a := range state.Cluster.Anomalies {
		if a.Explanation != "" {
			return a.Explanation
		}
	}
	return state.Cluster.Summary
}

func extractSourceFiles(hyps []schemas.Hypothesis) []string {
	seen := map[string]bool{}
	var files []string
	for _, h := range hyps {
		for _, f := range h.SourceFiles {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

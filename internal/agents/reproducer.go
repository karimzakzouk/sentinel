package agents

// reproducer.go: the LLM agent that writes a standalone reproducer script
// for sandbox Level 5 verification. Unlike the test generator (Level 4),
// this doesn't require a test framework — it's a plain script that calls
// the buggy function and asserts it doesn't crash.
//
// The reproducer runs against the canary image in the sandbox. Exit 0 means
// the fix worked (no crash); non-zero means the fix didn't work (still crashes).

import (
	"context"
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

const reproducerSystemPrompt = `You are Sentinel's Reproducer Generator agent. Your job is to write a standalone script that reproduces a production error.

## Your Task
Given:
- The root cause of a production incident
- The source code (after the fix is applied)

Generate a standalone script that:
1. Calls the function/endpoint that originally crashed
2. Asserts it does NOT crash (exits 0 if OK, exits 1 if crash)
3. Is SELF-CONTAINED — no external dependencies, no test framework needed

## Script Requirements
- Use the same language as the source code
- The script should be runnable directly: python script.py, go run script.go, node script.js
- If the function requires arguments, use the EXACT arguments that caused the crash (from the error context)
- The script must exit 0 on success (no crash) and exit 1 on failure (crash)
- Do NOT use a test framework (no pytest, no jest, no junit) — just a plain script

## Response Format
Return ONLY a JSON object:
{
  "script_file": "reproduce_error.py",
  "script_language": "python",
  "script_content": "#!/usr/bin/env python3\nimport sys\nfrom app.main import format_price\ntry:\n    result = format_price(50)\n    print(f'Result: {result}')\n    sys.exit(0)\nexcept Exception as e:\n    print(f'CRASH: {e}', file=sys.stderr)\n    sys.exit(1)",
  "run_command": "python /app/reproduce_error.py",
  "description": "Calls format_price(50) — was TypeError, should now return 'Price: 50'"
}

No prose before or after the JSON.`

// GeneratedReproducer is an alias for schemas.GeneratedReproducer. The
// canonical type lives in internal/schemas so the executors package doesn't
// have to import agents.
type GeneratedReproducer = schemas.GeneratedReproducer

// ReproducerGenerator generates standalone reproducer scripts.
type ReproducerGenerator struct {
	router *llm.Router
}

// NewReproducerGenerator creates a ReproducerGenerator.
func NewReproducerGenerator(router *llm.Router) *ReproducerGenerator {
	return &ReproducerGenerator{router: router}
}

// Generate produces a reproducer script for the given incident + fix.
func (g *ReproducerGenerator) Generate(ctx context.Context, state *schemas.AgentState) (*GeneratedReproducer, error) {
	if state.RootCause == nil || state.FixProposal == nil {
		return nil, fmt.Errorf("root cause and fix proposal are required")
	}

	userMsg := g.buildUserMessage(state)
	result, err := g.router.FrontierJSON(ctx, reproducerSystemPrompt, userMsg, 3000)
	if err != nil {
		return nil, fmt.Errorf("reproducer LLM call: %w", err)
	}

	scriptFile, _ := result["script_file"].(string)
	scriptLanguage, _ := result["script_language"].(string)
	scriptContent, _ := result["script_content"].(string)
	runCommand, _ := result["run_command"].(string)
	description, _ := result["description"].(string)

	if scriptFile == "" || scriptContent == "" {
		return nil, fmt.Errorf("LLM returned empty script file or content")
	}

	return &GeneratedReproducer{
		ScriptFile:     scriptFile,
		ScriptLanguage: scriptLanguage,
		ScriptContent:  scriptContent,
		RunCommand:     runCommand,
		Description:    description,
	}, nil
}

// buildUserMessage constructs the prompt for the reproducer LLM: root cause,
// error context (so the script uses the right inputs), fixed source code,
// fix description, and language hint.
func (g *ReproducerGenerator) buildUserMessage(state *schemas.AgentState) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("# Service: %s", state.Cluster.Service))
	parts = append(parts, "")

	parts = append(parts, "## Root Cause")
	if state.RootCause == nil {
		return ""
	}
	parts = append(parts, fmt.Sprintf("- statement: %s", state.RootCause.Statement))
	parts = append(parts, "")

	// Error context — critical so the reproducer uses the right inputs.
	parts = append(parts, "## Error Context (from pod logs — use these exact inputs)")
	for _, anomaly := range state.Cluster.Anomalies {
		explanation := anomaly.Explanation
		if len(explanation) > 3000 {
			explanation = explanation[:3000]
		}
		parts = append(parts, explanation)
	}
	parts = append(parts, "")

	// Fixed source code.
	parts = append(parts, "## Source Code (after fix is applied)")
	i := 0
	for path, content := range state.FetchedCode {
		if i >= 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("### %s", path))
		parts = append(parts, "```")
		if len(content) > 5000 {
			content = content[:5000]
		}
		parts = append(parts, content)
		parts = append(parts, "```")
		i++
	}
	parts = append(parts, "")

	if state.FixProposal.Description != "" {
		parts = append(parts, "## Fix Description")
		parts = append(parts, state.FixProposal.Description)
		parts = append(parts, "")
	}

	if lang := detectLanguageFromState(state); lang != "" {
		parts = append(parts, fmt.Sprintf("## Detected Language: %s", lang))
	}

	parts = append(parts, "")
	parts = append(parts, "## Your Task")
	parts = append(parts, "Generate a standalone script that calls the function that originally crashed.")
	parts = append(parts, "Use the EXACT inputs from the error context.")
	parts = append(parts, "The script must exit 0 if the function works, exit 1 if it crashes.")

	return strings.Join(parts, "\n")
}

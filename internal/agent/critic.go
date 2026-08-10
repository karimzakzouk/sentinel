package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// criticReview runs a second LLM call that reviews the investigator's
// root cause hypothesis. If the critic disagrees, the agent continues
// investigating instead of declaring done.
//
// This is the "Reflexion" pattern — after each attempt, the agent
// critiques its own output and retries with that critique in memory.
// The critic is a separate LLM call with a different prompt that
// specifically looks for:
//   - Does the root cause actually explain the error in the pod logs?
//   - Is the identified file the right one?
//   - Is there a more likely explanation?
//
// Based on: Reflexion paper (HumanEval 80% → 91%) + ALMAS multi-agent.
func (a *Agent) criticReview(ctx context.Context, rootCause string, files []string, podLogs string, conversation []map[string]string) (approved bool, critique string) {
	criticPrompt := fmt.Sprintf(`You are a senior SRE reviewing another agent's root cause analysis.

Pod logs (the crash):
%s

Proposed root cause:
%s

Files to change:
%v

Review this root cause analysis. Does it correctly explain the crash?
- Does the root cause match the actual error in the logs?
- Is the identified file the right location of the bug?
- Is there a more likely explanation?

Respond with JSON only:
{"approved": true/false, "critique": "one sentence explanation"}`, truncate(podLogs, 500), rootCause, files)

	criticCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	response, err := a.router.FrontierText(criticCtx,
		"You are a code review critic. Respond with JSON only.",
		criticPrompt,
		200,
	)
	cancel()

	if err != nil {
		// If the critic fails, approve by default — don't block on errors.
		return true, ""
	}

	var result struct {
		Approved bool   `json:"approved"`
		Critique string `json:"critique"`
	}

	// Try to parse the critic's response.
	jsonStr := response
	if idx := strings.Index(response, "{"); idx >= 0 {
		if end := strings.LastIndex(response, "}"); end > idx {
			jsonStr = response[idx : end+1]
		}
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// If parse fails, approve by default.
		return true, ""
	}

	return result.Approved, result.Critique
}

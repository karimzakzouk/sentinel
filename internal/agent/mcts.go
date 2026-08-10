package agent

import (
	"context"
	"fmt"
	"strings"
)

// generatePlans produces N investigation plans using the LLM. Each plan
// is generated independently with temperature variation so the plans
// explore different approaches.
//
// This is a simplified version of Monte Carlo Tree Search — instead of
// exploring one path, we generate multiple plans and pick the most
// promising one. The key insight from the MCTS paper (EMNLP 2025) is
// that exploring multiple paths improves pass rates by 15-20% on small
// models.
//
// We use "best-of-N" planning instead of full MCTS because:
// 1. Each tool call takes real time (file I/O) — deep simulations are impractical
// 2. The LLM can evaluate plans cheaply (one call) vs executing them (many calls)
// 3. It's simpler and more robust than maintaining a tree data structure
func (a *Agent) generatePlans(ctx context.Context, conversation []map[string]string, n int) []string {
	plans := make([]string, 0, n)

	for i := 0; i < n; i++ {
		planCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
		prompt := fmt.Sprintf(
			"You are an autonomous SRE agent. Read the crash context and write a brief 2-3 step investigation plan. "+
				"Which files will you read? What will you search for? Be specific. "+
				"Plan #%d — try a different approach than the obvious one.", i+1)

		plan, err := a.router.FrontierText(planCtx, prompt, formatConversation(conversation), 300)
		cancel()

		if err != nil || plan == "" {
			continue
		}
		plans = append(plans, plan)
	}

	return plans
}

// pickBestPlan selects the most promising plan from a list. It uses a
// simple heuristic: the plan that mentions the most specific file paths
// and function names is likely the most targeted.
//
// A more sophisticated version would use an LLM to score each plan,
// but that adds another LLM call. The heuristic is good enough for
// small models and doesn't add latency.
func pickBestPlan(plans []string, podLogs string) string {
	if len(plans) == 0 {
		return ""
	}
	if len(plans) == 1 {
		return plans[0]
	}

	bestScore := -1
	bestPlan := plans[0]

	for _, plan := range plans {
		score := scorePlan(plan, podLogs)
		if score > bestScore {
			bestScore = score
			bestPlan = plan
		}
	}

	return bestPlan
}

// scorePlan evaluates how promising a plan is based on specificity.
// More specific plans (mentioning file paths, function names, line numbers)
// tend to be more targeted and effective.
func scorePlan(plan string, podLogs string) int {
	score := 0

	// Points for mentioning file paths (e.g., "src/handlers.rs", "app/main.py")
	filePatterns := []string{".go", ".py", ".rs", ".ts", ".java", ".rb", ".cpp", ".yaml", ".yml"}
	for _, ext := range filePatterns {
		if strings.Contains(plan, ext) {
			score += 2
		}
	}

	// Points for mentioning specific actions
	actionKeywords := []string{"read", "search", "find", "trace", "check", "inspect", "examine"}
	for _, kw := range actionKeywords {
		if strings.Contains(strings.ToLower(plan), kw) {
			score += 1
		}
	}

	// Points for mentioning concepts from the pod logs
	if podLogs != "" {
		logKeywords := extractKeywords(podLogs)
		for _, kw := range logKeywords {
			if strings.Contains(strings.ToLower(plan), strings.ToLower(kw)) {
				score += 3
			}
		}
	}

	// Penalty for vague plans
	vagueWords := []string{"investigate", "look at", "explore", "check the code"}
	for _, vw := range vagueWords {
		if strings.Contains(strings.ToLower(plan), vw) && score > 0 {
			score -= 1
		}
	}

	return score
}

// extractKeywords pulls potential identifiers from pod logs (file paths,
// function names, error types).
func extractKeywords(logs string) []string {
	var keywords []string

	// Extract file paths from stack traces (e.g., "main.go:48")
	lines := strings.Split(logs, "\n")
	for _, line := range lines {
		// Look for file:line patterns
		if idx := strings.Index(line, ":"); idx > 0 {
			// Walk backwards to find the start of the path
			start := idx
			for start > 0 && (isPathChar(line[start-1])) {
				start--
			}
			if start < idx {
				path := line[start:idx]
				if len(path) > 2 {
					keywords = append(keywords, path)
				}
			}
		}

		// Extract error types (e.g., "KeyError", "NullPointerException", "panic")
		words := strings.Fields(line)
		for _, w := range words {
			clean := strings.Trim(w, "(){}[],.;:\"'")
			if isLikelyIdentifier(clean) {
				keywords = append(keywords, clean)
			}
		}
	}

	return keywords
}

func isPathChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '.' || c == '/' || c == '_' || c == '-'
}

func isLikelyIdentifier(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	hasUpper := false
	hasLower := false
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			hasUpper = true
		}
		if c >= 'a' && c <= 'z' {
			hasLower = true
		}
	}
	// Identifiers like "KeyError", "NullPointerException", "handleProduct"
	return hasUpper && hasLower
}

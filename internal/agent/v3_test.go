package agent

import (
	"testing"
)

func TestPickBestPlan_PrefersSpecificFiles(t *testing.T) {
	plans := []string{
		"Investigate the crash by looking at the code.",
		"Read src/handlers.rs:19 to find the unwrap() call, then search for callers of the search function.",
		"Check the database connection and look at the config.",
	}
	podLogs := "panicked at src/handlers.rs:19:34"

	best := pickBestPlan(plans, podLogs)
	if !containsStr(best, "handlers.rs") {
		t.Errorf("expected best plan to mention handlers.rs, got: %s", best)
	}
}

func TestPickBestPlan_SinglePlan(t *testing.T) {
	plans := []string{"Only plan available."}
	best := pickBestPlan(plans, "")
	if best != "Only plan available." {
		t.Errorf("expected single plan to be returned as-is")
	}
}

func TestPickBestPlan_Empty(t *testing.T) {
	best := pickBestPlan([]string{}, "")
	if best != "" {
		t.Errorf("expected empty string for no plans")
	}
}

func TestScorePlan_RewardsSpecificity(t *testing.T) {
	vague := "Investigate the crash by looking at the code."
	specific := "Read src/handlers.rs and search for unwrap() calls in the search function."

	vagueScore := scorePlan(vague, "panic at handlers.rs")
	specificScore := scorePlan(specific, "panic at handlers.rs")

	if specificScore <= vagueScore {
		t.Errorf("specific plan should score higher: specific=%d vague=%d", specificScore, vagueScore)
	}
}

func TestExtractKeywords_FindsFilePaths(t *testing.T) {
	logs := "panic at src/handlers.rs:19:34\ncalled Option::unwrap() on a None value"
	keywords := extractKeywords(logs)

	found := false
	for _, kw := range keywords {
		if kw == "src/handlers.rs" || kw == "handlers.rs" || kw == "handlers" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find a handler-related keyword in: %v", keywords)
	}
}

func TestExtractKeywords_FindsErrorTypes(t *testing.T) {
	logs := "KeyError: 'DATABASE_URL'"
	keywords := extractKeywords(logs)

	found := false
	for _, kw := range keywords {
		if kw == "KeyError" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find 'KeyError' in keywords: %v", keywords)
	}
}

func TestFormatParallelResults(t *testing.T) {
	results := []ToolResult{
		{Tool: "read_file", Success: true, Output: "file contents"},
		{Tool: "search_code", Success: true, Output: "3 matches"},
	}
	formatted := formatParallelResults(results)
	if !containsStr(formatted, "read_file") || !containsStr(formatted, "search_code") {
		t.Errorf("expected both tool names in formatted output")
	}
	if !containsStr(formatted, "file contents") || !containsStr(formatted, "3 matches") {
		t.Errorf("expected both tool outputs in formatted output")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (s[0:len(sub)] == sub || containsStr(s[1:], sub))))
}

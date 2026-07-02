package agents

// keysOf returns the keys of a map as a slice (no defined order).

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Named constants for limits used across the agent pipeline.
// Previously these were hardcoded magic numbers scattered across files.
const (
	// MaxPodLogsBytes is the max pod log size sent to the LLM (4KB).
	// Larger logs are truncated — the LLM only needs the crash + traceback.
	MaxPodLogsBytes = 4096

	// MaxErrorExplanationChars is the max error explanation sent to the
	// test generator + security reviewer (2KB).
	MaxErrorExplanationChars = 2000

	// MaxPRBodyExcerptChars is the max chars for the PR body error excerpt (500).
	MaxPRBodyExcerptChars = 500

	// MaxFixResultChars is the max chars for fix result string (300).
	MaxFixResultChars = 300

	// MaxFilesToRead is the max number of source files the investigator
	// reads in full for root cause analysis.
	MaxFilesToRead = 5

	// MaxStackFilesToRead is the max files read from stack trace parsing.
	MaxStackFilesToRead = 5

	// MaxHypothesesPerRound is the max hypotheses the investigator generates.
	MaxHypothesesPerRound = 5

	// ConfidenceFastPath is the threshold above which evidence gathering
	// is skipped (fast path). If the top hypothesis confidence >= this,
	// the pipeline goes straight to fix proposal.
	ConfidenceFastPath = 0.7

	// ConfidenceHighMatch is the threshold for applying a known fix from
	// vector memory (similar past incident).
	ConfidenceHighMatch = 0.85

	// ConfidenceMediumMatch is the threshold for using a past incident as
	// context (but not applying its fix directly).
	ConfidenceMediumMatch = 0.5

	// ConfidenceFloor is the minimum confidence for a hypothesis (below
	// this, the hypothesis is clamped to prevent near-zero confidence).
	ConfidenceFloor = 0.05

	// ConfidencePriorMean is the default prior mean for Beta distribution
	// (used when no historical data exists).
	ConfidencePriorMean = 0.5

	// FuzzyMatchThreshold is the minimum similarity score for fuzzy
	// SEARCH/REPLACE matching (0.0-1.0).
	FuzzyMatchThreshold = 0.6
)

package agents

// keysOf returns the keys of m as a slice with no defined order.
func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Pipeline-wide constants. Kept here so they aren't magic numbers scattered
// across files.
const (
	// MaxPodLogsBytes is the max pod log size sent to the LLM.
	MaxPodLogsBytes = 4096

	// MaxErrorExplanationChars is the max error explanation sent to the
	// test generator and security reviewer.
	MaxErrorExplanationChars = 2000

	// MaxPRBodyExcerptChars is the max chars for the PR body error excerpt.
	MaxPRBodyExcerptChars = 500

	// MaxFixResultChars is the max chars for fix result strings.
	MaxFixResultChars = 300

	// MaxFilesToRead is the max source files the investigator reads in full.
	MaxFilesToRead = 5

	// MaxStackFilesToRead is the max files read from stack-trace parsing.
	MaxStackFilesToRead = 5

	// MaxHypothesesPerRound is the max hypotheses the investigator generates.
	MaxHypothesesPerRound = 5

	// MaxSearchQueries (v1.7.16) caps the investigator's search queries
	// AFTER deterministic enrichment (LLM round-1 queries + stack-trace
	// identifiers + fault-class hint vocabulary). Run #29: 8 LLM-chosen
	// queries found the crash file but never the mechanism file.
	MaxSearchQueries = 12

	// MaxSearchResultsShown (v1.7.16) is how many search hits round 2
	// displays to the LLM. Was MaxStackFilesToRead (5) — the LLM chose
	// files_to_read from a 5-line glimpse of the repo.
	MaxSearchResultsShown = 24

	// ConfidenceFastPath: above this, evidence gathering is skipped — but
	// ONLY when the fast path is explicitly re-enabled via
	// Pipeline.SetEvidenceFastPath (SENTINEL_EVIDENCE_FAST_PATH=true).
	// Default off: self-reported confidence must never gate verification.
	ConfidenceFastPath = 0.7

	// ConfidenceHighMatch: above this, a known fix from vector memory is applied.
	ConfidenceHighMatch = 0.85

	// ConfidenceMediumMatch: above this, a past incident is used as context.
	ConfidenceMediumMatch = 0.5

	// ConfidenceFloor is the minimum confidence for a hypothesis (clamped).
	ConfidenceFloor = 0.05

	// ConfidencePriorMean is the default prior mean when no historical data exists.
	ConfidencePriorMean = 0.5

	// FuzzyMatchThreshold is the minimum similarity for fuzzy SEARCH/REPLACE matching.
	FuzzyMatchThreshold = 0.6
)

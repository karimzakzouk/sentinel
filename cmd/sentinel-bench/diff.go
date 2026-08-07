// Package main — diff similarity metric for sentinel-bench.
//
// The metric is line-recall: the multiset intersection of changed lines
// in the PR diff and ground-truth.diff, divided by the size of the
// ground-truth multiset. Threshold for the diff_similarity assertion: 0.80.
//
// v1 keeps the metric simple — full AST tree-edit-distance is overkill for
// an eval harness whose job is to count "did the agent change the right
// lines". Line-recall is robust to whitespace and to extra churn in the PR
// diff (extra lines beyond ground truth reduce precision, not recall).
package main

import (
	"strings"
)

// DiffSimilarity returns the line-recall of prDiff against gtDiff.
// Returns 0.0 if gtDiff has no changed lines (a malformed scenario).
//
//	lineRecall = |PR ∩ GT| / |GT|
//
// where the multiset intersection counts min(prCount[L], gtCount[L]) over
// every distinct changed line L.
func DiffSimilarity(prDiff, gtDiff string) float64 {
	gtCounts := changedLineCounts(gtDiff)
	prCounts := changedLineCounts(prDiff)

	var gtTotal int
	for _, n := range gtCounts {
		gtTotal += n
	}
	if gtTotal == 0 {
		return 0
	}

	var hits int
	for line, prN := range prCounts {
		gtN := gtCounts[line]
		if prN < gtN {
			hits += prN
		} else {
			hits += gtN
		}
	}
	return float64(hits) / float64(gtTotal)
}

// changedLineCounts returns a multiset (map[line]count) of the changed
// lines in a unified diff. A "changed line" is one that starts with '+' or
// '-' (excluding the '+++' / '---' file headers, '@@' hunk headers, and
// 'diff ' / 'index ' git metadata lines). The leading '+' or '-' marker
// is preserved so an addition is distinct from a deletion of the same text.
func changedLineCounts(diff string) map[string]int {
	counts := make(map[string]int)
	for _, raw := range strings.Split(diff, "\n") {
		if len(raw) == 0 {
			continue
		}
		// Skip file headers, hunk headers, and git metadata.
		if strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---") {
			continue
		}
		if strings.HasPrefix(raw, "@@") {
			continue
		}
		if strings.HasPrefix(raw, "diff ") || strings.HasPrefix(raw, "index ") {
			continue
		}
		// Only '+' or '-' lines are changed lines.
		if raw[0] != '+' && raw[0] != '-' {
			continue
		}
		counts[raw]++
	}
	return counts
}

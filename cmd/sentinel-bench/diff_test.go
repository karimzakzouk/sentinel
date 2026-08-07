package main

import (
	"strings"
	"testing"
)

// TestDiffSimilarity_IdenticalDiffs returns 1.0 when the PR diff matches
// the ground truth exactly.
func TestDiffSimilarity_IdenticalDiffs(t *testing.T) {
	diff := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
 a
-b
+c
 d
`
	got := DiffSimilarity(diff, diff)
	if got != 1.0 {
		t.Fatalf("identical diffs: want 1.0, got %f", got)
	}
}

// TestDiffSimilarity_DisjointDiffs returns 0.0 when the PR diff has no
// changed lines in common with the ground truth.
func TestDiffSimilarity_DisjointDiffs(t *testing.T) {
	gt := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
 a
-b
+c
`
	pr := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
 a
-x
+y
`
	got := DiffSimilarity(pr, gt)
	if got != 0.0 {
		t.Fatalf("disjoint diffs: want 0.0, got %f", got)
	}
}

// TestDiffSimilarity_EmptyGroundTruth returns 0.0 (the scenario is
// malformed; we never divide by zero).
func TestDiffSimilarity_EmptyGroundTruth(t *testing.T) {
	pr := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
 a
-b
+c
`
	got := DiffSimilarity(pr, "")
	if got != 0.0 {
		t.Fatalf("empty ground truth: want 0.0, got %f", got)
	}
}

// TestDiffSimilarity_PartialOverlap returns 0.5 when the PR diff matches
// half of the ground truth's changed lines.
func TestDiffSimilarity_PartialOverlap(t *testing.T) {
	gt := `--- a/foo
+++ b/foo
@@ -1,4 +1,4 @@
 a
-b
-c
+d
+e
`
	pr := `--- a/foo
+++ b/foo
@@ -1,4 +1,4 @@
 a
-b
-c
+x
`
	// Ground truth has 4 changed lines: -b, -c, +d, +e.
	// PR has 3 changed lines: -b, -c, +x.
	// Intersection: -b, -c (2 lines). Recall = 2 / 4 = 0.5.
	got := DiffSimilarity(pr, gt)
	want := 0.5
	if got != want {
		t.Fatalf("partial overlap: want %f, got %f", want, got)
	}
}

// TestDiffSimilarity_ExtraLinesInPR returns 1.0 when the PR diff is a
// superset of the ground truth — recall is 1.0 because every ground-truth
// line is present in the PR. Precision is not part of the metric.
func TestDiffSimilarity_ExtraLinesInPR(t *testing.T) {
	gt := `--- a/foo
+++ b/foo
@@ -1,2 +1,2 @@
 a
-b
+c
`
	pr := `--- a/foo
+++ b/foo
@@ -1,2 +1,3 @@
 a
-b
+c
+d
+e
`
	// All of gt's changed lines (-b, +c) appear in pr; pr has extra (+d, +e)
	// but recall only cares about gt coverage.
	got := DiffSimilarity(pr, gt)
	if got != 1.0 {
		t.Fatalf("superset PR: want 1.0, got %f", got)
	}
}

// TestDiffSimilarity_MultisetCountsMultiplicity verifies that repeated
// lines are counted with multiplicity (not as a single set element).
func TestDiffSimilarity_MultisetCountsMultiplicity(t *testing.T) {
	gt := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
-a
-a
+a
+b
`
	// gt changed lines: -a, -a, +a, +b  (4 lines)
	pr := `--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
-a
+a
+b
+c
`
	// pr changed lines: -a, +a, +b, +c  (4 lines)
	// intersection (multiset): -a (1), +a (1), +b (1) = 3.
	// recall = 3 / 4 = 0.75.
	got := DiffSimilarity(pr, gt)
	want := 0.75
	if got != want {
		t.Fatalf("multiset: want %f, got %f", want, got)
	}
}

// TestDiffSimilarity_AdditionDistinctFromDeletion verifies that the
// leading +/- marker is preserved so an addition is distinct from a
// deletion of the same text.
func TestDiffSimilarity_AdditionDistinctFromDeletion(t *testing.T) {
	gt := `--- a/foo
+++ b/foo
@@ -1,1 +1,1 @@
-b
`
	pr := `--- a/foo
+++ b/foo
@@ -1,1 +1,1 @@
+b
`
	// gt changed: -b. pr changed: +b. They are distinct lines.
	got := DiffSimilarity(pr, gt)
	if got != 0.0 {
		t.Fatalf("addition vs deletion: want 0.0, got %f", got)
	}
}

// TestChangedLineCounts_SkipsHeaders verifies that file headers, hunk
// headers, and git metadata are not counted as changed lines.
func TestChangedLineCounts_SkipsHeaders(t *testing.T) {
	diff := `diff --git a/foo b/foo
index abc..def 100644
--- a/foo
+++ b/foo
@@ -1,3 +1,3 @@
 a
-b
+c
 d
`
	counts := changedLineCounts(diff)
	// Only -b and +c are changed lines.
	if len(counts) != 2 {
		t.Fatalf("want 2 changed lines, got %d (%v)", len(counts), counts)
	}
	if counts["-b"] != 1 {
		t.Errorf("-b count: want 1, got %d", counts["-b"])
	}
	if counts["+c"] != 1 {
		t.Errorf("+c count: want 1, got %d", counts["+c"])
	}
}

// TestChangedLineCounts_EmptyDiff returns an empty (non-nil) map.
func TestChangedLineCounts_EmptyDiff(t *testing.T) {
	counts := changedLineCounts("")
	if counts == nil {
		t.Fatal("want non-nil map for empty input")
	}
	if len(counts) != 0 {
		t.Fatalf("want 0 entries, got %d", len(counts))
	}
}

// TestDiffSimilarity_RealS001 verifies the metric on the real S001
// ground-truth diff: an identical PR diff scores 1.0, and a diff that
// gets the image line right but no other line scores 0.5 (1 of 2 changed
// lines).
func TestDiffSimilarity_RealS001(t *testing.T) {
	gt := `--- a/deployment/k8s.yaml
+++ b/deployment/k8s.yaml
@@ -12,7 +12,7 @@ spec:
   spec:
     containers:
       - name: app
-          image: ghcr.io/sentinel-bench/sample-python-app:v1.2.3
+          image: ghcr.io/sentinel-bench/sample-python-app:v1.23
       ports:
         - containerPort: 8080
`
	if got := DiffSimilarity(gt, gt); got != 1.0 {
		t.Fatalf("identical S001: want 1.0, got %f", got)
	}
	// PR that gets the +image line right but the -image line wrong.
	pr := strings.Replace(gt, "-          image: ghcr.io/sentinel-bench/sample-python-app:v1.2.3",
		"-          image: ghcr.io/sentinel-bench/sample-python-app:OLD", 1)
	// 1 of 2 ground-truth changed lines matches.
	if got := DiffSimilarity(pr, gt); got != 0.5 {
		t.Fatalf("half-match S001: want 0.5, got %f", got)
	}
}

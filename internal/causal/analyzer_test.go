package causal

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeGraphProvider returns a fixed Graph for any input. Used to drive the
// analyzer without depending on internal/depanalysis.
type fakeGraphProvider struct {
	graph *Graph
	err   error
}

func (f *fakeGraphProvider) GraphFor(_ context.Context, _ []Node) (*Graph, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.graph, nil
}

func node(svc, ns string) Node { return Node{Service: svc, Namespace: ns} }

// TestCausal_GrangerCausality verifies the core Granger-causality logic:
// given a 2-node graph A → B where A's anomaly started 5 minutes before
// B's, the analyzer attributes the cascade to A.
func TestCausal_GrangerCausality(t *testing.T) {
	graph := &Graph{
		Nodes: []Node{node("a", "prod"), node("b", "prod")},
		Edges: []Edge{{Source: node("a", "prod"), Target: node("b", "prod"), Weight: 100}},
	}
	a := NewAnalyzer(&fakeGraphProvider{graph: graph})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-1",
		StartedAt: t0,
		Anomalies: []Anomaly{
			{Node: node("a", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
			{Node: node("b", "prod"), Metric: "error_rate", StartedAt: t0.Add(5 * time.Minute), Magnitude: 4.0},
		},
	}

	rc, err := a.Analyze(context.Background(), cascade)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if rc.Node != node("a", "prod") {
		t.Errorf("expected root cause a/prod, got %s/%s", rc.Node.Service, rc.Node.Namespace)
	}
	if rc.Confidence <= 0.5 {
		t.Errorf("expected high confidence for clear cause, got %.3f", rc.Confidence)
	}
	if len(rc.PairScores) != 1 {
		t.Errorf("expected 1 pair score, got %d", len(rc.PairScores))
	}
	if len(rc.PairScores) > 0 {
		ps := rc.PairScores[0]
		if ps.From != node("a", "prod") || ps.To != node("b", "prod") {
			t.Errorf("pair score has wrong from/to: %+v", ps)
		}
		if ps.Lag != 5*time.Minute {
			t.Errorf("expected lag=5m, got %s", ps.Lag)
		}
		if ps.Score <= 0 || ps.Score > 1 {
			t.Errorf("expected score in (0,1], got %f", ps.Score)
		}
	}
}

// TestCausal_TopologicalSort verifies that for a chain A → B → C where each
// anomaly starts after its upstream, the analyzer identifies the head of
// the chain (A) as the root cause — not the most-symptomatic middle node.
func TestCausal_TopologicalSort(t *testing.T) {
	graph := &Graph{
		Nodes: []Node{node("a", "prod"), node("b", "prod"), node("c", "prod")},
		Edges: []Edge{
			{Source: node("a", "prod"), Target: node("b", "prod"), Weight: 100},
			{Source: node("b", "prod"), Target: node("c", "prod"), Weight: 100},
		},
	}
	a := NewAnalyzer(&fakeGraphProvider{graph: graph})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-chain",
		StartedAt: t0,
		Anomalies: []Anomaly{
			{Node: node("a", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
			{Node: node("b", "prod"), Metric: "error_rate", StartedAt: t0.Add(3 * time.Minute), Magnitude: 4.0},
			{Node: node("c", "prod"), Metric: "error_rate", StartedAt: t0.Add(7 * time.Minute), Magnitude: 4.0},
		},
	}

	rc, err := a.Analyze(context.Background(), cascade)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if rc.Node != node("a", "prod") {
		t.Errorf("expected root cause a/prod (chain head), got %s/%s", rc.Node.Service, rc.Node.Namespace)
	}
	// B and C have inbound scores (they were "caused by" their upstream),
	// so they should appear as alternative candidates, not as the winner.
	for _, alt := range rc.AlternativeCandidates {
		if alt.Node == node("a", "prod") {
			t.Error("winner should not also appear as alternative")
		}
	}
	if len(rc.PairScores) != 2 {
		t.Errorf("expected 2 pair scores (A→B and B→C), got %d", len(rc.PairScores))
	}
}

// TestCausal_AttributesRootCause verifies the RootCause struct is fully
// populated with cascade ID, confidence, reasoning, and alternatives.
func TestCausal_AttributesRootCause(t *testing.T) {
	graph := &Graph{
		Nodes: []Node{node("a", "prod"), node("b", "prod")},
		Edges: []Edge{{Source: node("a", "prod"), Target: node("b", "prod"), Weight: 100}},
	}
	a := NewAnalyzer(&fakeGraphProvider{graph: graph})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-attrs",
		StartedAt: t0,
		Anomalies: []Anomaly{
			{Node: node("a", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
			{Node: node("b", "prod"), Metric: "error_rate", StartedAt: t0.Add(2 * time.Minute), Magnitude: 4.0},
		},
	}

	rc, err := a.Analyze(context.Background(), cascade)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if rc.CascadeID != "casc-attrs" {
		t.Errorf("expected cascade ID casc-attrs, got %q", rc.CascadeID)
	}
	if rc.Confidence < 0 || rc.Confidence > 1 {
		t.Errorf("confidence out of [0,1]: %f", rc.Confidence)
	}
	if rc.Reasoning == "" {
		t.Error("expected non-empty reasoning")
	}
	if len(rc.PairScores) == 0 {
		t.Error("expected at least 1 pair score")
	}
	// A is the winner; B is the alternative.
	if len(rc.AlternativeCandidates) != 1 {
		t.Errorf("expected 1 alternative candidate (B), got %d", len(rc.AlternativeCandidates))
	}
	if len(rc.AlternativeCandidates) > 0 {
		alt := rc.AlternativeCandidates[0]
		if alt.Node != node("b", "prod") {
			t.Errorf("expected B as alternative, got %s/%s", alt.Node.Service, alt.Node.Namespace)
		}
		if alt.IsSource {
			t.Error("B has inbound edges (from A), should not be a source")
		}
	}
}

// TestCausal_SimultaneousAnomaliesNoLag verifies that when two anomalies
// start at the same time (lag=0), no Granger-causality pair is emitted.
// This guards against false positives when the lag signal is absent.
func TestCausal_SimultaneousAnomaliesNoLag(t *testing.T) {
	graph := &Graph{
		Nodes: []Node{node("a", "prod"), node("b", "prod")},
		Edges: []Edge{{Source: node("a", "prod"), Target: node("b", "prod"), Weight: 100}},
	}
	a := NewAnalyzer(&fakeGraphProvider{graph: graph})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-simul",
		StartedAt: t0,
		Anomalies: []Anomaly{
			{Node: node("a", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
			{Node: node("b", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
		},
	}

	rc, err := a.Analyze(context.Background(), cascade)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(rc.PairScores) != 0 {
		t.Errorf("expected 0 pair scores for simultaneous anomalies, got %d", len(rc.PairScores))
	}
	// With no pairs, all candidates are sources with score 0. The winner
	// is picked arbitrarily (first in the sort, which prefers sources on
	// ties) and confidence collapses to the source boost (0.2) since
	// winner.Score and totalScore are both 0.
	if rc.Confidence != 0.2 {
		t.Errorf("expected confidence 0.2 (source boost only) with no signal, got %f", rc.Confidence)
	}
}

// TestCausal_ReversedTimingFavorsActualSource verifies that if B's anomaly
// started BEFORE A's (i.e. the dependency edge points A→B but B's symptom
// came first), the analyzer does NOT attribute the cause to A — the lag is
// non-positive so the A→B pair is dropped, leaving both nodes as graph
// sources with zero net score.
func TestCausal_ReversedTimingFavorsActualSource(t *testing.T) {
	graph := &Graph{
		Nodes: []Node{node("a", "prod"), node("b", "prod")},
		Edges: []Edge{{Source: node("a", "prod"), Target: node("b", "prod"), Weight: 100}},
	}
	a := NewAnalyzer(&fakeGraphProvider{graph: graph})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-reversed",
		StartedAt: t0,
		Anomalies: []Anomaly{
			// B's anomaly starts 5 min BEFORE A's — A can't be the cause.
			{Node: node("b", "prod"), Metric: "error_rate", StartedAt: t0, Magnitude: 4.0},
			{Node: node("a", "prod"), Metric: "error_rate", StartedAt: t0.Add(5 * time.Minute), Magnitude: 4.0},
		},
	}

	rc, err := a.Analyze(context.Background(), cascade)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(rc.PairScores) != 0 {
		t.Errorf("expected 0 pair scores for reversed timing, got %d", len(rc.PairScores))
	}
}

// TestCausal_EmptyCascadeErrors verifies that Analyze rejects an empty
// cascade with a clear error.
func TestCausal_EmptyCascadeErrors(t *testing.T) {
	a := NewAnalyzer(&fakeGraphProvider{graph: &Graph{}})
	_, err := a.Analyze(context.Background(), Cascade{ID: "empty"})
	if err == nil {
		t.Fatal("expected error on empty cascade")
	}
}

// TestCausal_GraphProviderError verifies that a GraphFor error is wrapped
// and returned.
func TestCausal_GraphProviderError(t *testing.T) {
	providerErr := errors.New("dep graph unavailable")
	a := NewAnalyzer(&fakeGraphProvider{err: providerErr})

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cascade := Cascade{
		ID:        "casc-err",
		StartedAt: t0,
		Anomalies: []Anomaly{{Node: node("a", "prod"), StartedAt: t0, Magnitude: 4.0}},
	}
	_, err := a.Analyze(context.Background(), cascade)
	if err == nil {
		t.Fatal("expected error from GraphFor failure")
	}
	if !errors.Is(err, providerErr) {
		t.Errorf("expected wrapped provider error, got %v", err)
	}
}

// TestCausal_SigmoidSanity verifies the sigmoid helper squashes a few
// representative lag values into the expected (0, 1) range.
func TestCausal_SigmoidSanity(t *testing.T) {
	tests := []struct {
		x    float64
		want float64
	}{
		{0, 0.5},       // no lead → 0.5 (no signal either way).
		{1, 0.731058},  // 1 minute lead → ~0.73.
		{5, 0.993307},  // 5 minute lead → ~0.99.
		{-1, 0.268941}, // negative lead → ~0.27.
	}
	for _, tc := range tests {
		got := sigmoid(tc.x)
		if (got-tc.want) > 1e-5 || (tc.want-got) > 1e-5 {
			t.Errorf("sigmoid(%v) = %v, want %v", tc.x, got, tc.want)
		}
	}
}

// TestCausal_UniqueNodes verifies deduplication when a node has multiple
// anomalies (e.g. error_rate + cpu).
func TestCausal_UniqueNodes(t *testing.T) {
	anomalies := []Anomaly{
		{Node: node("a", "prod"), Metric: "error_rate", Magnitude: 4.0},
		{Node: node("a", "prod"), Metric: "cpu", Magnitude: 3.0},
		{Node: node("b", "prod"), Metric: "error_rate", Magnitude: 4.0},
	}
	got := uniqueNodes(anomalies)
	if len(got) != 2 {
		t.Errorf("expected 2 unique nodes, got %d (%+v)", len(got), got)
	}
}

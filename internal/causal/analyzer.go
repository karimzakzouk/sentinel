package causal

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Node is a service in the dependency graph.
type Node struct {
	Service   string
	Namespace string
}

// Edge is a directed dependency: Source calls Target.
type Edge struct {
	Source Node
	Target Node
	// Weight is the call volume (req/s) — used to rank which downstream
	// dependencies matter most.
	Weight float64
}

// Graph is a directed acyclic graph of service dependencies.
type Graph struct {
	Nodes []Node
	Edges []Edge
}

// Anomaly is a metric anomaly observed on a single service.
type Anomaly struct {
	Node      Node
	Metric    string // "error_rate", "p99_latency", "cpu", "memory".
	StartedAt time.Time
	// Magnitude is how far the metric deviated from baseline, in sigma.
	// e.g. 4.2 = 4.2-sigma anomaly.
	Magnitude float64
}

// Cascade is a group of anomalies that fired within a short window.
// internal/correlation already produces these; this package consumes them.
type Cascade struct {
	ID        string
	StartedAt time.Time
	Anomalies []Anomaly
}

// RootCause is the verdict produced by Analyze.
type RootCause struct {
	CascadeID string
	Node      Node
	// Confidence is 0..1. Computed as the winner's net outbound Granger
	// score divided by the total score, boosted when the winner is a graph
	// source (no anomalous upstream dependencies).
	Confidence float64

	// Reasoning is the human-readable chain of evidence.
	Reasoning string

	// PairScores is the per-pair Granger scores used to reach the verdict.
	PairScores []PairScore

	// AlternativeCandidates are other nodes we considered but ranked lower,
	// with their scores. Operators should review these when confidence < 0.7.
	AlternativeCandidates []Candidate
}

// PairScore is the Granger-causality score for one (source, target) pair.
type PairScore struct {
	From  Node
	To    Node
	Lag   time.Duration // how much earlier From's anomaly started.
	Score float64       // 0..1.
}

// Candidate is a node under consideration as the root cause.
type Candidate struct {
	Node     Node
	Score    float64
	IsSource bool // true if node has no anomalous upstream dependencies.
}

// DepGraphProvider is the abstraction over internal/depanalysis.
type DepGraphProvider interface {
	// GraphFor returns the dependency graph for the namespace(s) involved
	// in the cascade. The graph should include all upstream + downstream
	// services of every anomalous node.
	GraphFor(ctx context.Context, nodes []Node) (*Graph, error)
}

// Analyzer computes causal root-cause attribution for a cascade.
type Analyzer struct {
	graph DepGraphProvider
}

// NewAnalyzer creates an Analyzer.
func NewAnalyzer(graph DepGraphProvider) *Analyzer {
	return &Analyzer{graph: graph}
}

// Analyze takes a cascade and returns a root-cause verdict. The verdict is
// computed by: (1) fetching the dependency subgraph for the anomalous
// nodes, (2) computing pairwise Granger-causality scores for every
// anomalous edge where the source's anomaly started before the target's,
// (3) ranking nodes by net outbound score (outbound minus inbound), and
// (4) boosting confidence when the winner is a graph source.
func (a *Analyzer) Analyze(ctx context.Context, c Cascade) (*RootCause, error) {
	if len(c.Anomalies) == 0 {
		return nil, fmt.Errorf("causal: empty cascade")
	}

	// Collect the anomalous nodes.
	nodes := uniqueNodes(c.Anomalies)

	// Get the dependency graph for these nodes + their neighbors.
	graph, err := a.graph.GraphFor(ctx, nodes)
	if err != nil {
		return nil, fmt.Errorf("causal: get dep graph: %w", err)
	}

	// Build an anomaly-start lookup keyed by node. When the same node has
	// multiple anomalies (e.g. error_rate + cpu), keep the earliest start
	// and the magnitude that goes with it.
	startByNode := map[Node]time.Time{}
	magnitudeByNode := map[Node]float64{}
	for _, an := range c.Anomalies {
		if existing, ok := startByNode[an.Node]; !ok || an.StartedAt.Before(existing) {
			startByNode[an.Node] = an.StartedAt
			magnitudeByNode[an.Node] = an.Magnitude
		}
	}

	// Compute Granger-style pairwise scores.
	// For every (X, Y) where X->Y in the graph AND both are anomalous:
	//   lag = Y.StartedAt - X.StartedAt
	//   score = sigmoid(lag/60s) * (X.Magnitude / Y.Magnitude)
	// If lag <= 0 (X started AFTER Y), score is 0 — X can't cause Y if X
	// happened later.
	var pairs []PairScore
	for _, edge := range graph.Edges {
		xStart, xOk := startByNode[edge.Source]
		yStart, yOk := startByNode[edge.Target]
		if !xOk || !yOk {
			continue
		}
		lag := yStart.Sub(xStart)
		if lag <= 0 {
			continue
		}
		xMag := magnitudeByNode[edge.Source]
		yMag := magnitudeByNode[edge.Target]
		if yMag == 0 {
			continue
		}
		score := sigmoid(lag.Seconds()/60.0) * (xMag / yMag)
		if score > 1 {
			score = 1
		}
		pairs = append(pairs, PairScore{
			From:  edge.Source,
			To:    edge.Target,
			Lag:   lag,
			Score: score,
		})
	}

	// Aggregate: for each anomalous node, sum the outbound pair scores
	// (how much it "causes" downstream nodes) and subtract inbound scores
	// (how much it's "caused by" upstream).
	outbound := map[Node]float64{}
	inbound := map[Node]float64{}
	for _, p := range pairs {
		outbound[p.From] += p.Score
		inbound[p.To] += p.Score
	}

	var candidates []Candidate
	for _, n := range nodes {
		isSource := inbound[n] == 0
		netScore := outbound[n] - inbound[n]
		if netScore < 0 {
			netScore = 0
		}
		candidates = append(candidates, Candidate{
			Node:     n,
			Score:    netScore,
			IsSource: isSource,
		})
	}

	// Sort by score descending, prefer source nodes on ties.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].IsSource && !candidates[j].IsSource
	})

	if len(candidates) == 0 {
		return nil, fmt.Errorf("causal: no candidates produced (graph may be empty)")
	}

	winner := candidates[0]
	alternatives := candidates[1:]

	// Confidence: normalize winner's score against total.
	totalScore := 0.0
	for _, c := range candidates {
		totalScore += c.Score
	}
	confidence := 0.0
	if totalScore > 0 {
		confidence = winner.Score / totalScore
	}
	// Boost if winner is a graph source (no anomalous upstream).
	if winner.IsSource {
		confidence = confidence + (1-confidence)*0.2
	}
	if confidence > 1 {
		confidence = 1
	}

	reasoning := fmt.Sprintf(
		"Node %s/%s has the highest net outbound Granger-causality score (%.3f). "+
			"Its anomaly started at %s, preceding downstream anomalies by %s. "+
			"It is a graph source (no anomalous upstream dependencies): %v. "+
			"Confidence: %.2f.",
		winner.Node.Service, winner.Node.Namespace,
		winner.Score,
		startByNode[winner.Node].Format(time.RFC3339),
		aggregateLag(pairs, winner.Node),
		winner.IsSource,
		confidence,
	)

	logging.Info("causal: root-cause attributed",
		"cascade_id", c.ID,
		"winner", winner.Node.Service+"/"+winner.Node.Namespace,
		"confidence", confidence,
		"alternatives", len(alternatives),
	)

	return &RootCause{
		CascadeID:             c.ID,
		Node:                  winner.Node,
		Confidence:            confidence,
		Reasoning:             reasoning,
		PairScores:            pairs,
		AlternativeCandidates: alternatives,
	}, nil
}

// aggregateLag returns a human-readable summary of how much earlier the
// winner's anomaly started vs. its downstream nodes.
func aggregateLag(pairs []PairScore, winner Node) string {
	var lags []time.Duration
	for _, p := range pairs {
		if p.From == winner && p.Score > 0 {
			lags = append(lags, p.Lag)
		}
	}
	if len(lags) == 0 {
		return "n/a"
	}
	min := lags[0]
	max := lags[0]
	var sum time.Duration
	for _, l := range lags {
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
		sum += l
	}
	avg := sum / time.Duration(len(lags))
	return fmt.Sprintf("min=%s avg=%s max=%s", min.Round(time.Second), avg.Round(time.Second), max.Round(time.Second))
}

// uniqueNodes returns the deduplicated list of nodes from a slice of Anomalies.
func uniqueNodes(anomalies []Anomaly) []Node {
	seen := map[Node]bool{}
	var out []Node
	for _, a := range anomalies {
		if !seen[a.Node] {
			seen[a.Node] = true
			out = append(out, a.Node)
		}
	}
	return out
}

package brain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/slot"
)

// LocalizeInput is the evidence for ranking an incident's possible root
// causes: the workload where it was detected and the workloads it depends on.
type LocalizeInput struct {
	Workload   model.ObjectRef     `json:"workload"`
	Candidates []LocalizeCandidate `json:"candidates"`
}

// LocalizeCandidate is one possible root cause and what is known about it.
type LocalizeCandidate struct {
	Workload model.ObjectRef `json:"workload"`
	// Hops is how many dependency edges separate it from the incident's
	// workload: 0 for the workload itself.
	Hops int `json:"hops"`
	// Anomalous lists the fact kinds significant on it now.
	Anomalous []string `json:"anomalous,omitempty"`
	// ChangedBefore is how long before the incident's onset it last changed,
	// in seconds; negative when it did not change within the lookback.
	ChangedBefore float64 `json:"changedBeforeSeconds"`
}

// Suspect is a ranked root-cause candidate with its readable evidence.
type Suspect struct {
	Workload model.ObjectRef         `json:"workload"`
	Ratios   []model.LikelihoodRatio `json:"ratios"`
	LogOdds  float64                 `json:"logOdds"`
	P        float64                 `json:"p"`
}

// EvidenceRanking is the default localization engine. Candidates start
// equally likely; each one's odds multiply readable likelihood ratios: an
// anomaly of its own, a recent change, and, for a dependency, being upstream
// of the symptoms. A dependency that looks healthy is an unlikely cause.
// Metrics-based deviation scores join as another ratio once metrics exist.
type EvidenceRanking struct {
	// Lookback and Typical are as in attribution: how far back changes count,
	// and the usual delay between a bad change and its symptoms.
	Lookback, Typical time.Duration
}

func (EvidenceRanking) ID() string { return "evidence-ranking-v1" }

func (e EvidenceRanking) Decide(_ context.Context, in LocalizeInput) (slot.Ranking[Suspect], error) {
	out := slot.Ranking[Suspect]{Coverage: Coverage}
	if len(in.Candidates) == 0 {
		return out, nil
	}
	suspects := make([]Suspect, 0, len(in.Candidates))
	for _, c := range in.Candidates {
		var ratios []model.LikelihoodRatio
		switch {
		case len(c.Anomalous) > 0:
			ratios = append(ratios, model.LikelihoodRatio{Factor: "its own anomaly: " + strings.Join(c.Anomalous, ", "), Ratio: 20})
			if c.Hops > 0 {
				ratios = append(ratios, model.LikelihoodRatio{Factor: fmt.Sprintf("upstream of the symptoms, %d hop(s) away", c.Hops), Ratio: 2})
			}
		case c.Hops > 0:
			ratios = append(ratios, model.LikelihoodRatio{Factor: "no anomaly of its own", Ratio: 0.2})
		}
		if c.ChangedBefore >= 0 && c.ChangedBefore <= e.Lookback.Seconds() {
			timing := e.Lookback.Seconds() / e.Typical.Seconds() * math.Exp(-c.ChangedBefore/e.Typical.Seconds())
			ratios = append(ratios, model.LikelihoodRatio{
				Factor: fmt.Sprintf("changed %s before the symptoms", (time.Duration(c.ChangedBefore) * time.Second).Round(time.Second)),
				Ratio:  math.Max(timing, 0.1)})
		}
		logOdds := 0.0
		for _, r := range ratios {
			logOdds += math.Log(r.Ratio)
		}
		suspects = append(suspects, Suspect{Workload: c.Workload, Ratios: ratios, LogOdds: logOdds})
	}
	total := 0.0
	for _, s := range suspects {
		total += math.Exp(s.LogOdds)
	}
	for i := range suspects {
		suspects[i].P = math.Exp(suspects[i].LogOdds) / total
	}
	sort.SliceStable(suspects, func(i, j int) bool { return suspects[i].LogOdds > suspects[j].LogOdds })
	covered := 0.0
	for _, s := range suspects {
		out.Items = append(out.Items, slot.Ranked[Suspect]{Item: s, Score: s.LogOdds, InSet: covered < Coverage})
		covered += s.P
	}
	return out, nil
}

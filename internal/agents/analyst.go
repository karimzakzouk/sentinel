package agents

import (
        "context"
        "strings"
        "time"

        "github.com/karimzakzouk/sentinel/internal/llm"
        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// categoryKeywordMap maps hypothesis keywords to categories for Beta-prior lookup.

var categoryKeywordMap = []struct {
        keyword, category string
}{
        {"oom", "oom"},
        {"out of memory", "oom"},
        {"oomkilled", "oom"},
        {"connection pool", "conn_pool_exhausted"},
        {"timeout", "timeout"},
        {"deadline exceeded", "timeout"},
        {"bad deploy", "bad_deploy"},
        {"regression", "bad_deploy"},
        {"config drift", "config_drift"},
        {"config change", "config_drift"},
        {"upstream", "upstream_down"},
        {"dependency", "upstream_down"},
        {"migration", "schema_migration"},
        {"schema", "schema_migration"},
        {"dns", "dns"},
        {"certificate", "cert"},
        {"auth", "auth"},
        {"ratelimit", "ratelimit"},
        {"rate limit", "ratelimit"},
}

// Analyst kills falsified hypotheses, computes confidence via Bayes, picks survivor.
// Does NOT use the LLM — pure math (Bayes + Beta priors).

type Analyst struct {
        router *llm.Router // kept for future LLM-assisted analysis; not used in v3.0
        // LLM prior weight in the Bayesian blend.4.
        // Set via SetLLMPriorWeight from main.go using cfg.LLMPriorWeight.

        llmPriorWeight float64
}

// NewAnalyst creates an Analyst.

func NewAnalyst(router *llm.Router) *Analyst {
        return &Analyst{
                router:         router,
                llmPriorWeight: 0.4, // v3.11.0: default; override via SetLLMPriorWeight
        }
}

// SetLLMPriorWeight sets the weight given to the LLM's confidence vs the
// Bayesian posterior. v3.11.0: Replaces the hardcoded 0.4.
// Range [0, 1]: 0 = pure Bayesian (ignore LLM), 1 = pure LLM (ignore Bayes).
// Default 0.4 (slight Bayesian lean — Bayes is more robust with few samples).

func (a *Analyst) SetLLMPriorWeight(w float64) {
        if w > 0 && w < 1 {
                a.llmPriorWeight = w
        }
}

// BetaStore is the interface for Beta prior lookups + updates.
// Satisfied by feedback.Store.

type BetaStore interface {
        GetBetaPrior(ctx context.Context, service, fixType string) (alpha, beta float64, successes, failures int, err error)
}

// Run executes the analyst agent. Requires a BetaStore to look up priors.

func (a *Analyst) Run(ctx context.Context, state *schemas.AgentState, betaStore BetaStore) error {
        if len(state.Hypotheses) == 0 {
                state.Status = schemas.StatusFailed
                s := "analyst_no_hypotheses"
                state.Error = &s
                return nil
        }

        // 1. Falsify: any hypothesis with strong contradicting evidence is dead.

        for i := range state.Hypotheses {
                h := &state.Hypotheses[i]
                contradicting := []schemas.Evidence{}
                supporting := []schemas.Evidence{}
                for _, e := range state.Evidence {
                        if e.ContradictsHypothesisID != nil && *e.ContradictsHypothesisID == h.HID {
                                contradicting = append(contradicting, e)
                        }
                        if e.SupportsHypothesisID != nil && *e.SupportsHypothesisID == h.HID {
                                supporting = append(supporting, e)
                        }
                }
                strongContradictions := []string{}
                for _, e := range contradicting {
                        if isStrongResult(e.Result) {
                                strongContradictions = append(strongContradictions, e.EID)
                        }
                }
                if len(strongContradictions) > 0 {
                        h.Falsified = true
                        h.ContradictingEvidence = strongContradictions
                        if h.Confidence > 0.05 {
                                h.Confidence = 0.05
                        }
                }
                strongSupports := []string{}
                for _, e := range supporting {
                        if isStrongResult(e.Result) {
                                strongSupports = append(strongSupports, e.EID)
                        }
                }
                if len(strongSupports) > 0 {
                        h.SupportingEvidence = strongSupports
                        h.Confidence += 0.1 * float64(len(strongSupports))
                        if h.Confidence > 1.0 {
                                h.Confidence = 1.0
                        }
                }
        }

        // 2. Pick survivor: highest non-falsified confidence

        var survivors []schemas.Hypothesis
        for _, h := range state.Hypotheses {
                if !h.Falsified {
                        survivors = append(survivors, h)
                }
        }
        if len(survivors) == 0 {
                // All hypotheses falsified — rewind

                state.RewindEvents = append(state.RewindEvents, schemas.RewindEvent{
                        RaisedBy:   "analyst",
                        Reason:     "all_hypotheses_falsified",
                        NewContext: map[string]any{"falsified_hypotheses": state.Hypotheses},
                        Ts:         time.Now().UTC(),
                })
                state.Hypotheses = nil
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":  "analyst",
                        "action": "rewind_all_falsified",
                })
                return nil
        }

        // Sort survivors by confidence (highest first)

        for i := 0; i < len(survivors); i++ {
                for j := i + 1; j < len(survivors); j++ {
                        if survivors[j].Confidence > survivors[i].Confidence {
                                survivors[i], survivors[j] = survivors[j], survivors[i]
                        }
                }
        }
        survivor := survivors[0]

        // 3. Compute posterior via Beta prior + evidence likelihood

        category := classifyCategory(survivor.Statement)
        service := state.Cluster.Service
        var alpha, beta, priorMean float64
        if betaStore != nil {
                a, b, _, _, _ := betaStore.GetBetaPrior(ctx, service, category)
                alpha = a
                beta = b
                priorMean = alpha / (alpha + beta)
        } else {
                alpha = 1.0
                beta = 1.0
                priorMean = 0.5
        }

        // Count strong evidence for/against survivor

        nSupport := 0
        nContra := 0
        for _, e := range state.Evidence {
                if e.SupportsHypothesisID != nil && *e.SupportsHypothesisID == survivor.HID && isStrongResult(e.Result) {
                        nSupport++
                }
                if e.ContradictsHypothesisID != nil && *e.ContradictsHypothesisID == survivor.HID && isStrongResult(e.Result) {
                        nContra++
                }
        }
        postAlpha := alpha + float64(nSupport)
        postBeta := beta + float64(nContra)
        posteriorMean := postAlpha / (postAlpha + postBeta)

        // Combine LLM prior with Bayesian posterior (weighted average)
        // Use a.llmPriorWeight instead of hardcoded 0.4.

        w := a.llmPriorWeight
        if w <= 0 || w >= 1 {
                w = 0.4 // safety net — should never fire
        }
        finalConfidence := w*survivor.Confidence + (1-w)*posteriorMean

        // 4. Detect schema migration

        hasSchemaMigration := detectSchemaMigration(survivor.Statement, state)

        // 5. Build the RootCause

        rootCause := &schemas.RootCause{
                Statement:          survivor.Statement,
                Confidence:         finalConfidence,
                SourceFiles:        survivor.SourceFiles,
                Category:           category,
                HasSchemaMigration: hasSchemaMigration,
        }
        for _, e := range state.Evidence {
                if e.SupportsHypothesisID != nil && *e.SupportsHypothesisID == survivor.HID {
                        rootCause.EvidenceIDs = append(rootCause.EvidenceIDs, e.EID)
                }
        }

        state.RootCause = rootCause
        state.Status = schemas.StatusRootCaused
        state.AgentTrace = append(state.AgentTrace, map[string]any{
                "agent":               "analyst",
                "survivor":            survivor.HID,
                "falsified_count":     len(state.Hypotheses) - 1,
                "prior_mean":          priorMean,
                "posterior_mean":      posteriorMean,
                "final_confidence":    finalConfidence,
                "has_schema_migration": hasSchemaMigration,
        })

        return nil
}

// isStrongResult returns true if a query returned actual data (not just an error).

func isStrongResult(result map[string]any) bool {
        if _, hasError := result["error"]; hasError {
                return false
        }
        if items, ok := result["items"].([]any); ok {
                return len(items) > 0
        }
        if r, ok := result["result"].([]any); ok {
                return len(r) > 0
        }
        return len(result) > 0
}

func classifyCategory(statement string) string {
        lower := strings.ToLower(statement)
        for _, km := range categoryKeywordMap {
                if strings.Contains(lower, km.keyword) {
                        return km.category
                }
        }
        return "other"
}

func detectSchemaMigration(statement string, state *schemas.AgentState) bool {
        lower := strings.ToLower(statement)
        for _, kw := range []string{"migration", "alter table", "schema"} {
                if strings.Contains(lower, kw) {
                        return true
                }
        }
        clusterText := strings.ToLower(state.Cluster.Summary)
        for _, anomaly := range state.Cluster.Anomalies {
                clusterText += " " + strings.ToLower(anomaly.Explanation)
        }
        if strings.Contains(clusterText, "has_schema_migration") || strings.Contains(clusterText, "schema migration") {
                return true
        }
        return false
}

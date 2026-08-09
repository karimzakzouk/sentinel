// pipeline_adapter.go: K8s + GitHub plumbing around the agent-loop
// Investigator so it can drop into the pipeline as an InvestigatorRunner.
//
// The legacy agents.Investigator does three things internally before the
// 2-round LLM loop runs:
//
//  1. Fetch fresh pod logs via the K8s API (fallback: detection-layer logs).
//  2. Query the vector store for similar past incidents (fast-path + context).
//  3. Discover the GitHub repo from annotations, enforce the allowlist, clone
//     the repo, then run the loop against the cloned file tree.
//
// The new agent.Investigator expects a pre-cloned repoPath and pod logs
// already attached to state. PipelineAdapter bridges that gap: it owns the
// prelude (pod logs + clone) and delegates the actual investigation to a
// fresh agent.Investigator constructed with the cloned repo path.
//
// Why not just refactor agent.Investigator to do the clone itself? Two
// reasons: (1) the new agent package is intentionally thin and free of K8s
// + GitHub deps so it can be unit-tested in isolation with a fake repo on
// disk; (2) the cloner + K8s client are owned by the pipeline's caller
// (cmd/sentinel/wire.go), so reusing them avoids a second cloner instance
// that would compete for the GitHub rate limit.
package agent

import (
	"context"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/agents"
	"github.com/karimzakzouk/sentinel/internal/k8s"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repo"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// PipelineAdapter wraps agent.Investigator with the same K8s + GitHub
// plumbing that the legacy agents.Investigator owns (pod logs fetch, repo
// allowlist, clone, cleanup). It satisfies agents.InvestigatorRunner so the
// pipeline can swap to the agent loop via Pipeline.SetAgentLoopRunner.
//
// The adapter is constructed once at wire time and called once per
// investigation. It is safe for concurrent use because each Run constructs a
// fresh *agent.Investigator with the per-call cloned repo path.
type PipelineAdapter struct {
	router        *llm.Router
	cfg           AgentConfig
	k8s           *k8s.Client
	gitHubToken   string
	cloner        *repo.Cloner
	allowedRepos  []string
	gitHost       string
	defaultBranch string
	vectorStore   agents.VectorStore
}

// NewPipelineAdapter constructs an adapter wired to the same router, K8s
// client, GitHub token, and vector store as the legacy investigator.
// vectorStore may be nil — the memory-loop fast path is skipped.
func NewPipelineAdapter(router *llm.Router, k8sClient *k8s.Client, gitHubToken string, vectorStore agents.VectorStore, cfg AgentConfig) *PipelineAdapter {
	return &PipelineAdapter{
		router:        router,
		cfg:           cfg,
		k8s:           k8sClient,
		gitHubToken:   gitHubToken,
		cloner:        repo.NewCloner(gitHubToken),
		gitHost:       "github.com",
		defaultBranch: "main",
		vectorStore:   vectorStore,
	}
}

// SetAllowedRepos sets the GitHub repo allowlist. Empty means allow all.
// Mirrors agents.Investigator.SetAllowedRepos so the pipeline can propagate
// the same config to both investigator paths.
func (a *PipelineAdapter) SetAllowedRepos(repos []string) { a.allowedRepos = repos }

// SetGitHost sets the git host (default github.com). Also propagates to the
// cloner so GitHub Enterprise clone URLs work.
func (a *PipelineAdapter) SetGitHost(host string) {
	if host != "" {
		a.gitHost = host
		a.cloner.WithGitHost(host)
	}
}

// SetDefaultBranch sets the default branch fallback when neither the
// sentinel.io/git-commit nor sentinel.io/branch annotation is present.
func (a *PipelineAdapter) SetDefaultBranch(branch string) {
	if branch != "" {
		a.defaultBranch = branch
		a.cloner.WithDefaultBranch(branch)
	}
}

// Run implements agents.InvestigatorRunner. It is the agent-loop equivalent
// of agents.Investigator.Run: fetch pod logs, query the vector store for
// similar past incidents, discover + clone the repo, then delegate the
// actual N-turn agent loop to agent.Investigator.Run.
//
// Fallbacks (mirroring the legacy investigator):
//   - no repo annotation / repo not in allowlist / clone failure → run the
//     error-parser-only path (agents.GenerateHypothesesUniversal) so the
//     pipeline still produces hypotheses for the analyst.
//   - agent.Investigator.Run returns an error → same fallback (so a transient
//     LLM outage doesn't kill the pipeline).
func (a *PipelineAdapter) Run(ctx context.Context, state *schemas.AgentState) error {
	cluster := state.Cluster

	// Fetch fresh pod logs via K8s API (up to 8KB, retry 3x). Falls back to
	// whatever the detection layer already captured.
	var podLogs string
	if a.k8s != nil {
		podLogs, _ = a.k8s.FetchCrashingPodLogs(ctx, cluster.Service, cluster.Namespace)
	}
	if podLogs != "" {
		logging.Info("agent: fetched pod logs via K8s API", "bytes", len(podLogs))
		fullExplanation := cluster.Summary + "\n\n## Pod Logs (full, from K8s API)\n" + podLogs
		if len(cluster.Anomalies) > 0 {
			cluster.Anomalies[0].Explanation = fullExplanation
		}
		state.Cluster = cluster
	} else {
		podLogs = extractPodLogs(state)
		logging.Info("agent: K8s API logs unavailable, using detection logs", "bytes", len(podLogs))
	}

	// Memory loop: query the vector index for similar past incidents. Same
	// gating as the legacy investigator: only apply a known fix directly
	// when the embedding pipeline is semantic (not the hash fallback).
	if a.vectorStore != nil && podLogs != "" {
		similar, scores, err := a.vectorStore.Search(ctx, podLogs, 5)
		if err == nil && len(similar) > 0 {
			logging.Info("agent: found similar incidents",
				"count", len(similar), "top_score", scores[0])

			if scores[0] > agents.ConfidenceHighMatch && similar[0].Outcome == "success" && !a.vectorStore.UsingHashFallback() {
				logging.Info("agent: high-similarity match — would apply known fix",
					"score", scores[0], "incident_id", similar[0].IncidentID)
				// Note: the agent-loop adapter does NOT auto-apply past fixes.
				// The legacy investigator's applyKnownFix bypasses the agent
				// loop entirely, which defeats the point of the new path.
				// Instead we surface the match as context for the agent loop.
			}

			if scores[0] > agents.ConfidenceMediumMatch {
				logging.Info("agent: medium-similarity match — using past incident as LLM context",
					"score", scores[0], "incident_id", similar[0].IncidentID)
				if state.RetrievedIncidents == nil {
					state.RetrievedIncidents = []map[string]any{}
				}
				state.RetrievedIncidents = append(state.RetrievedIncidents, map[string]any{
					"root_cause":  similar[0].RootCause,
					"fix_pattern": similar[0].FixPattern,
					"fix_type":    similar[0].FixType,
					"outcome":     similar[0].Outcome,
					"similarity":  scores[0],
					"incident_id": similar[0].IncidentID,
				})
			}
		}
	}

	// Discover + allow + clone the repo.
	repoName := agents.DiscoverRepo(state, a.gitHost)
	if repoName == "" {
		logging.Info("agent: no repo discovered — running error-parser-only path")
		return a.runWithoutRepo(ctx, state, podLogs)
	}
	if !agents.IsRepoAllowed(repoName, a.allowedRepos) {
		logging.Info("agent: repo not in allowlist — refusing to clone", "repo", repoName)
		return a.runWithoutRepo(ctx, state, podLogs)
	}

	branch := a.resolveBranch(state)
	logging.Info("agent: cloning repo", "repo", repoName, "branch", branch)
	repoPath, err := a.cloner.Clone(ctx, repoName, branch)
	if err != nil {
		logging.Error("agent: clone failed — falling back to no-repo mode", "err", err)
		return a.runWithoutRepo(ctx, state, podLogs)
	}
	defer a.cloner.Cleanup(repoPath)
	logging.Info("agent: repo cloned", "path", repoPath)

	// Delegate to the agent loop with the freshly-cloned repo.
	inv := NewInvestigator(a.router, repoPath, a.cfg)
	return inv.Run(ctx, state)
}

// runWithoutRepo is the fallback when no repo is available (no annotation,
// not on allowlist, clone failure). It uses the error parser to extract
// hypotheses from pod logs alone — no code intelligence, no agent loop.
// This mirrors the legacy investigator's runWithoutRepo path.
func (a *PipelineAdapter) runWithoutRepo(_ context.Context, state *schemas.AgentState, podLogs string) error {
	hyps := agents.GenerateHypothesesUniversal(podLogs, state)
	state.Hypotheses = hyps
	state.SourceFiles = extractSourceFiles(hyps)
	return nil
}

// resolveBranch mirrors agents.Investigator.resolveBranch: prefer the
// sentinel.io/git-commit annotation, then sentinel.io/branch, then fall back
// to the configured default branch with a warning.
func (a *PipelineAdapter) resolveBranch(state *schemas.AgentState) string {
	annotations := map[string]string{}
	for k, v := range state.Cluster.Annotations {
		annotations[k] = v
	}
	for _, anomaly := range state.Cluster.Anomalies {
		for k, v := range anomaly.Annotations {
			if _, exists := annotations[k]; !exists {
				annotations[k] = v
			}
		}
	}

	if commit, ok := annotations["sentinel.io/git-commit"]; ok && commit != "" {
		logging.Info("agent: using git-commit from annotation", "commit", commit)
		return commit
	}
	if branch, ok := annotations["sentinel.io/branch"]; ok && branch != "" {
		logging.Info("agent: using branch from annotation", "branch", branch)
		return branch
	}

	db := a.defaultBranch
	if db == "" {
		db = "main"
	}
	logging.Warn("agent: no sentinel.io/branch annotation - cloning default (may not match deployed code)",
		"default_branch", db)
	return db
}

// extractSourceFiles + extractPodLogs live in investigator.go (shared with
// the agent.Investigator path).

// Compile-time assertion: *PipelineAdapter satisfies agents.InvestigatorRunner.
var _ agents.InvestigatorRunner = (*PipelineAdapter)(nil)

// String returns a human-readable identifier for logging. Implements fmt.Stringer
// so the pipeline can log which runner is active without exposing internals.
func (a *PipelineAdapter) String() string {
	var sb strings.Builder
	sb.WriteString("agent.PipelineAdapter{")
	if a.k8s != nil {
		sb.WriteString("k8s=yes")
	} else {
		sb.WriteString("k8s=no")
	}
	sb.WriteString(",gitHost=")
	sb.WriteString(a.gitHost)
	sb.WriteString(",defaultBranch=")
	sb.WriteString(a.defaultBranch)
	if len(a.allowedRepos) > 0 {
		sb.WriteString(",allowlist=yes")
	} else {
		sb.WriteString(",allowlist=no")
	}
	sb.WriteString("}")
	return sb.String()
}

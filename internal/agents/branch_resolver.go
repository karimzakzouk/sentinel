package agents

import (
	"context"
	"log"
	"strings"

	"github.com/karimzakzouk/sentinel/v3/internal/repo"
	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// resolveBranch determines which branch or commit to clone for this incident.
//
// Priority:
//   1. sentinel.io/git-commit annotation → clone that exact commit (most precise)
//   2. sentinel.io/branch annotation     → clone that branch
//   3. (none)                            → clone main + log warning
//
// The annotation is the source of truth. We do NOT guess the branch from the
// image tag — that's a fragile heuristic that fails silently. If the user
// wants accurate diagnosis, they set the annotation. If they don't, we do
// our best with main and are honest about it.
func (a *Investigator) resolveBranch(ctx context.Context, state *schemas.AgentState, repoName string) string {
	annotations := state.Cluster.Annotations
	// Also check anomaly-level annotations (CRD watcher merges them into cluster.Annotations,
	// but just in case)
	for _, anomaly := range state.Cluster.Anomalies {
		for k, v := range anomaly.Annotations {
			if _, exists := annotations[k]; !exists {
				annotations[k] = v
			}
		}
	}

	// 1. git-commit (highest priority — exact commit)
	if commit, ok := annotations["sentinel.io/git-commit"]; ok && commit != "" {
		log.Printf("[investigator] using git-commit from annotation: %s", commit)
		return commit
	}

	// 2. branch
	if branch, ok := annotations["sentinel.io/branch"]; ok && branch != "" {
		log.Printf("[investigator] using branch from annotation: %s", branch)
		return branch
	}

	// 3. Fallback: main with warning
	log.Printf("[investigator] WARNING: no sentinel.io/branch annotation — cloning main (may not match deployed code)")
	return "main"
}

// discoverRepo extracts the GitHub repo from the cluster's annotations.
func discoverRepo(state *schemas.AgentState) string {
	if repo, ok := state.Cluster.Annotations["sentinel.io/repo"]; ok {
		return normalizeRepo(repo)
	}
	for _, anomaly := range state.Cluster.Anomalies {
		if repo, ok := anomaly.Annotations["sentinel.io/repo"]; ok {
			return normalizeRepo(repo)
		}
	}
	return ""
}

// normalizeRepo strips "github.com/" prefix if present.
func normalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	return strings.TrimSuffix(repo, ".git")
}

// _ keeps the import alive if unused in future refactors
var _ = repo.NewCloner
var _ schemas.AnomalyCluster

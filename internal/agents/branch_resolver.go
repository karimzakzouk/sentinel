package agents

import (
	"context"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// resolveBranch determines which branch or commit to clone for this incident.
// Priority:
//  1. sentinel.io/git-commit annotation — clone that exact commit (most precise)
//  2. sentinel.io/branch annotation — clone that branch
//  3. configured default branch (with a warning)
//
// The annotation is the source of truth. We do NOT guess the branch from the
// image tag — that heuristic fails silently. Operators who want accurate
// diagnosis set the annotation.
func (a *Investigator) resolveBranch(_ context.Context, state *schemas.AgentState, _ string) string {
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
		logging.Info("using git-commit from annotation", "commit", commit)
		return commit
	}
	if branch, ok := annotations["sentinel.io/branch"]; ok && branch != "" {
		logging.Info("using branch from annotation", "branch", branch)
		return branch
	}

	db := a.defaultBranch
	if db == "" {
		db = "main"
	}
	logging.Warn("no sentinel.io/branch annotation - cloning default (may not match deployed code)",
		"default_branch", db)
	return db
}

// discoverRepo extracts the GitHub repo from the cluster's annotations,
// normalized via normalizeRepo. Returns "" if no annotation is present.
func discoverRepo(state *schemas.AgentState, gitHost string) string {
	if repo, ok := state.Cluster.Annotations["sentinel.io/repo"]; ok {
		return normalizeRepo(repo, gitHost)
	}
	for _, anomaly := range state.Cluster.Anomalies {
		if repo, ok := anomaly.Annotations["sentinel.io/repo"]; ok {
			return normalizeRepo(repo, gitHost)
		}
	}
	return ""
}

// normalizeRepo strips the configured git host prefix (https://<host>/ and
// <host>/) and a trailing ".git". Also strips the legacy github.com prefix
// in case the annotation was set before the operator configured a custom
// gitHost.
func normalizeRepo(repo string, gitHost string) string {
	repo = strings.TrimSpace(repo)
	if gitHost == "" {
		gitHost = "github.com"
	}
	repo = strings.TrimPrefix(repo, "https://"+gitHost+"/")
	repo = strings.TrimPrefix(repo, gitHost+"/")
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	return strings.TrimSuffix(repo, ".git")
}

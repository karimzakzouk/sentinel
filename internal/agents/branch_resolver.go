package agents

import (
        "context"
        "log"
        "strings"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// resolveBranch determines which branch or commit to clone for this incident.
// Priority:
//   1. sentinel.io/git-commit annotation → clone that exact commit (most precise)
//   2. sentinel.io/branch annotation     → clone that branch
//   3. (none)                            → clone main + log warning
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

        // 3. Fallback: configured default branch with warning.
        // Now uses the
        // Investigator's configured defaultBranch (set from cfg.DefaultBranch).

        db := a.defaultBranch
        if db == "" {
                db = "main" // safety net — should never fire
        }
        log.Printf("[investigator] WARNING: no sentinel.io/branch annotation — cloning %s (may not match deployed code)", db)
        return db
}

// discoverRepo extracts the GitHub repo from the cluster's annotations.
// Now accepts a gitHost parameter so normalizeRepo can strip the
// configured host prefix (com")..

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
// <host>/) and a trailing ".git". v3.11.0:com" —
// a GitHub Enterprise repo string like "gh.mycorp.internal/acme/api" would
// not be normalized, producing a malformed clone URL.

func normalizeRepo(repo string, gitHost string) string {
        repo = strings.TrimSpace(repo)
        if gitHost == "" {
                gitHost = "github.com"
        }
        repo = strings.TrimPrefix(repo, "https://"+gitHost+"/")
        repo = strings.TrimPrefix(repo, gitHost+"/")
        // Also strip the legacy github.com prefix in case the annotation was
        // set before the operator configured a custom gitHost. This is a
        // backwards-compat safety net — new annotations should use the
        // configured host.

        repo = strings.TrimPrefix(repo, "https://github.com/")
        repo = strings.TrimPrefix(repo, "github.com/")
        return strings.TrimSuffix(repo, ".git")
}

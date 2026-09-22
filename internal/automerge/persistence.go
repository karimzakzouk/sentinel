package automerge

import (
	"context"
	"time"
)

// TrackedPRRecord is the persistence representation of a TrackedPR. Input is
// stored as JSON so schema changes to Input don't require a table migration.
type TrackedPRRecord struct {
	IncidentID string
	Repo       string
	PRNumber   int
	Branch     string
	BaseBranch string
	InputJSON  []byte
	AddedAt    time.Time
}

// TrackedPRStore persists tracked PRs across restarts. Implemented by
// feedback.Store. A nil store (or unset via SetPersistence) means
// memory-only operation — tracked PRs are lost on restart, which is the
// pre-1.7 behavior and still useful for tests.
type TrackedPRStore interface {
	// SaveTrackedPR upserts a tracked PR keyed on (repo, pr_number).
	SaveTrackedPR(ctx context.Context, rec TrackedPRRecord) error
	// DeleteTrackedPR removes a tracked PR.
	DeleteTrackedPR(ctx context.Context, repo string, prNumber int) error
	// ListTrackedPRs returns all persisted tracked PRs (oldest first).
	ListTrackedPRs(ctx context.Context) ([]TrackedPRRecord, error)
}

// persistenceKey identifies a record for logging.
func persistenceKey(rec TrackedPRRecord) string {
	return prKey(rec.Repo, rec.PRNumber)
}

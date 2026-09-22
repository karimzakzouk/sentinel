package feedback

import (
	"context"
	"time"

	"github.com/karimzakzouk/sentinel/internal/automerge"
)

// This file implements automerge.TrackedPRStore on top of the SQLite store,
// so the auto-merge merger can survive restarts: tracked PRs are written on
// Track, deleted on Untrack, and re-adopted by Merger.Reconcile at startup.

// SaveTrackedPR upserts a tracked auto-merge PR keyed on (repo, pr_number).
func (s *Store) SaveTrackedPR(ctx context.Context, rec automerge.TrackedPRRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_prs (repo, pr_number, incident_id, branch, base_branch, input_json, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, pr_number) DO UPDATE SET
			incident_id = excluded.incident_id,
			branch = excluded.branch,
			base_branch = excluded.base_branch,
			input_json = excluded.input_json,
			added_at = excluded.added_at`,
		rec.Repo, rec.PRNumber, rec.IncidentID, rec.Branch, rec.BaseBranch,
		string(rec.InputJSON), rec.AddedAt.UTC())
	return err
}

// DeleteTrackedPR removes a tracked auto-merge PR.
func (s *Store) DeleteTrackedPR(ctx context.Context, repo string, prNumber int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM tracked_prs WHERE repo = ? AND pr_number = ?`, repo, prNumber)
	return err
}

// ListTrackedPRs returns all persisted tracked PRs, oldest first.
func (s *Store) ListTrackedPRs(ctx context.Context) ([]automerge.TrackedPRRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT repo, pr_number, incident_id, branch, base_branch, input_json, added_at
		FROM tracked_prs ORDER BY added_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []automerge.TrackedPRRecord
	for rows.Next() {
		var rec automerge.TrackedPRRecord
		var inputJSON string
		var addedAt time.Time
		if err := rows.Scan(&rec.Repo, &rec.PRNumber, &rec.IncidentID,
			&rec.Branch, &rec.BaseBranch, &inputJSON, &addedAt); err != nil {
			return nil, err
		}
		rec.InputJSON = []byte(inputJSON)
		rec.AddedAt = addedAt
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Package outbox implements a durable incident queue using SQLite.
//
// When Sentinel receives an incident signal (from log detection, pod status,
// Alertmanager, or manual trigger), it enqueues the signal to the outbox
// table. A worker goroutine dequeues + processes signals one at a time.
//
// If Sentinel crashes mid-investigation, the outbox row is still marked
// "processing". On restart, RecoverPending() resets it to "pending" and
// it's processed again. This gives at-least-once delivery — a crashed
// investigation is retried on the next startup.
package outbox

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
)

// Signal is a queued incident signal.
type Signal struct {
	ID          int64
	IncidentID  string
	Service     string
	Namespace   string
	Reason      string
	Repo        string
	RawLogs     string
	Status      string
	CreatedAt   time.Time
	ProcessedAt *time.Time
	Attempts    int
	LastError   string
}

// Queue is the durable incident queue backed by SQLite.
type Queue struct {
	db *sql.DB
}

// New creates a new outbox Queue.
func New(db *sql.DB) (*Queue, error) {
	if err := initSchema(db); err != nil {
		return nil, fmt.Errorf("init outbox schema: %w", err)
	}
	return &Queue{db: db}, nil
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS incident_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			incident_id TEXT NOT NULL,
			service TEXT NOT NULL,
			namespace TEXT NOT NULL,
			reason TEXT NOT NULL,
			repo TEXT,
			raw_logs TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL,
			processed_at TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_outbox_status ON incident_outbox(status);
		CREATE INDEX IF NOT EXISTS idx_outbox_incident ON incident_outbox(incident_id);
	`)
	return err
}

// Enqueue adds a signal to the queue. Deduplicates by service+namespace
// (if a pending/processing incident exists, returns its ID).
func (q *Queue) Enqueue(ctx context.Context, service, namespace, reason, repo, rawLogs string) (string, error) {
	incidentID := uuid.New().String()

	var existingID string
	err := q.db.QueryRowContext(ctx, `
		SELECT incident_id FROM incident_outbox
		WHERE service = ? AND namespace = ? AND status IN ('pending', 'processing')
		ORDER BY created_at DESC LIMIT 1
	`, service, namespace).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("check existing: %w", err)
	}

	_, err = q.db.ExecContext(ctx, `
		INSERT INTO incident_outbox (incident_id, service, namespace, reason, repo, raw_logs, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)
	`, incidentID, service, namespace, reason, repo, rawLogs, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("insert: %w", err)
	}
	return incidentID, nil
}

// Dequeue returns the next pending signal and marks it as "processing".
func (q *Queue) Dequeue(ctx context.Context) (*Signal, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var sig Signal
	var createdAtStr string
	var processedAt sql.NullString
	var repo, rawLogs, lastError sql.NullString

	err = tx.QueryRowContext(ctx, `
		SELECT id, incident_id, service, namespace, reason, repo, raw_logs, status, created_at, processed_at, attempts, last_error
		FROM incident_outbox
		WHERE status = 'pending'
		ORDER BY created_at ASC
		LIMIT 1
	`).Scan(&sig.ID, &sig.IncidentID, &sig.Service, &sig.Namespace, &sig.Reason,
		&repo, &rawLogs, &sig.Status, &createdAtStr, &processedAt, &sig.Attempts, &lastError)
	if err != nil {
		return nil, err
	}

	sig.Repo = repo.String
	sig.RawLogs = rawLogs.String
	sig.LastError = lastError.String
	sig.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	if processedAt.Valid {
		t, _ := time.Parse(time.RFC3339, processedAt.String)
		sig.ProcessedAt = &t
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE incident_outbox SET status = 'processing', attempts = attempts + 1 WHERE id = ?
	`, sig.ID)
	if err != nil {
		return nil, err
	}
	return &sig, tx.Commit()
}

// MarkDone marks a signal as successfully processed.
func (q *Queue) MarkDone(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE incident_outbox SET status = 'done', processed_at = ? WHERE id = ?
	`, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// MarkFailed marks a signal as failed. After 3 attempts, status becomes
// "failed" permanently; otherwise it goes back to "pending" for retry.
func (q *Queue) MarkFailed(ctx context.Context, id int64, lastError string) error {
	var attempts int
	err := q.db.QueryRowContext(ctx, `SELECT attempts FROM incident_outbox WHERE id = ?`, id).Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts >= 3 {
		_, err = q.db.ExecContext(ctx, `
			UPDATE incident_outbox SET status = 'failed', last_error = ?, processed_at = ? WHERE id = ?
		`, lastError, time.Now().UTC().Format(time.RFC3339), id)
	} else {
		_, err = q.db.ExecContext(ctx, `
			UPDATE incident_outbox SET status = 'pending', last_error = ? WHERE id = ?
		`, lastError, id)
	}
	return err
}

// RecoverPending resets "processing" rows back to "pending" on startup.
func (q *Queue) RecoverPending(ctx context.Context) (int, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE incident_outbox SET status = 'pending' WHERE status = 'processing'
	`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		logging.Info("recovered  interrupted incidents", "n", n)
	}
	return int(n), nil
}

// PendingCount returns the number of pending signals.
func (q *Queue) PendingCount(ctx context.Context) (int, error) {
	var count int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM incident_outbox WHERE status = 'pending'`).Scan(&count)
	return count, err
}

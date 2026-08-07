package feedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// AppendEvent appends an audit event to the per-incident event log.
func (s *Store) AppendEvent(ctx context.Context, incidentID uuid.UUID, layer, eventType string, agent *string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (incident_id, ts, layer, event_type, agent, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, incidentID.String(), time.Now().UTC(), layer, eventType, nullableStringPtr(agent), string(payloadJSON))
	return err
}

// GetBetaPrior returns the Beta distribution prior (alpha, beta, successes,
// failures) for a given service + fix_type. Returns (1.0, 1.0, 0, 0, nil)
// if no prior data exists — a uniform Beta(1,1) is the maximum-entropy
// starting point.
func (s *Store) GetBetaPrior(ctx context.Context, service, fixType string) (alpha, beta float64, successes, failures int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT alpha, beta, successes, failures FROM beta_priors WHERE service = ? AND fix_type = ?
	`, service, fixType)

	err = row.Scan(&alpha, &beta, &successes, &failures)
	if err == sql.ErrNoRows {
		return 1.0, 1.0, 0, 0, nil
	}
	return
}

// RecordBetaOutcome updates the Beta prior for a service + fix_type after
// a fix succeeds or fails. Returns the new (alpha, beta).
func (s *Store) RecordBetaOutcome(ctx context.Context, service, fixType string, success bool) (alpha, beta float64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := 1.0
	currentBeta := 1.0
	succ := 0
	fail := 0

	row := s.db.QueryRowContext(ctx, `SELECT alpha, beta, successes, failures FROM beta_priors WHERE service = ? AND fix_type = ?`, service, fixType)
	if err := row.Scan(&current, &currentBeta, &succ, &fail); err != nil && err != sql.ErrNoRows {
		return 0, 0, err
	}

	if success {
		current++
		succ++
	} else {
		currentBeta++
		fail++
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO beta_priors (service, fix_type, alpha, beta, successes, failures)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(service, fix_type) DO UPDATE SET
			alpha = excluded.alpha,
			beta = excluded.beta,
			successes = excluded.successes,
			failures = excluded.failures
	`, service, fixType, current, currentBeta, succ, fail)
	if err != nil {
		return 0, 0, err
	}
	return current, currentBeta, nil
}

// StoreIncidentVector stores an incident's embedding for future RAG
// retrieval. The embedding is computed upstream (typically from cluster
// summary + root cause + fix pattern).
func (s *Store) StoreIncidentVector(ctx context.Context, inc *schemas.Incident, outcomeSuccess bool, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if inc.State.RootCause == nil || inc.State.FixProposal == nil {
		return nil
	}

	summary := inc.Cluster.Summary
	rootCause := inc.State.RootCause.Statement
	fixPattern := inc.State.FixProposal.Description

	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}

	sourceFilesJSON, _ := json.Marshal(inc.State.RootCause.SourceFiles)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO incident_vectors (incident_id, service, root_cause_category, fix_type, summary, root_cause, fix_pattern, source_files_json, git_commit, outcome_verified, outcome_success, embedding_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(incident_id) DO UPDATE SET
			service = excluded.service,
			root_cause_category = excluded.root_cause_category,
			fix_type = excluded.fix_type,
			summary = excluded.summary,
			root_cause = excluded.root_cause,
			fix_pattern = excluded.fix_pattern,
			source_files_json = excluded.source_files_json,
			git_commit = excluded.git_commit,
			outcome_verified = excluded.outcome_verified,
			outcome_success = excluded.outcome_success,
			embedding_json = excluded.embedding_json
	`,
		inc.IncidentID.String(), inc.Cluster.Service,
		inc.State.RootCause.Category,
		string(inc.State.FixProposal.FixType),
		summary, rootCause, fixPattern,
		string(sourceFilesJSON),
		nullableString(inc.State.RootCause.GitCommit),
		boolToInt(inc.Status == schemas.StatusVerified),
		boolToInt(outcomeSuccess),
		string(embeddingJSON),
		time.Now().UTC(),
	)
	return err
}

// UpdateVectorOutcome marks an incident's vector entry as outcome-verified
// with the given success flag. Called by OutcomeWatcher.recordOutcome after
// the recovery check completes.
func (s *Store) UpdateVectorOutcome(ctx context.Context, incidentID string, success bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE incident_vectors SET outcome_verified = 1, outcome_success = ? WHERE incident_id = ?`,
		boolToInt(success), incidentID)
	return err
}

// CommandEvent is one row in the command_events audit log.
type CommandEvent struct {
	EventID          int64     `json:"event_id"`
	IncidentID       string    `json:"incident_id,omitempty"`
	PRNumber         int       `json:"pr_number"`
	PRRepo           string    `json:"pr_repo"`
	Command          string    `json:"command"`
	Args             string    `json:"args,omitempty"`
	Author           string    `json:"author"`
	AuthorPermission string    `json:"author_permission"`
	Result           string    `json:"result"`
	ResultDetail     string    `json:"result_detail,omitempty"`
	Timestamp        time.Time `json:"ts"`
}

// LogCommandEvent writes a chatops command invocation to the audit log.
func (s *Store) LogCommandEvent(ctx context.Context, ev *CommandEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var incidentID any
	if ev.IncidentID != "" {
		incidentID = ev.IncidentID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO command_events (incident_id, pr_number, pr_repo, command, args, author, author_permission, result, result_detail, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, incidentID, ev.PRNumber, ev.PRRepo, ev.Command, ev.Args, ev.Author, ev.AuthorPermission, ev.Result, ev.ResultDetail, ev.Timestamp)
	return err
}

// ListCommandEvents returns recent command events for analytics. If repo is
// non-empty, results are filtered to that repo. limit defaults to 100 and
// is capped at 1000.
func (s *Store) ListCommandEvents(ctx context.Context, repo string, limit int) ([]*CommandEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if repo == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT event_id, incident_id, pr_number, pr_repo, command, args, author, author_permission, result, result_detail, ts
			FROM command_events ORDER BY ts DESC LIMIT ?
		`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT event_id, incident_id, pr_number, pr_repo, command, args, author, author_permission, result, result_detail, ts
			FROM command_events WHERE pr_repo = ? ORDER BY ts DESC LIMIT ?
		`, repo, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CommandEvent
	for rows.Next() {
		ev := &CommandEvent{}
		var incidentID sql.NullString
		var args, detail sql.NullString
		if err := rows.Scan(&ev.EventID, &incidentID, &ev.PRNumber, &ev.PRRepo, &ev.Command, &args, &ev.Author, &ev.AuthorPermission, &ev.Result, &detail, &ev.Timestamp); err != nil {
			continue
		}
		if incidentID.Valid {
			ev.IncidentID = incidentID.String
		}
		if args.Valid {
			ev.Args = args.String
		}
		if detail.Valid {
			ev.ResultDetail = detail.String
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

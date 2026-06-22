// Package feedback provides the storage layer for Sentinel v3.
// v3: SQLite replaces both Postgres (incident store) AND Weaviate (vector memory).
// One file, one DB, one process. pgvector-style vector search implemented in
// pure Go for the "similar past incidents" RAG query.
package feedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/karimzakzoukz/sentinel/v3/internal/schemas"
)

// Store is the single storage layer for Sentinel v3.
// Replaces: IncidentStore (Postgres), EventStore (Postgres), BetaStore (Postgres),
// WeaviateStore (Weaviate vector DB).
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// New opens (or creates) the SQLite database at path and initializes the schema.
func New(path string) (*Store, error) {
	// Use WAL mode for concurrent reads + writes, and busy_timeout to avoid
	// "database is locked" errors under load.
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS incidents (
			incident_id TEXT PRIMARY KEY,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			service TEXT NOT NULL,
			namespace TEXT NOT NULL,
			status TEXT NOT NULL,
			severity TEXT,
			cluster_json TEXT NOT NULL,
			state_json TEXT NOT NULL,
			beta_alpha REAL DEFAULT 1.0,
			beta_beta REAL DEFAULT 1.0,
			git_commit_at_resolution TEXT,
			postmortem TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_incidents_service ON incidents(service);
		CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);
		CREATE INDEX IF NOT EXISTS idx_incidents_updated ON incidents(updated_at DESC);

		CREATE TABLE IF NOT EXISTS events (
			event_id INTEGER PRIMARY KEY AUTOINCREMENT,
			incident_id TEXT NOT NULL,
			ts DATETIME NOT NULL,
			layer TEXT NOT NULL,
			event_type TEXT NOT NULL,
			agent TEXT,
			payload_json TEXT,
			FOREIGN KEY (incident_id) REFERENCES incidents(incident_id)
		);
		CREATE INDEX IF NOT EXISTS idx_events_incident ON events(incident_id);

		CREATE TABLE IF NOT EXISTS beta_priors (
			service TEXT NOT NULL,
			fix_type TEXT NOT NULL,
			alpha REAL DEFAULT 1.0,
			beta REAL DEFAULT 1.0,
			successes INTEGER DEFAULT 0,
			failures INTEGER DEFAULT 0,
			PRIMARY KEY (service, fix_type)
		);

		-- Vector store for incident memory (replaces Weaviate).
		-- Embeddings stored as JSON arrays of floats. Similarity search is
		-- cosine similarity computed in Go. At our scale (<100K incidents)
		-- this is faster than Weaviate and needs no separate pod.
		CREATE TABLE IF NOT EXISTS incident_vectors (
			incident_id TEXT PRIMARY KEY,
			service TEXT NOT NULL,
			root_cause_category TEXT,
			fix_type TEXT,
			summary TEXT NOT NULL,
			root_cause TEXT,
			fix_pattern TEXT,
			source_files_json TEXT,
			git_commit TEXT,
			outcome_verified INTEGER DEFAULT 0,
			outcome_success INTEGER DEFAULT 0,
			embedding_json TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (incident_id) REFERENCES incidents(incident_id)
		);
		CREATE INDEX IF NOT EXISTS idx_vectors_service ON incident_vectors(service);
	`)
	return err
}

// UpsertIncident stores or updates an incident.
func (s *Store) UpsertIncident(ctx context.Context, inc *schemas.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clusterJSON, err := json.Marshal(inc.Cluster)
	if err != nil {
		return fmt.Errorf("marshal cluster: %w", err)
	}
	stateJSON, err := json.Marshal(inc.State)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	now := time.Now().UTC()
	inc.UpdatedAt = now

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO incidents (incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(incident_id) DO UPDATE SET
			updated_at = excluded.updated_at,
			status = excluded.status,
			severity = excluded.severity,
			cluster_json = excluded.cluster_json,
			state_json = excluded.state_json,
			beta_alpha = excluded.beta_alpha,
			beta_beta = excluded.beta_beta,
			git_commit_at_resolution = excluded.git_commit_at_resolution,
			postmortem = excluded.postmortem
	`,
		inc.IncidentID.String(), inc.CreatedAt, inc.UpdatedAt,
		inc.Cluster.Service, inc.Cluster.Namespace, string(inc.Status),
		string(inc.Cluster.Severity), string(clusterJSON), string(stateJSON),
		inc.BetaAlpha, inc.BetaBeta,
		nullableString(inc.GitCommitAtResolution),
		nullableString(inc.Postmortem),
	)
	return err
}

// GetIncident retrieves a single incident by ID.
func (s *Store) GetIncident(ctx context.Context, id uuid.UUID) (*schemas.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
		FROM incidents WHERE incident_id = ?
	`, id.String())

	var inc schemas.Incident
	var incidentIDStr, statusStr, severityStr, clusterJSON, stateJSON string
	var betaAlpha, betaBeta float64
	var createdAt, updatedAt time.Time
	var gitCommit, postmortem sql.NullString

	if err := row.Scan(
		&incidentIDStr, &createdAt, &updatedAt,
		&inc.Cluster.Service, &inc.Cluster.Namespace, &statusStr, &severityStr,
		&clusterJSON, &stateJSON, &betaAlpha, &betaBeta,
		&gitCommit, &postmortem,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	parsedID, err := uuid.Parse(incidentIDStr)
	if err != nil {
		return nil, fmt.Errorf("parse incident ID: %w", err)
	}
	inc.IncidentID = parsedID
	inc.CreatedAt = createdAt
	inc.UpdatedAt = updatedAt
	inc.Status = schemas.IncidentStatus(statusStr)
	inc.Cluster.Severity = schemas.Severity(severityStr)
	inc.BetaAlpha = betaAlpha
	inc.BetaBeta = betaBeta
	if gitCommit.Valid {
		s := gitCommit.String
		inc.GitCommitAtResolution = &s
	}
	if postmortem.Valid {
		s := postmortem.String
		inc.Postmortem = &s
	}

	if err := json.Unmarshal([]byte(clusterJSON), &inc.Cluster); err != nil {
		return nil, fmt.Errorf("unmarshal cluster: %w", err)
	}
	if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	return &inc, nil
}

// ListRecentIncidents returns the most recent N incidents.
func (s *Store) ListRecentIncidents(ctx context.Context, limit int) ([]*schemas.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > 500 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
		FROM incidents ORDER BY updated_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*schemas.Incident
	for rows.Next() {
		var inc schemas.Incident
		var incidentIDStr, statusStr, severityStr, clusterJSON, stateJSON string
		var betaAlpha, betaBeta float64
		var createdAt, updatedAt time.Time
		var gitCommit, postmortem sql.NullString

		if err := rows.Scan(
			&incidentIDStr, &createdAt, &updatedAt,
			&inc.Cluster.Service, &inc.Cluster.Namespace, &statusStr, &severityStr,
			&clusterJSON, &stateJSON, &betaAlpha, &betaBeta,
			&gitCommit, &postmortem,
		); err != nil {
			return nil, err
		}

		parsedID, err := uuid.Parse(incidentIDStr)
		if err != nil {
			continue
		}
		inc.IncidentID = parsedID
		inc.CreatedAt = createdAt
		inc.UpdatedAt = updatedAt
		inc.Status = schemas.IncidentStatus(statusStr)
		inc.Cluster.Severity = schemas.Severity(severityStr)
		inc.BetaAlpha = betaAlpha
		inc.BetaBeta = betaBeta
		if gitCommit.Valid {
			s := gitCommit.String
			inc.GitCommitAtResolution = &s
		}
		if postmortem.Valid {
			s := postmortem.String
			inc.Postmortem = &s
		}

		if err := json.Unmarshal([]byte(clusterJSON), &inc.Cluster); err != nil {
			continue
		}
		if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
			continue
		}
		out = append(out, &inc)
	}
	return out, rows.Err()
}

// AppendEvent appends an audit event to the event log.
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

// GetBetaPrior returns the Beta distribution prior (alpha, beta, successes, failures)
// for a given service + fix_type. Returns (1.0, 1.0, 0, 0) if no prior data.
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

// RecordBetaOutcome updates the Beta prior for a service + fix_type after a
// fix succeeds or fails. Returns the new (alpha, beta).
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

// StoreIncidentVector stores an incident's embedding for future RAG retrieval.
// The embedding is computed from the cluster summary + root cause + fix pattern.
// In v3 we use a simple bag-of-words embedding (no external model required).
// This is replaced by a real embedding model in v3.1 if needed.
func (s *Store) StoreIncidentVector(ctx context.Context, inc *schemas.Incident, outcomeSuccess bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if inc.State.RootCause == nil || inc.State.FixProposal == nil {
		return nil
	}

	// Build the text signature for embedding
	summary := inc.Cluster.Summary
	rootCause := inc.State.RootCause.Statement
	fixPattern := inc.State.FixProposal.Description
	embedding := computeEmbedding(summary + " " + rootCause + " " + fixPattern)

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

// RetrieveSimilarIncidents returns the top-K most similar past incidents
// using cosine similarity on the embedding vectors. This replaces the
// Weaviate nearText query.
func (s *Store) RetrieveSimilarIncidents(ctx context.Context, cluster schemas.AnomalyCluster, topK int) ([]map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if topK <= 0 {
		topK = 5
	}

	queryEmbedding := computeEmbedding(cluster.Summary + " " + cluster.Service)

	rows, err := s.db.QueryContext(ctx, `
		SELECT incident_id, service, root_cause_category, fix_type, summary, root_cause, fix_pattern, source_files_json, git_commit, outcome_verified, outcome_success, embedding_json
		FROM incident_vectors
		WHERE service = ?
		ORDER BY created_at DESC
		LIMIT 200
	`, cluster.Service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		props    map[string]any
		embedding []float64
	}
	var candidates []candidate

	for rows.Next() {
		var incidentID, service, rootCauseCategory, fixType, summary, rootCause, fixPattern, sourceFilesJSON, embeddingJSON string
		var gitCommit sql.NullString
		var outcomeVerified, outcomeSuccess int

		if err := rows.Scan(
			&incidentID, &service, &rootCauseCategory, &fixType,
			&summary, &rootCause, &fixPattern, &sourceFilesJSON,
			&gitCommit, &outcomeVerified, &outcomeSuccess, &embeddingJSON,
		); err != nil {
			continue
		}

		var embedding []float64
		if err := json.Unmarshal([]byte(embeddingJSON), &embedding); err != nil {
			continue
		}

		candidates = append(candidates, candidate{
			props: map[string]any{
				"incident_id":         incidentID,
				"service":             service,
				"root_cause_category": rootCauseCategory,
				"fix_type":            fixType,
				"summary":             summary,
				"root_cause":          rootCause,
				"fix_pattern":         fixPattern,
				"source_files":        sourceFilesJSON,
				"git_commit":          gitCommit.String,
				"outcome_verified":    outcomeVerified == 1,
				"outcome_success":     outcomeSuccess == 1,
			},
			embedding: embedding,
		})
	}

	// Compute cosine similarity and sort
	for i := range candidates {
		candidates[i].props["semantic_score"] = cosineSim(queryEmbedding, candidates[i].embedding)
	}

	// Sort by semantic_score descending
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			si, _ := candidates[i].props["semantic_score"].(float64)
			sj, _ := candidates[j].props["semantic_score"].(float64)
			if sj > si {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	// Return top K
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	out := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.props)
	}
	return out, nil
}

// computeEmbedding produces a simple bag-of-words vector from text.
// This is a placeholder — v3.1 will use a real embedding model (sentence-transformers
// via a local ONNX model, or the LLM provider's embedding API).
// For now this gives us working similarity search with zero external deps.
func computeEmbedding(text string) []float64 {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return []float64{0}
	}

	// Simple: hash each word into one of 256 buckets, count occurrences
	const dim = 256
	vec := make([]float64, dim)
	for _, w := range words {
		h := simpleHash(w) % dim
		vec[h]++
	}

	// L2 normalize
	var sum float64
	for _, v := range vec {
		sum += v * v
	}
	if sum > 0 {
		norm := math.Sqrt(sum)
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

func simpleHash(s string) int {
	h := 0
	for _, c := range s {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

func cosineSim(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func nullableString(s *string) interface{} {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullableStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

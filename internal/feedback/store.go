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
        // "math" // removed: no longer needed after float32 migration
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"
        _ "modernc.org/sqlite"

        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
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
        // modernc.org/sqlite uses _pragma= query params for pragmas.
        // WAL mode for concurrent reads + writes.
        // busy_timeout=10000ms to wait instead of returning SQLITE_BUSY immediately.
        // foreign_keys=on for referential integrity.
        dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(on)", path)
        db, err := sql.Open("sqlite", dsn)
        if err != nil {
                return nil, fmt.Errorf("open sqlite: %w", err)
        }

        // SQLite only allows one writer at a time. Limit connections to avoid
        // "database is locked" errors under concurrent access.
        db.SetMaxOpenConns(1)
        db.SetMaxIdleConns(1)
        db.SetConnMaxLifetime(0) // keep connections open forever

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

// FindIncidentByPrefix finds an incident whose UUID starts with the given
// prefix. If multiple incidents match, returns the most recently updated one.
// Returns nil (no error) if no match.
//
// v3.8.4: Added so the CLI can accept the 8-char truncated IDs it displays
// in `incidents list` output. Without this, `sentinelctl incidents get
// 91cce5a5` would fail with "invalid incident ID" because the server did
// uuid.Parse("91cce5a5") which requires a full 36-char UUID.
//
// The prefix must be at least 4 chars to prevent accidental collisions on
// short prefixes (a 1-char prefix would match ~1/16 of all UUIDs).
func (s *Store) FindIncidentByPrefix(ctx context.Context, prefix string) (*schemas.Incident, error) {
        if len(prefix) < 4 {
                return nil, nil
        }
        // Normalize: lowercase, strip hyphens so "91cce5a5" and "91cce5a5-1234"
        // both work against stored UUIDs (which are stored as lowercase strings
        // with hyphens).
        cleaned := strings.ToLower(strings.ReplaceAll(prefix, "-", ""))
        if len(cleaned) > 36 {
                return nil, nil
        }

        s.mu.RLock()
        defer s.mu.RUnlock()

        // Pull the most recent 500 incidents and scan in Go. SQLite's LIKE on
        // a hyphenated UUID is awkward (the stored form has hyphens, the prefix
        // doesn't), and an index on incident_id won't help with prefix matches
        // anyway. 500 is plenty — if you have >500 recent incidents, use the
        // full UUID.
        rows, err := s.db.QueryContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents ORDER BY updated_at DESC LIMIT 500
        `)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        var best *schemas.Incident
        var bestUpdatedAt time.Time
        for rows.Next() {
                inc, updatedAt, err := scanIncidentRow(rows)
                if err != nil {
                        return nil, err
                }
                // Compare against the prefix. The stored incident_id is a full
                // UUID string like "91cce5a5-1234-5678-9abc-def012345678".
                // Strip its hyphens too for the comparison.
                stored := strings.ReplaceAll(strings.ToLower(inc.IncidentID.String()), "-", "")
                if strings.HasPrefix(stored, cleaned) {
                        if best == nil || updatedAt.After(bestUpdatedAt) {
                                best = inc
                                bestUpdatedAt = updatedAt
                        }
                }
        }
        return best, nil
}

// scanIncidentRow is the shared row-scan logic for GetIncident,
// FindIncidentByPrefix, and ListRecentIncidents. Takes a sql.Rows (so it
// works for both single-row QueryRow and multi-row Query).
type rowScanner interface {
        Scan(dest ...any) error
}

func scanIncidentRow(row rowScanner) (*schemas.Incident, time.Time, error) {
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
                return nil, time.Time{}, err
        }

        parsedID, err := uuid.Parse(incidentIDStr)
        if err != nil {
                return nil, time.Time{}, fmt.Errorf("parse incident ID: %w", err)
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
                return nil, time.Time{}, fmt.Errorf("unmarshal cluster: %w", err)
        }
        if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
                return nil, time.Time{}, fmt.Errorf("unmarshal state: %w", err)
        }
        return &inc, updatedAt, nil
}
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

// computeEmbedding produces a bag-of-words vector from text.
// v3.13.0: CRITICAL FIX — now calls hashEmbedding() (the same tokenizer used
// by the runtime VectorStore) instead of strings.Fields(). Previously,
// computeEmbedding used strings.Fields+ToLower (which keeps punctuation in
// words: "KeyError:" → "keyerror:") while hashEmbedding used splitWords
// (which strips punctuation: "KeyError:" → "KeyError", no lowercasing).
// Same text produced DIFFERENT vectors. After pod restart, LoadFromDB
// loaded the computeEmbedding vectors into HNSW, but runtime queries used
// hashEmbedding — cosine similarity was near-zero for all stored incidents.
// The entire memory loop (applyKnownFix, similar-incident retrieval) was
// silently broken across restarts. Now both use the same tokenizer.
func computeEmbedding(text string) []float32 {
        return hashEmbedding(text, 1536)
}

func simpleHash32(s string) uint32 {
        h := uint32(2166136261)
        for _, c := range s {
                h ^= uint32(c)
                h *= 16777619
        }
        return h
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

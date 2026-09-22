package feedback

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Store is the single storage layer for Sentinel. It wraps a SQLite
// database with WAL mode for concurrent read/write access.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// New opens (or creates) the SQLite database at path and initializes the
// schema. The DSN enables WAL mode, a 10-second busy timeout, and foreign
// keys.
func New(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(on)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// WAL allows concurrent readers + 1 writer. 4 connections lets reads
	// proceed in parallel while writes serialize naturally.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return store, nil
}

// Close closes the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// initSchema creates the tables if they don't exist, then runs the
// idempotent additive migrations on the incidents table.
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

                -- incident_vectors is the RAG table. Embeddings are stored as JSON
                -- float arrays; the in-memory VectorStore overlays an HNSW graph
                -- for fast cosine-similarity search at query time.
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

                -- command_events is the chatops audit log. Indexed on
                -- (pr_repo, pr_number) so /sentinel command lookup on a PR is O(1).
                CREATE TABLE IF NOT EXISTS command_events (
                        event_id INTEGER PRIMARY KEY AUTOINCREMENT,
                        incident_id TEXT,
                        pr_number INTEGER NOT NULL,
                        pr_repo TEXT NOT NULL,
                        command TEXT NOT NULL,
                        args TEXT,
                        author TEXT NOT NULL,
                        author_permission TEXT NOT NULL,
                        result TEXT NOT NULL,
                        result_detail TEXT,
                        ts DATETIME NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_command_events_pr ON command_events(pr_repo, pr_number);
                CREATE INDEX IF NOT EXISTS idx_command_events_incident ON command_events(incident_id);
                CREATE INDEX IF NOT EXISTS idx_command_events_ts ON command_events(ts DESC);

                -- tracked_prs survives restarts for the auto-merge merger.
                -- Each row is one PR pending auto-merge evaluation; the full
                -- automerge.Input is stored as JSON so Input schema changes
                -- don't require a table migration. Rows are deleted when the
                -- PR is merged, closed, or skipped.
                CREATE TABLE IF NOT EXISTS tracked_prs (
                        repo TEXT NOT NULL,
                        pr_number INTEGER NOT NULL,
                        incident_id TEXT NOT NULL,
                        branch TEXT NOT NULL DEFAULT '',
                        base_branch TEXT NOT NULL DEFAULT '',
                        input_json TEXT NOT NULL,
                        added_at DATETIME NOT NULL,
                        PRIMARY KEY (repo, pr_number)
                );
        `)
	if err != nil {
		return err
	}

	return s.migrateSchema()
}

// migrateSchema applies additive column migrations to the incidents table.
// SQLite doesn't support IF NOT EXISTS on ADD COLUMN, so each column is
// guarded by a PRAGMA table_info check.
func (s *Store) migrateSchema() error {
	rows, err := s.db.Query(`PRAGMA table_info(incidents)`)
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	type colAdd struct {
		name, decl string
	}
	additions := []colAdd{
		{"pr_number", "INTEGER"},
		{"pr_repo", "TEXT"},
		{"pr_branch", "TEXT"},
		{"pr_url", "TEXT"},
		{"auto_merge_status", "TEXT DEFAULT 'not_eligible'"},
	}
	for _, a := range additions {
		if existing[a.name] {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE incidents ADD COLUMN %s %s", a.name, a.decl)); err != nil {
			return fmt.Errorf("alter table add %s: %w", a.name, err)
		}
		logging.Info("feedback: migration added column to incidents table", "name", a.name)
	}

	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_pr ON incidents(pr_repo, pr_number) WHERE pr_number IS NOT NULL`); err != nil {
		logging.Warn("feedback: could not create idx_incidents_pr (continuing)", "err", err)
	}
	return nil
}

// nullableString converts a *string to sql.NullString.
func nullableString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

// nullableStringPtr is an alias of nullableString kept for call-site
// readability when the input represents a pointer-typed optional field.
func nullableStringPtr(s *string) sql.NullString {
	return nullableString(s)
}

// boolToInt converts a bool to the integer 0/1 SQLite stores.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

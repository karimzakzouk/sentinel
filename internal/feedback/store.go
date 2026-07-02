// Package feedback provides the storage layer for Sentinel v3.
// v3: SQLite replaces both Postgres (incident store) AND Weaviate (vector memory).
// One file, one DB, one process. pgvector-style vector search implemented in
// pure Go for the "similar past incidents" RAG query.
package feedback

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "database/sql"
        "fmt"
        "sync"

        _ "modernc.org/sqlite"
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

                -- v4.0.0: PR-tracking columns on incidents (added via ALTER in
                -- migrateSchema — see that function for the migration logic).
                -- Columns: pr_number INTEGER, pr_repo TEXT, pr_branch TEXT,
                -- pr_url TEXT, auto_merge_status TEXT.
                -- Indexed on (pr_repo, pr_number) so chatops command lookup is O(1).

                -- v4.0.0: Audit log for chatops commands. Every /sentinel command
                -- is logged here for compliance + analytics ("which commands are
                -- actually used?").
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
        `)
        if err != nil {
                return err
        }

        // SQLite doesn't support IF NOT EXISTS on ADD COLUMN, so we check the
        // schema first and skip columns that already exist.

        return s.migrateSchema()
}

// migrateSchema applies additive column migrations to the incidents table.
// columns for chatops support. Idempotent — safe to run on every startup.

func (s *Store) migrateSchema() error {
        // PRAGMA table_info returns one row per column. We check which columns
        // already exist before issuing ALTER TABLE statements.

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
                logging.Info("migration: added column to incidents table", "name", a.name)
        }

        // Add the (pr_repo, pr_number) index if it doesn't exist.
        // SQLite supports CREATE INDEX IF NOT EXISTS so we can do this unconditionally.

        if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_pr ON incidents(pr_repo, pr_number) WHERE pr_number IS NOT NULL`); err != nil {
                logging.Warn("could not create idx_incidents_pr:  (continuing)", "err", err)
        }
        return nil
}

// UpsertIncident stores or updates an incident.


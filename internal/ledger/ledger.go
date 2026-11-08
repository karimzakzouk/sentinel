// Package ledger is Sentinel's append-only, hash-chained event log. It is both
// the system of record and the audit trail: every record change is an event,
// and the current state of each record is a projection that can be rebuilt
// from the events at any time.
package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/karimzakzouk/sentinel/internal/model"
)

// GenesisHash is the previous-hash value of the first event.
var GenesisHash = strings.Repeat("0", sha256.Size*2)

// ErrTampered reports that the stored events no longer match their hash chain.
var ErrTampered = errors.New("ledger hash chain is broken")

// Event is one entry in the log. It maps onto a CloudEvents 1.0 envelope.
type Event struct {
	Seq        int64           `json:"sequence"`
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Source     string          `json:"source"`
	Subject    string          `json:"subject"`
	Time       time.Time       `json:"time"`
	EntityKind model.Kind      `json:"entityKind,omitempty"`
	EntityID   model.ID        `json:"entityId,omitempty"`
	IncidentID model.ID        `json:"incidentId,omitempty"`
	Data       json.RawMessage `json:"data"`
	PrevHash   string          `json:"prevHash"`
	Hash       string          `json:"hash"`
}

// Draft is an event before the Ledger assigns its sequence, time and hashes.
type Draft struct {
	Type    string
	Source  string
	Subject string
	Data    any
	// Entity, when set, makes the event replace the projected state of that record.
	Entity *Entity
}

// Entity identifies the record an event updates.
type Entity struct {
	Kind       model.Kind
	ID         model.ID
	IncidentID model.ID
}

// RecordDraft builds the event for a change to a model record. The data is the
// record's full new state, so projections never have to merge partial updates.
func RecordDraft(source string, kind model.Kind, verb string, id, incidentID model.ID, record any) Draft {
	return Draft{
		Type:    "sentinel." + string(kind) + "." + verb,
		Source:  source,
		Subject: string(id),
		Data:    record,
		Entity:  &Entity{Kind: kind, ID: id, IncidentID: incidentID},
	}
}

// Ledger is a SQLite-backed event log. It is safe for concurrent use within
// one process; only one process may write to a ledger file at a time.
type Ledger struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
}

// Open opens or creates the ledger at path and applies schema migrations.
func Open(ctx context.Context, path string) (*Ledger, error) {
	if path == "" {
		return nil, errors.New("ledger path is empty")
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	l := &Ledger{db: db, now: time.Now}
	if err := l.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the database.
func (l *Ledger) Close() error { return l.db.Close() }

var migrations = []string{
	`CREATE TABLE events (
		seq         INTEGER PRIMARY KEY,
		id          TEXT NOT NULL UNIQUE,
		type        TEXT NOT NULL,
		source      TEXT NOT NULL,
		subject     TEXT NOT NULL,
		time        TEXT NOT NULL,
		entity_kind TEXT NOT NULL DEFAULT '',
		entity_id   TEXT NOT NULL DEFAULT '',
		incident_id TEXT NOT NULL DEFAULT '',
		data        BLOB NOT NULL,
		prev_hash   TEXT NOT NULL,
		hash        TEXT NOT NULL
	);
	CREATE TRIGGER events_no_update BEFORE UPDATE ON events
		BEGIN SELECT RAISE(ABORT, 'ledger events are append-only'); END;
	CREATE TRIGGER events_no_delete BEFORE DELETE ON events
		BEGIN SELECT RAISE(ABORT, 'ledger events are append-only'); END;
	CREATE TABLE entities (
		kind        TEXT NOT NULL,
		id          TEXT NOT NULL,
		incident_id TEXT NOT NULL DEFAULT '',
		seq         INTEGER NOT NULL,
		data        BLOB NOT NULL,
		PRIMARY KEY (kind, id)
	);
	CREATE INDEX entities_by_incident ON entities (incident_id, kind);`,
	`CREATE INDEX events_by_incident ON events (incident_id, seq);
	CREATE INDEX events_by_type ON events (type, seq);`,
}

func (l *Ledger) migrate(ctx context.Context) error {
	if _, err := l.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var version int
	err := l.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := l.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("init schema_version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read schema_version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("ledger schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Append writes drafts as consecutive events in one transaction and updates
// their projections. Either all drafts are stored or none are.
func (l *Ledger) Append(ctx context.Context, drafts ...Draft) ([]Event, error) {
	if len(drafts) == 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	seq, prev, err := head(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(drafts))
	for _, d := range drafts {
		e, err := l.build(d, seq+1, prev)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (seq, id, type, source, subject, time, entity_kind, entity_id, incident_id, data, prev_hash, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Seq, e.ID, e.Type, e.Source, e.Subject, formatTime(e.Time),
			string(e.EntityKind), string(e.EntityID), string(e.IncidentID), []byte(e.Data), e.PrevHash, e.Hash); err != nil {
			return nil, fmt.Errorf("insert event %d: %w", e.Seq, err)
		}
		if err := project(ctx, tx, e); err != nil {
			return nil, err
		}
		out = append(out, e)
		seq, prev = e.Seq, e.Hash
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return out, nil
}

func (l *Ledger) build(d Draft, seq int64, prev string) (Event, error) {
	if d.Type == "" || d.Source == "" {
		return Event{}, errors.New("event type and source are required")
	}
	data, err := json.Marshal(d.Data)
	if err != nil {
		return Event{}, fmt.Errorf("marshal %s data: %w", d.Type, err)
	}
	e := Event{
		Seq:      seq,
		ID:       string(model.NewID()),
		Type:     d.Type,
		Source:   d.Source,
		Subject:  d.Subject,
		Time:     l.now().UTC(),
		Data:     data,
		PrevHash: prev,
	}
	if d.Entity != nil {
		if d.Entity.Kind == "" || d.Entity.ID == "" {
			return Event{}, fmt.Errorf("event %s names an entity without kind or id", d.Type)
		}
		e.EntityKind, e.EntityID, e.IncidentID = d.Entity.Kind, d.Entity.ID, d.Entity.IncidentID
	}
	e.Hash = e.computeHash()
	return e, nil
}

// computeHash covers every stored field. Each field is length-prefixed so no
// two different events can produce the same byte stream.
func (e Event) computeHash() string {
	h := sha256.New()
	var buf [binary.MaxVarintLen64]byte
	write := func(b []byte) {
		n := binary.PutUvarint(buf[:], uint64(len(b)))
		h.Write(buf[:n])
		h.Write(b)
	}
	for _, f := range []string{
		strconv.FormatInt(e.Seq, 10), e.ID, e.Type, e.Source, e.Subject, formatTime(e.Time),
		string(e.EntityKind), string(e.EntityID), string(e.IncidentID),
	} {
		write([]byte(f))
	}
	write(e.Data)
	write([]byte(e.PrevHash))
	return hex.EncodeToString(h.Sum(nil))
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func head(ctx context.Context, q querier) (int64, string, error) {
	var seq int64
	var hash string
	err := q.QueryRowContext(ctx, `SELECT seq, hash FROM events ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, GenesisHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read ledger head: %w", err)
	}
	return seq, hash, nil
}

// Head returns the sequence number and hash of the newest event. Publishing
// it somewhere Sentinel cannot rewrite makes later tampering detectable.
func (l *Ledger) Head(ctx context.Context) (int64, string, error) {
	return head(ctx, l.db)
}

func project(ctx context.Context, tx *sql.Tx, e Event) error {
	if e.EntityKind == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO entities (kind, id, incident_id, seq, data) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (kind, id) DO UPDATE SET incident_id = excluded.incident_id, seq = excluded.seq, data = excluded.data`,
		string(e.EntityKind), string(e.EntityID), string(e.IncidentID), e.Seq, []byte(e.Data))
	if err != nil {
		return fmt.Errorf("project event %d: %w", e.Seq, err)
	}
	return nil
}

// Events returns up to limit events with a sequence number greater than after.
func (l *Ledger) Events(ctx context.Context, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 1000
	}
	return l.queryEvents(ctx, `SELECT `+eventColumns+` FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
}

// IncidentEvents returns every event recorded for an incident, oldest first.
func (l *Ledger) IncidentEvents(ctx context.Context, incidentID model.ID) ([]Event, error) {
	return l.queryEvents(ctx, `SELECT `+eventColumns+` FROM events WHERE incident_id = ? ORDER BY seq`, string(incidentID))
}

// Latest returns up to limit events of one type, newest first.
func (l *Ledger) Latest(ctx context.Context, eventType string, limit int) ([]Event, error) {
	return l.queryEvents(ctx, `SELECT `+eventColumns+` FROM events WHERE type = ? ORDER BY seq DESC LIMIT ?`, eventType, limit)
}

const eventColumns = `seq, id, type, source, subject, time, entity_kind, entity_id, incident_id, data, prev_hash, hash`

// queryEvents runs one of the constant event queries above.
func (l *Ledger) queryEvents(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var t, kind, id, incident string
		var data []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.Source, &e.Subject, &t, &kind, &id, &incident, &data, &e.PrevHash, &e.Hash); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.EntityKind, e.EntityID, e.IncidentID = model.Kind(kind), model.ID(id), model.ID(incident)
		e.Data = data
		if e.Time, err = time.Parse(time.RFC3339Nano, t); err != nil {
			return nil, fmt.Errorf("event %d has a malformed time: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Verify walks the whole log and checks every hash and link. It returns the
// number of events checked, and an error wrapping ErrTampered at the first
// event that does not match.
func (l *Ledger) Verify(ctx context.Context) (int64, error) {
	prev := GenesisHash
	var checked, after int64
	for {
		batch, err := l.Events(ctx, after, 1000)
		if err != nil {
			return checked, err
		}
		if len(batch) == 0 {
			return checked, nil
		}
		for _, e := range batch {
			if e.Seq != after+1 {
				return checked, fmt.Errorf("%w: expected event %d, found %d", ErrTampered, after+1, e.Seq)
			}
			if e.PrevHash != prev {
				return checked, fmt.Errorf("%w: event %d does not link to event %d", ErrTampered, e.Seq, after)
			}
			if got := e.computeHash(); got != e.Hash {
				return checked, fmt.Errorf("%w: event %d content does not match its hash", ErrTampered, e.Seq)
			}
			prev, after = e.Hash, e.Seq
			checked++
		}
	}
}

// Get loads the current state of one record into out. It reports false when
// the record does not exist.
func (l *Ledger) Get(ctx context.Context, kind model.Kind, id model.ID, out any) (bool, error) {
	var data []byte
	err := l.db.QueryRowContext(ctx, `SELECT data FROM entities WHERE kind = ? AND id = ?`, string(kind), string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get %s %s: %w", kind, id, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("decode %s %s: %w", kind, id, err)
	}
	return true, nil
}

// List returns the current state of every record of a kind, in creation
// order. An empty incidentID lists records of every incident.
func List[T any](ctx context.Context, l *Ledger, kind model.Kind, incidentID model.ID) ([]T, error) {
	query := `SELECT data FROM entities WHERE kind = ? ORDER BY id`
	args := []any{string(kind)}
	if incidentID != "" {
		query = `SELECT data FROM entities WHERE kind = ? AND incident_id = ? ORDER BY id`
		args = append(args, string(incidentID))
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", kind, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Rebuild recomputes every projection from the event log.
func (l *Ledger) Rebuild(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM entities`); err != nil {
		return fmt.Errorf("clear projections: %w", err)
	}
	var after int64
	for {
		batch, err := l.Events(ctx, after, 1000)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			if err := project(ctx, tx, e); err != nil {
				return err
			}
			after = e.Seq
		}
	}
	return tx.Commit()
}

// CloudEvent renders the event as a CloudEvents 1.0 JSON envelope, with the
// Ledger fields carried as extension attributes.
func (e Event) CloudEvent() map[string]any {
	ce := map[string]any{
		"specversion":     "1.0",
		"id":              e.ID,
		"source":          e.Source,
		"type":            e.Type,
		"time":            formatTime(e.Time),
		"datacontenttype": "application/json",
		"data":            e.Data,
		"sequence":        strconv.FormatInt(e.Seq, 10),
		"prevhash":        e.PrevHash,
		"hash":            e.Hash,
	}
	if e.Subject != "" {
		ce["subject"] = e.Subject
	}
	if e.EntityKind != "" {
		ce["entitykind"] = string(e.EntityKind)
		ce["entityid"] = string(e.EntityID)
	}
	if e.IncidentID != "" {
		ce["incidentid"] = string(e.IncidentID)
	}
	return ce
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

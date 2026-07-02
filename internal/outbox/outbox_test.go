package outbox

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEnqueueDequeue(t *testing.T) {
	q, err := New(testDB(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	id, err := q.Enqueue(ctx, "test-svc", "default", "TypeError", "org/repo", "logs...")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty incident ID")
	}

	sig, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if sig.Service != "test-svc" {
		t.Errorf("service: got %q, want test-svc", sig.Service)
	}
	// Status is "pending" at SELECT time; the UPDATE to "processing"
	// happens after the struct is populated. The committed row is "processing".
	if sig.Status != StatusPending {
		t.Errorf("status at dequeue: got %q, want %q", sig.Status, StatusPending)
	}
	if sig.Attempts != 0 {
		t.Errorf("attempts at dequeue: got %d, want 0 (incremented after commit)", sig.Attempts)
	}
}

func TestEnqueueDedup(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	id1, _ := q.Enqueue(ctx, "svc", "ns", "reason", "", "")
	id2, _ := q.Enqueue(ctx, "svc", "ns", "reason", "", "")

	if id1 != id2 {
		t.Errorf("dedup failed: id1=%q id2=%q", id1, id2)
	}
}

func TestMarkDone(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	q.Enqueue(ctx, "svc", "ns", "reason", "", "")
	sig, _ := q.Dequeue(ctx)

	if err := q.MarkDone(ctx, sig.ID); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	// Queue should now be empty
	_, err := q.Dequeue(ctx)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after MarkDone, got %v", err)
	}
}

func TestMarkFailedRetry(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	q.Enqueue(ctx, "svc", "ns", "reason", "", "")
	sig, _ := q.Dequeue(ctx)

	// First failure — should go back to pending
	if err := q.MarkFailed(ctx, sig.ID, "test error"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	pending, _ := q.PendingCount(ctx)
	if pending != 1 {
		t.Errorf("expected 1 pending after failure, got %d", pending)
	}
}

func TestMarkFailedPermanentAfter3(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	q.Enqueue(ctx, "svc", "ns", "reason", "", "")

	for range 3 {
		sig, _ := q.Dequeue(ctx)
		q.MarkFailed(ctx, sig.ID, "test error")
	}

	pending, _ := q.PendingCount(ctx)
	if pending != 0 {
		t.Errorf("expected 0 pending after 3 failures, got %d", pending)
	}
}

func TestRecoverPending(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	q.Enqueue(ctx, "svc", "ns", "reason", "", "")
	_, _ = q.Dequeue(ctx) // marks as "processing"

	// Simulate crash — no MarkDone/MarkFailed called

	n, err := q.RecoverPending(ctx)
	if err != nil {
		t.Fatalf("RecoverPending: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 recovered, got %d", n)
	}

	pending, _ := q.PendingCount(ctx)
	if pending != 1 {
		t.Errorf("expected 1 pending after recovery, got %d", pending)
	}
}

func TestPendingCount(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	count, _ := q.PendingCount(ctx)
	if count != 0 {
		t.Errorf("expected 0 pending, got %d", count)
	}

	q.Enqueue(ctx, "svc1", "ns", "reason", "", "")
	q.Enqueue(ctx, "svc2", "ns", "reason", "", "")

	count, _ = q.PendingCount(ctx)
	if count != 2 {
		t.Errorf("expected 2 pending, got %d", count)
	}
}

func TestSignalFields(t *testing.T) {
	q, _ := New(testDB(t))
	ctx := context.Background()

	q.Enqueue(ctx, "svc", "ns", "TypeError: bad", "org/repo", "traceback...")
	sig, _ := q.Dequeue(ctx)

	if sig.Reason != "TypeError: bad" {
		t.Errorf("reason: got %q", sig.Reason)
	}
	if sig.Repo != "org/repo" {
		t.Errorf("repo: got %q", sig.Repo)
	}
	if sig.RawLogs != "traceback..." {
		t.Errorf("rawLogs: got %q", sig.RawLogs)
	}
	if sig.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if time.Since(sig.CreatedAt) > 5*time.Second {
		t.Error("CreatedAt should be recent")
	}
}

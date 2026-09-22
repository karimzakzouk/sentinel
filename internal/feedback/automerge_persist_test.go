package feedback

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/karimzakzouk/sentinel/internal/automerge"
)

// TestTrackedPRSQLiteRoundTrip verifies the real SQLite-backed
// TrackedPRStore: save two records, list them, upsert one, delete one.
func TestTrackedPRSQLiteRoundTrip(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	inputJSON := []byte(`{"incident_id":"inc-1","service":"api","namespace":"sim","confidence":0.93,"sandbox_verified":true,"sandbox_levels":"L1-L5"}`)

	rec1 := automerge.TrackedPRRecord{
		IncidentID: "inc-1",
		Repo:       "org/repo",
		PRNumber:   42,
		Branch:     "sentinel/api-abc12345",
		BaseBranch: "main",
		InputJSON:  inputJSON,
		AddedAt:    time.Now().UTC().Truncate(time.Second),
	}
	rec2 := automerge.TrackedPRRecord{
		IncidentID: "inc-2",
		Repo:       "org/other",
		PRNumber:   7,
		Branch:     "sentinel/worker-def67890",
		BaseBranch: "main",
		InputJSON:  inputJSON,
		AddedAt:    time.Now().UTC().Add(time.Minute).Truncate(time.Second),
	}

	if err := store.SaveTrackedPR(ctx, rec1); err != nil {
		t.Fatalf("SaveTrackedPR rec1: %v", err)
	}
	if err := store.SaveTrackedPR(ctx, rec2); err != nil {
		t.Fatalf("SaveTrackedPR rec2: %v", err)
	}

	got, err := store.ListTrackedPRs(ctx)
	if err != nil {
		t.Fatalf("ListTrackedPRs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d records, want 2", len(got))
	}

	// Upsert rec1 with a new branch name — must overwrite, not duplicate.
	rec1.Branch = "sentinel/api-updated"
	if err := store.SaveTrackedPR(ctx, rec1); err != nil {
		t.Fatalf("SaveTrackedPR upsert: %v", err)
	}
	got, err = store.ListTrackedPRs(ctx)
	if err != nil {
		t.Fatalf("ListTrackedPRs after upsert: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("upsert created a duplicate: %d records, want 2", len(got))
	}

	if err := store.DeleteTrackedPR(ctx, "org/repo", 42); err != nil {
		t.Fatalf("DeleteTrackedPR: %v", err)
	}
	got, err = store.ListTrackedPRs(ctx)
	if err != nil {
		t.Fatalf("ListTrackedPRs after delete: %v", err)
	}
	if len(got) != 1 || got[0].Repo != "org/other" {
		t.Fatalf("after delete got %+v, want only org/other", got)
	}

	// Fields survive the round trip.
	if got[0].IncidentID != "inc-2" || got[0].PRNumber != 7 || got[0].BaseBranch != "main" {
		t.Errorf("field mismatch after round trip: %+v", got[0])
	}
}

// TestTrackedPRStoreImplementsInterface is a compile-time guarantee that
// feedback.Store satisfies automerge.TrackedPRStore (the merger's
// persistence contract).
func TestTrackedPRStoreImplementsInterface(t *testing.T) {
	var _ automerge.TrackedPRStore = (*Store)(nil)
}

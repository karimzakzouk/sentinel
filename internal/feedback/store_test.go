package feedback

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// newTestStore opens a SQLite DB in a temp file and returns a Store backed
// by it. The caller does not need to close it — t.Cleanup handles that.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// makeIncident returns a minimal incident for store tests.
func makeIncident(service, namespace string) *schemas.Incident {
	cluster := schemas.AnomalyCluster{
		ClusterID: uuid.New(),
		Service:   service,
		Namespace: namespace,
		Summary:   "test cluster summary",
		Severity:  schemas.SeverityWarning,
	}
	inc := schemas.NewIncident(cluster)
	inc.Status = schemas.StatusInvestigating
	return &inc
}

// TestStore_UpsertAndRoundTrip verifies UpsertIncident + GetIncident.
func TestStore_UpsertAndRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inc := makeIncident("api", "prod")
	inc.Status = schemas.StatusInvestigating

	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("UpsertIncident: %v", err)
	}

	got, err := s.GetIncident(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if got == nil {
		t.Fatal("GetIncident returned nil after Upsert")
	}
	if got.IncidentID != inc.IncidentID {
		t.Errorf("IncidentID = %s, want %s", got.IncidentID, inc.IncidentID)
	}
	if got.Cluster.Service != "api" {
		t.Errorf("Cluster.Service = %q, want api", got.Cluster.Service)
	}
	if got.Cluster.Namespace != "prod" {
		t.Errorf("Cluster.Namespace = %q, want prod", got.Cluster.Namespace)
	}
	if got.Status != schemas.StatusInvestigating {
		t.Errorf("Status = %s, want investigating", got.Status)
	}
}

// TestStore_UpsertIsIdempotent verifies a second Upsert updates the row
// instead of duplicating.
func TestStore_UpsertIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inc := makeIncident("api", "prod")
	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	inc.Status = schemas.StatusResolved
	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, _ := s.GetIncident(ctx, inc.IncidentID)
	if got.Status != schemas.StatusResolved {
		t.Errorf("Status = %s, want resolved (update path)", got.Status)
	}

	list, err := s.ListRecentIncidents(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentIncidents: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListRecentIncidents returned %d rows, want 1 (no duplicates)", len(list))
	}
}

// TestStore_FindIncidentByPrefix verifies the prefix lookup.
func TestStore_FindIncidentByPrefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inc := makeIncident("api", "prod")
	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 4-char prefix of the UUID (no hyphens).
	prefix := inc.IncidentID.String()[:8]
	got, err := s.FindIncidentByPrefix(ctx, prefix)
	if err != nil {
		t.Fatalf("FindIncidentByPrefix: %v", err)
	}
	if got == nil {
		t.Fatalf("FindIncidentByPrefix returned nil for prefix %q", prefix)
	}
	if got.IncidentID != inc.IncidentID {
		t.Errorf("IncidentID = %s, want %s", got.IncidentID, inc.IncidentID)
	}

	// Prefix shorter than 4 chars returns nil.
	if got, _ := s.FindIncidentByPrefix(ctx, "ab"); got != nil {
		t.Errorf("prefix < 4 chars should return nil, got %v", got)
	}

	// Non-matching prefix returns nil.
	if got, _ := s.FindIncidentByPrefix(ctx, "zzzzzzzz"); got != nil {
		t.Errorf("non-matching prefix should return nil, got %v", got)
	}
}

// TestStore_ListRecentIncidents verifies limit clamping + ordering.
func TestStore_ListRecentIncidents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		inc := makeIncident("api", "prod")
		// Stagger UpdatedAt so the ordering is deterministic.
		inc.UpdatedAt = time.Now().Add(-time.Duration(i) * time.Minute)
		if err := s.UpsertIncident(ctx, inc); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	// limit=3 returns the 3 most recent.
	got, err := s.ListRecentIncidents(ctx, 3)
	if err != nil {
		t.Fatalf("ListRecentIncidents: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}

	// limit=0 should clamp to the default (50) — at most 5 returned.
	got, _ = s.ListRecentIncidents(ctx, 0)
	if len(got) != 5 {
		t.Errorf("len = %d, want 5 (limit=0 clamps to default 50)", len(got))
	}

	// limit=10000 should clamp to 500 — at most 5 returned.
	got, _ = s.ListRecentIncidents(ctx, 10000)
	if len(got) != 5 {
		t.Errorf("len = %d, want 5 (limit clamped to 500)", len(got))
	}
}

// TestStore_GetIncidentMissing verifies GetIncident on a non-existent ID.
func TestStore_GetIncidentMissing(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetIncident(context.Background(), uuid.New())
	if err != nil {
		t.Errorf("GetIncident on missing ID returned err: %v", err)
	}
	if got != nil {
		t.Errorf("GetIncident on missing ID returned %v, want nil", got)
	}
}

// TestFeedback_StoreCRUD is the v1 regression test: end-to-end
// UpsertIncident → GetIncident → FindIncidentByPrefix → ListRecentIncidents
// → FindIncidentByPR with a real SQLite file. Catches schema migrations
// that drift from the Go struct fields.
func TestFeedback_StoreCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 1. Create.
	inc := makeIncident("api", "prod")
	inc.Status = schemas.StatusFixProposed
	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("UpsertIncident: %v", err)
	}

	// 2. Read.
	got, err := s.GetIncident(ctx, inc.IncidentID)
	if err != nil || got == nil {
		t.Fatalf("GetIncident: err=%v inc=%v", err, got)
	}
	if got.Status != schemas.StatusFixProposed {
		t.Errorf("Status = %s, want fix_proposed", got.Status)
	}

	// 3. Update (PR metadata).
	if err := s.UpdateIncidentPR(ctx, inc.IncidentID, "acme/api", 42, "fix-branch", "https://github.com/acme/api/pull/42"); err != nil {
		t.Fatalf("UpdateIncidentPR: %v", err)
	}
	if err := s.UpdateAutoMergeStatus(ctx, inc.IncidentID, "pending_ci"); err != nil {
		t.Fatalf("UpdateAutoMergeStatus: %v", err)
	}

	// 4. Find by PR.
	got2, err := s.FindIncidentByPR(ctx, "acme/api", 42)
	if err != nil {
		t.Fatalf("FindIncidentByPR: %v", err)
	}
	if got2 == nil || got2.IncidentID != inc.IncidentID {
		t.Errorf("FindIncidentByPR returned %v, want our incident", got2)
	}

	// 5. List recent.
	list, err := s.ListRecentIncidents(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentIncidents: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1", len(list))
	}

	// 6. AppendEvent + (no ListEvents exposed; just verify no error).
	if err := s.AppendEvent(ctx, inc.IncidentID, "investigation", "hypothesis_formed", strPtr("investigator"), map[string]any{"h": "key"}); err != nil {
		t.Errorf("AppendEvent: %v", err)
	}

	// 7. UpdateOverrides (read-modify-write on state_json).
	if err := s.UpdateOverrides(ctx, inc.IncidentID, true, false); err != nil {
		t.Errorf("UpdateOverrides: %v", err)
	}
	got3, _ := s.GetIncident(ctx, inc.IncidentID)
	if got3 == nil || got3.State.ExecutionResult == nil || !got3.State.ExecutionResult.SandboxOverride {
		t.Errorf("UpdateOverrides did not persist SandboxOverride=true, got state=%+v", got3.State.ExecutionResult)
	}

	// 8. LogCommandEvent + ListCommandEvents.
	ev := &CommandEvent{
		IncidentID:       inc.IncidentID.String(),
		PRNumber:         42,
		PRRepo:           "acme/api",
		Command:          "approve",
		Author:           "alice",
		AuthorPermission: "admin",
		Result:           "ok",
		Timestamp:        time.Now().UTC(),
	}
	if err := s.LogCommandEvent(ctx, ev); err != nil {
		t.Fatalf("LogCommandEvent: %v", err)
	}
	events, err := s.ListCommandEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListCommandEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("len(events) = %d, want 1", len(events))
	}
}

// TestFeedback_OutcomeUpdatesBetaPriors is the v1 regression test:
// RecordBetaOutcome must increment the right counter (alpha for success,
// beta for failure) and persist across GetBetaPrior reads. A second
// success on the same (service, fix_type) must increment alpha again.
func TestFeedback_OutcomeUpdatesBetaPriors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Fresh prior: (1.0, 1.0, 0, 0).
	a, b, succ, fail, err := s.GetBetaPrior(ctx, "api", "code_fix")
	if err != nil {
		t.Fatalf("GetBetaPrior: %v", err)
	}
	if a != 1.0 || b != 1.0 || succ != 0 || fail != 0 {
		t.Errorf("fresh prior = (%.1f, %.1f, %d, %d), want (1.0, 1.0, 0, 0)", a, b, succ, fail)
	}

	// Record a success → (2.0, 1.0, 1, 0).
	a, b, err = s.RecordBetaOutcome(ctx, "api", "code_fix", true)
	if err != nil {
		t.Fatalf("RecordBetaOutcome success: %v", err)
	}
	if a != 2.0 || b != 1.0 {
		t.Errorf("after success: (%.1f, %.1f), want (2.0, 1.0)", a, b)
	}

	// Record another success → (3.0, 1.0, 2, 0).
	if _, _, err := s.RecordBetaOutcome(ctx, "api", "code_fix", true); err != nil {
		t.Fatalf("RecordBetaOutcome 2nd success: %v", err)
	}
	a, b, _, _, _ = s.GetBetaPrior(ctx, "api", "code_fix")
	if a != 3.0 || b != 1.0 {
		t.Errorf("after 2 successes: (%.1f, %.1f), want (3.0, 1.0)", a, b)
	}

	// Record a failure → (3.0, 2.0, 2, 1).
	a, b, err = s.RecordBetaOutcome(ctx, "api", "code_fix", false)
	if err != nil {
		t.Fatalf("RecordBetaOutcome failure: %v", err)
	}
	if a != 3.0 || b != 2.0 {
		t.Errorf("after failure: (%.1f, %.1f), want (3.0, 2.0)", a, b)
	}

	_, _, succ, fail, _ = s.GetBetaPrior(ctx, "api", "code_fix")
	if succ != 2 || fail != 1 {
		t.Errorf("counts = (%d succ, %d fail), want (2, 1)", succ, fail)
	}

	// Different (service, fix_type) is tracked independently.
	a2, b2, _, _, _ := s.GetBetaPrior(ctx, "worker", "restart")
	if a2 != 1.0 || b2 != 1.0 {
		t.Errorf("independent prior should be fresh (1.0, 1.0), got (%.1f, %.1f)", a2, b2)
	}
}

// TestStore_RecordBetaOutcomeDifferentFixTypes verifies per-fix-type
// isolation.
func TestStore_RecordBetaOutcomeDifferentFixTypes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RecordBetaOutcome(ctx, "api", "code_fix", true)
	s.RecordBetaOutcome(ctx, "api", "code_fix", true)
	s.RecordBetaOutcome(ctx, "api", "rollback", false)

	a, b, succ, fail, _ := s.GetBetaPrior(ctx, "api", "code_fix")
	if a != 3.0 || b != 1.0 || succ != 2 || fail != 0 {
		t.Errorf("code_fix prior = (%.1f, %.1f, %d, %d), want (3.0, 1.0, 2, 0)", a, b, succ, fail)
	}
	a, b, succ, fail, _ = s.GetBetaPrior(ctx, "api", "rollback")
	if a != 1.0 || b != 2.0 || succ != 0 || fail != 1 {
		t.Errorf("rollback prior = (%.1f, %.1f, %d, %d), want (1.0, 2.0, 0, 1)", a, b, succ, fail)
	}
}

// TestStore_UpdateVectorOutcome verifies the vector-outcome UPDATE.
func TestStore_UpdateVectorOutcome(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inc := makeIncident("api", "prod")
	inc.State.RootCause = &schemas.RootCause{
		Statement: "missing env var",
		Category:  "config",
	}
	inc.State.FixProposal = &schemas.FixProposal{
		FixType:     schemas.FixCodeFix,
		Description: "add env var",
	}
	inc.Status = schemas.StatusVerified
	if err := s.UpsertIncident(ctx, inc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	embedding := []float32{0.1, 0.2, 0.3}
	if err := s.StoreIncidentVector(ctx, inc, false, embedding); err != nil {
		t.Fatalf("StoreIncidentVector: %v", err)
	}
	if err := s.UpdateVectorOutcome(ctx, inc.IncidentID.String(), true); err != nil {
		t.Fatalf("UpdateVectorOutcome: %v", err)
	}
	// No reader for the vector row's outcome flag — but no error means
	// the UPDATE ran cleanly against the stored row.
}

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

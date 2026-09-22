package automerge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeTrackedPRStore is an in-memory TrackedPRStore for tests.
type fakeTrackedPRStore struct {
	mu      sync.Mutex
	records map[string]TrackedPRRecord
}

func newFakeTrackedPRStore() *fakeTrackedPRStore {
	return &fakeTrackedPRStore{records: make(map[string]TrackedPRRecord)}
}

func (f *fakeTrackedPRStore) SaveTrackedPR(_ context.Context, rec TrackedPRRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[persistenceKey(rec)] = rec
	return nil
}

func (f *fakeTrackedPRStore) DeleteTrackedPR(_ context.Context, repo string, prNumber int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, prKey(repo, prNumber))
	return nil
}

func (f *fakeTrackedPRStore) ListTrackedPRs(_ context.Context) ([]TrackedPRRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TrackedPRRecord, 0, len(f.records))
	for _, rec := range f.records {
		out = append(out, rec)
	}
	return out, nil
}

func (f *fakeTrackedPRStore) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// fakeStatusStore captures the last auto-merge status written per
// incident, so tests can assert on the terminal state.
type fakeStatusStore struct {
	mu         sync.Mutex
	lastStatus string
}

func (f *fakeStatusStore) UpdateAutoMergeStatus(_ context.Context, incidentID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastStatus = status
	return nil
}

// newMergerForReconcile builds a merger wired with fakes, mimicking
// main.go's wiring (persistence + status store).
func newMergerForReconcile(t *testing.T) (*Merger, *mockGitHubClient, *fakeTrackedPRStore, *fakeStatusStore) {
	t.Helper()
	gh := &mockGitHubClient{}
	status := &fakeStatusStore{}
	persist := newFakeTrackedPRStore()
	m := NewMerger(NewPolicy(DefaultConfig(), nil), gh, status, nil)
	m.SetPersistence(persist)
	return m, gh, persist, status
}

func validTrackedPR(repo string, prNumber int) *TrackedPR {
	return &TrackedPR{
		IncidentID: fmt.Sprintf("inc-%d", prNumber),
		Repo:       repo,
		PRNumber:   prNumber,
		Branch:     "sentinel/api-abc12345",
		BaseBranch: "main",
		Input: Input{
			IncidentID:          fmt.Sprintf("inc-%d", prNumber),
			Service:             "api",
			Namespace:           "sim",
			FixType:             "code_fix",
			Confidence:          0.93,
			SandboxVerified:     true,
			SandboxLevels:       "L1-L5",
			PolicyVerdict:       "allow",
			AutoMergeAnnotation: true,
		},
	}
}

// TestTrackPersistsAcrossRestart simulates: Track → restart (new merger
// with same persistence) → Reconcile → PR still tracked.
func TestTrackPersistsAcrossRestart(t *testing.T) {
	m1, _, persist, _ := newMergerForReconcile(t)
	m1.Track(validTrackedPR("org/repo", 42))

	if persist.len() != 1 {
		t.Fatalf("persisted records = %d, want 1", persist.len())
	}

	// Simulate a restart: brand-new merger, same persistence layer.
	gh := &mockGitHubClient{} // prClosed=false → PR still open
	status := &fakeStatusStore{}
	m2 := NewMerger(NewPolicy(DefaultConfig(), nil), gh, status, nil)
	m2.SetPersistence(persist)
	m2.Reconcile(context.Background())

	if !m2.IsTracked("org/repo", 42) {
		t.Error("PR should be re-adopted after restart via Reconcile")
	}
	if status.lastStatus != "" {
		t.Errorf("status should not change for adopted PR, got %q", status.lastStatus)
	}
}

// TestReconcilePrunesClosedPRs verifies that PRs merged/closed while
// Sentinel was down are dropped, not adopted.
func TestReconcilePrunesClosedPRs(t *testing.T) {
	m1, _, persist, _ := newMergerForReconcile(t)
	m1.Track(validTrackedPR("org/repo", 42))

	// Restart with the PR closed (human merged it during downtime).
	gh := &mockGitHubClient{prClosed: true}
	status := &fakeStatusStore{}
	m2 := NewMerger(NewPolicy(DefaultConfig(), nil), gh, status, nil)
	m2.SetPersistence(persist)
	m2.Reconcile(context.Background())

	if m2.IsTracked("org/repo", 42) {
		t.Error("closed PR must not be adopted")
	}
	if persist.len() != 0 {
		t.Errorf("closed PR record should be pruned, %d left", persist.len())
	}
	if status.lastStatus != "skipped:closed_while_down" {
		t.Errorf("incident status = %q, want skipped:closed_while_down", status.lastStatus)
	}
}

// TestEvaluateUntracksClosedPR verifies the runtime path: a PR that gets
// closed by a human mid-flight is untracked on the next evaluation.
func TestEvaluateUntracksClosedPR(t *testing.T) {
	m, gh, persist, status := newMergerForReconcile(t)
	m.Track(validTrackedPR("org/repo", 42))
	gh.mu.Lock()
	gh.prClosed = true
	gh.mu.Unlock()

	m.EvaluateNow(context.Background(), "org/repo", 42)

	if m.IsTracked("org/repo", 42) {
		t.Error("closed PR should be untracked after evaluation")
	}
	if persist.len() != 0 {
		t.Errorf("persistence should be cleaned, %d records left", persist.len())
	}
	if status.lastStatus != "closed_before_merge" {
		t.Errorf("status = %q, want closed_before_merge", status.lastStatus)
	}
}

// TestTrackedPRRecordRoundTrip verifies Input survives JSON serialization.
func TestTrackedPRRecordRoundTrip(t *testing.T) {
	pr := validTrackedPR("org/repo", 7)
	rec, err := pr.toRecord()
	if err != nil {
		t.Fatalf("toRecord: %v", err)
	}
	restored, err := rec.toTrackedPR()
	if err != nil {
		t.Fatalf("toTrackedPR: %v", err)
	}
	if restored.IncidentID != pr.IncidentID || restored.Repo != pr.Repo ||
		restored.PRNumber != pr.PRNumber || restored.BaseBranch != pr.BaseBranch {
		t.Errorf("round-trip mismatch: %+v vs %+v", restored, pr)
	}
	if restored.Input.Confidence != pr.Input.Confidence ||
		restored.Input.SandboxLevels != pr.Input.SandboxLevels ||
		!restored.Input.AutoMergeAnnotation {
		t.Errorf("Input round-trip mismatch: %+v vs %+v", restored.Input, pr.Input)
	}
}

// TestReconcileCorruptRecordPruned: a record with invalid JSON is deleted,
// not fatal.
func TestReconcileCorruptRecordPruned(t *testing.T) {
	_, _, persist, _ := newMergerForReconcile(t)
	_ = persist.SaveTrackedPR(context.Background(), TrackedPRRecord{
		IncidentID: "inc-x",
		Repo:       "org/repo",
		PRNumber:   99,
		InputJSON:  []byte("{not valid json"),
		AddedAt:    time.Now(),
	})

	gh := &mockGitHubClient{}
	status := &fakeStatusStore{}
	m := NewMerger(NewPolicy(DefaultConfig(), nil), gh, status, nil)
	m.SetPersistence(persist)
	m.Reconcile(context.Background())

	if persist.len() != 0 {
		t.Errorf("corrupt record should be pruned, %d left", persist.len())
	}
	if m.IsTracked("org/repo", 99) {
		t.Error("corrupt record must not be adopted")
	}
}
